package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const (
	statusPass    = "pass"
	statusFail    = "fail"
	statusSkip    = "skip"
	statusNA      = "not_applicable"
	dualSuffix    = " FROM dual"
	unicodeValue  = "中文🙂"
	exactNumber   = "12345678901234567890.123456789"
	seedOne       = "one"
	seedTwo       = "two"
	seedChanged   = "changed"
	blobType      = "BLOB"
	cleanupFailed = "cleanup_failed"
	xaOnePhase    = "one-phase"
)

type caseDefinition struct {
	name, description string
	oracleOnly        bool
	mysqlOnly         bool
	configure         func(*oceanbase.Config)
	run               func(context.Context, *scenario) error
}

type report struct {
	RunID     string       `json:"run_id"`
	Started   time.Time    `json:"started"`
	Duration  string       `json:"duration"`
	GoVersion string       `json:"go_version"`
	Platform  string       `json:"platform"`
	Selected  []string     `json:"selected_cases"`
	Results   []caseResult `json:"results"`
}

type caseResult struct {
	Target        string            `json:"target"`
	Case          string            `json:"case"`
	Mode          string            `json:"mode,omitempty"`
	Status        string            `json:"status"`
	Duration      string            `json:"duration"`
	Message       string            `json:"message,omitempty"`
	CleanupErrors []string          `json:"cleanup_errors,omitempty"`
	Objects       []*objectRecord   `json:"objects,omitempty"`
	Observations  map[string]string `json:"observations,omitempty"`
}

type objectRecord struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type cleanupAction struct {
	name string
	run  func(context.Context) error
}

type scenario struct {
	db             *sql.DB
	cfg            oceanbase.Config
	target         targetConfig
	mode           oceanbase.Mode
	prefix         string
	cleanupTimeout time.Duration
	cleanups       []cleanupAction
	objects        []*objectRecord
	observations   map[string]string
}

type checkFailure string

func (e checkFailure) Error() string { return string(e) }

type skipped string

func (e skipped) Error() string { return string(e) }

func errorSummary(err error) string {
	if err == nil {
		return ""
	}
	var assertion checkFailure
	if errors.As(err, &assertion) {
		return string(assertion)
	}
	var skip skipped
	if errors.As(err, &skip) {
		return string(skip)
	}
	if errors.Is(err, context.Canceled) {
		return "operation canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation deadline exceeded"
	}
	var server *oceanbase.Error
	if errors.As(err, &server) {
		return fmt.Sprintf("server error code=%d", server.Code)
	}
	if errors.Is(err, oceanbase.ErrUnsupported) {
		return "capability unsupported"
	}
	return fmt.Sprintf("operation failed (%T; raw message omitted)", err)
}

func (r report) count(status string) int {
	n := 0
	for _, item := range r.Results {
		if item.Status == status {
			n++
		}
	}
	return n
}

func (r report) exitCode(strict bool) int {
	if r.count(statusFail) != 0 || strict && r.count(statusSkip) != 0 {
		return 1
	}
	if r.count(statusSkip) != 0 || r.count(statusPass) == 0 {
		return 3
	}
	return 0
}

func runScenario(ctx context.Context, s *scenario, execute func(context.Context, *scenario) error) caseResult {
	start := time.Now()
	result := caseResult{Status: statusPass}
	err := execute(ctx, s)
	if err != nil {
		result.Status, result.Message = statusFail, errorSummary(err)
		var skip skipped
		if errors.As(err, &skip) {
			result.Status = statusSkip
		}
	}
	// Every cleanup gets its own budget: one failure must not suppress later drops.
	for i := len(s.cleanups) - 1; i >= 0; i-- {
		action := s.cleanups[i]
		cleanupCtx, cancel := context.WithTimeout(context.Background(), s.cleanupTimeout)
		err = action.run(cleanupCtx)
		cancel()
		if err != nil {
			result.CleanupErrors = append(result.CleanupErrors, action.name+": "+errorSummary(err))
			result.Status = statusFail
		}
	}
	result.Objects = s.objects
	result.Observations = s.observations
	result.Duration = time.Since(start).String()
	return result
}

