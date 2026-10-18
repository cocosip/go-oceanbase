// Command obtest runs an end-to-end OceanBase acceptance suite against configured databases.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"time"

	"github.com/cocosip/go-oceanbase"
)

type configuration struct {
	Targets        []targetConfig `json:"targets"`
	Cases          []string       `json:"cases,omitempty"`
	Timeout        string         `json:"timeout,omitempty"`
	CleanupTimeout string         `json:"cleanup_timeout,omitempty"`
	RequireAll     bool           `json:"require_all,omitempty"`
}

type targetConfig struct {
	Name             string `json:"name"`
	DSN              string `json:"dsn,omitempty"`
	DSNEnv           string `json:"dsn_env,omitempty"`
	ChangeUserDSNEnv string `json:"change_user_dsn_env,omitempty"`
	LocalImportSQL   string `json:"local_import_sql,omitempty"`
	WarningSQL       string `json:"warning_sql,omitempty"`
	CancelSQL        string `json:"cancel_sql,omitempty"`
	CancelCode       uint32 `json:"cancel_code,omitempty"`
}

var targetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func readConfig(r io.Reader) (configuration, error) {
	var cfg configuration
	const maxConfigBytes = 1 << 20
	data, err := io.ReadAll(io.LimitReader(r, maxConfigBytes+1))
	if err != nil {
		return cfg, errors.New("cannot read configuration")
	}
	if len(data) > maxConfigBytes {
		return cfg, errors.New("configuration exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&cfg); err != nil {
		return cfg, errors.New("invalid configuration JSON or unknown field")
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return cfg, errors.New("configuration must contain one JSON object")
	}
	if len(cfg.Targets) == 0 {
		return cfg, errors.New("at least one target is required")
	}
	seen := map[string]bool{}
	for _, t := range cfg.Targets {
		if !targetName.MatchString(t.Name) || seen[t.Name] {
			return cfg, errors.New("target names must be unique ASCII identifiers of 1-64 characters")
		}
		seen[t.Name] = true
		if (t.DSN == "") == (t.DSNEnv == "") {
			return cfg, errors.New("each target requires exactly one of dsn and dsn_env")
		}
	}
	if cfg.Timeout == "" {
		cfg.Timeout = "2m"
	}
	if cfg.CleanupTimeout == "" {
		cfg.CleanupTimeout = "30s"
	}
	for _, value := range []string{cfg.Timeout, cfg.CleanupTimeout} {
		v, err := time.ParseDuration(value)
		if err != nil || v <= 0 || v > time.Hour {
			return cfg, errors.New("timeouts must be positive durations no greater than 1h")
		}
	}
	known := map[string]bool{}
	for _, c := range caseDefinitions() {
		known[c.name] = true
	}
	seen = map[string]bool{}
	for _, name := range cfg.Cases {
		if !known[name] || seen[name] {
			return cfg, errors.New("cases contains an unknown or duplicate scenario")
		}
		seen[name] = true
	}
	return cfg, nil
}

func (t targetConfig) driverConfig() (oceanbase.Config, error) {
	dsn := t.DSN
	if t.DSNEnv != "" {
		dsn = os.Getenv(t.DSNEnv)
	}
	if dsn == "" {
		return oceanbase.Config{}, errors.New("configured DSN environment variable is empty")
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		return cfg, errors.New("invalid target DSN")
	}
	// Acceptance fixtures must never cause creation of a database outside their manifest.
	if cfg.CreateDatabaseIfMissing {
		return cfg, errors.New("createDatabaseIfMissing is not allowed in acceptance targets")
	}
	return cfg, nil
}

func main() { os.Exit(command(os.Args[1:], os.Stdout, os.Stderr)) }

func command(args []string, out, stderr io.Writer) int {
	f := flag.NewFlagSet("obtest", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("config", "", "JSON configuration file (default: OB_DSN environment variable)")
	list := f.Bool("list", false, "list scenarios without connecting")
	check := f.Bool("check", false, "validate configuration without connecting")
	reportPath := f.String("report", "obtest-report.json", "JSON report destination")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "unexpected positional arguments")
		return 2
	}
	if *list {
		for _, c := range caseDefinitions() {
			_, _ = fmt.Fprintf(out, "%s\t%s\n", c.name, c.description)
		}
		return 0
	}
	cfg := configuration{Targets: []targetConfig{{Name: "default", DSNEnv: "OB_DSN"}}, Timeout: "2m", CleanupTimeout: "30s"}
	if *path != "" {
		file, err := os.Open(*path)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "cannot open configuration file")
			return 2
		}
		cfg, err = readConfig(file)
		_ = file.Close()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
	}
	configs := make([]oceanbase.Config, len(cfg.Targets))
	for i, t := range cfg.Targets {
		var err error
		configs[i], err = t.driverConfig()
		if err == nil {
			var connector io.Closer
			c, e := oceanbase.NewConnector(configs[i])
			err = e
			if err == nil {
				connector, _ = c.(io.Closer)
				if connector != nil {
					err = connector.Close()
				}
			}
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: invalid connection configuration (%s)\n", t.Name, errorSummary(err))
			return 2
		}
	}
	if *check {
		_, _ = fmt.Fprintf(out, "Configuration valid: %d target(s); no database connection made\n", len(cfg.Targets))
		return 0
	}
	// Create the report before the first connection, so an invalid destination has no DB effects.
	reportFile, err := os.OpenFile(*reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot create report (choose a new, writable path)")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	r := runSuite(ctx, cfg, configs, out)
	e := json.NewEncoder(reportFile)
	e.SetIndent("", "  ")
	err = e.Encode(r)
	err = errors.Join(err, reportFile.Close())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "cannot write complete report")
		return 2
	}
	_, _ = fmt.Fprintf(out, "pass=%d fail=%d skip=%d not_applicable=%d\n", r.count(statusPass), r.count(statusFail), r.count(statusSkip), r.count(statusNA))
	return r.exitCode(cfg.RequireAll)
}