func runSuite(ctx context.Context, cfg configuration, configs []oceanbase.Config, out io.Writer) report {
	start := time.Now()
	r := report{Started: start.UTC(), GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		r.Results = append(r.Results, caseResult{Case: "startup", Status: statusFail, Message: "cannot generate run identity"})
		return r
	}
	r.RunID = hex.EncodeToString(nonce)
	definitions := caseDefinitions()
	selected := map[string]bool{}
	for _, name := range cfg.Cases {
		selected[name] = true
	}
	for _, c := range definitions {
		if len(selected) == 0 || selected[c.name] {
			r.Selected = append(r.Selected, c.name)
		}
	}
	timeout, _ := time.ParseDuration(cfg.Timeout)
	cleanupTimeout, _ := time.ParseDuration(cfg.CleanupTimeout)
	for i, t := range cfg.Targets {
		mode, err := detectMode(ctx, configs[i], timeout)
		if err != nil {
			r.Results = append(r.Results, caseResult{Target: t.Name, Case: "target-connect", Status: statusFail, Message: errorSummary(err)})
			for _, name := range r.Selected {
				r.Results = append(r.Results, caseResult{Target: t.Name, Case: name, Status: statusSkip, Message: "target connection failed"})
			}
			_, _ = fmt.Fprintf(out, "%s connect fail: %s\n", t.Name, errorSummary(err))
			continue
		}
		for j, c := range definitions {
			if len(selected) != 0 && !selected[c.name] {
				continue
			}
			result := caseResult{Status: statusNA, Message: "Oracle compatibility mode only"}
			if c.mysqlOnly {
				result.Message = "MySQL compatibility mode only"
			}
			if (!c.oracleOnly || mode == oceanbase.ModeOracle) && (!c.mysqlOnly || mode == oceanbase.ModeMySQL) {
				s := &scenario{cfg: configs[i], target: t, mode: mode, prefix: fmt.Sprintf("OBT_%s_%d_%d", strings.ToUpper(r.RunID), i, j), cleanupTimeout: cleanupTimeout}
				s.cfg.Mode = mode
				if c.configure != nil {
					c.configure(&s.cfg)
				}
				caseCtx, cancel := context.WithTimeout(ctx, timeout)
				result = runScenario(caseCtx, s, func(ctx context.Context, s *scenario) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					var err error
					s.db, err = openDatabase(s.cfg)
					if err != nil {
						return err
					}
					s.cleanups = append(s.cleanups, cleanupAction{"close database", func(context.Context) error { return s.db.Close() }})
					return c.run(ctx, s)
				})
				cancel()
			}
			result.Target, result.Case, result.Mode = t.Name, c.name, modeName(mode)
			r.Results = append(r.Results, result)
			_, _ = fmt.Fprintf(out, "%s %s %s (%s) %s\n", t.Name, c.name, result.Status, result.Duration, result.Message)
		}
	}
	r.Duration = time.Since(start).String()
	return r
}

func openDatabase(cfg oceanbase.Config) (*sql.DB, error) {
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(c)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	return db, nil
}

func detectMode(parent context.Context, cfg oceanbase.Config, timeout time.Duration) (mode oceanbase.Mode, err error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	db, err := openDatabase(cfg)
	if err != nil {
		return mode, err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	c, err := db.Conn(ctx)
	if err != nil {
		return mode, err
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	_, err = oceanbase.CurrentCatalog(ctx, c)
	if errors.Is(err, oceanbase.ErrUnsupported) {
		return oceanbase.ModeOracle, nil
	}
	return oceanbase.ModeMySQL, err
}

func modeName(mode oceanbase.Mode) string {
	if mode == oceanbase.ModeOracle {
		return "oracle"
	}
	return "mysql"
}

func (s *scenario) create(ctx context.Context, kind, suffix, definition string) (string, error) {
	name := s.prefix + "_" + suffix
	record := &objectRecord{Kind: kind, Name: name, State: "create_unconfirmed"}
	s.objects = append(s.objects, record)
	if _, err := s.db.ExecContext(ctx, "CREATE "+kind+" "+name+" "+definition); err != nil {
		return "", err
	}
	record.State = "created"
	s.cleanups = append(s.cleanups, cleanupAction{"drop " + kind + " " + name, func(ctx context.Context) error {
		_, err := s.db.ExecContext(ctx, "DROP "+kind+" "+name)
		if err != nil {
			record.State = cleanupFailed
		} else {
			record.State = "dropped"
		}
		return err
	}})
	return name, nil
}

func (s *scenario) table(ctx context.Context, definition string) (string, error) {
	if s.mode == oceanbase.ModeMySQL {
		definition += " ENGINE=InnoDB"
	}
	return s.create(ctx, "TABLE", "T", definition)
}

func (s *scenario) scalar(query string) string {
	if s.mode == oceanbase.ModeOracle {
		return query + dualSuffix
	}
	return query
}

func expect(ok bool, message string) error {
	if !ok {
		return checkFailure(message)
	}
	return nil
}

func affected(result sql.Result, err error, want int64) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	return expect(n == want, "unexpected affected row count")
}

func countRows(ctx context.Context, db *sql.DB, table string, want int) error {
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		return err
	}
	return expect(n == want, "persisted row count differs from expected transaction outcome")
}

func exactPattern(name string) string {
	return strings.NewReplacer("/", "//", "_", "/_", "%", "/%").Replace(name)
}

func (s *scenario) observe(key, value string) {
	if s.observations == nil {
		s.observations = make(map[string]string)
	}
	s.observations[key] = value
}

func strategyName(strategy oceanbase.BatchStrategy) string {
	names := []string{"auto", "sequential", "multi-send", "rewrite", "bulk", "array-binding"}
	if int(strategy) >= len(names) {
		return "unknown"
	}
	return names[strategy]
}
