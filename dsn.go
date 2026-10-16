package oceanbase

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ParseDSN parses an oceanbase URL and applies the same defaults as NewConnector.
func ParseDSN(dsn string) (Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "oceanbase" || u.Opaque != "" || u.User == nil || u.Fragment != "" || strings.Contains(dsn, "#") {
		return Config{}, &ConfigError{Field: "DSN"}
	}
	if strings.Contains(strings.TrimPrefix(u.EscapedPath(), "/"), "/") {
		return Config{}, &ConfigError{Field: "Database"}
	}
	cfg := Config{Addr: u.Host, User: u.User.Username(), Database: strings.TrimPrefix(u.Path, "/")}
	cfg.Password, _ = u.User.Password()
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Config{}, &ConfigError{Field: "DSN options"}
	}
	for name, list := range values {
		if len(list) != 1 || list[0] == "" {
			return Config{}, &ConfigError{Field: "DSN options"}
		}
		if err := applyDSNOption(&cfg, name, list[0]); err != nil {
			return Config{}, err
		}
	}
	return normalizeConfig(cfg)
}

func applyDSNOption(cfg *Config, name, value string) error {
	var err error
	switch name {
	case "mode":
		switch value {
		case "auto":
			cfg.Mode = ModeAuto
		case "mysql":
			cfg.Mode = ModeMySQL
		case "oracle":
			cfg.Mode = ModeOracle
		default:
			err = &ConfigError{Field: "mode"}
		}
	case "connectTimeout":
		cfg.ConnectTimeout, err = time.ParseDuration(value)
	case "cleanupTimeout":
		cfg.Limits.CleanupTimeout, err = time.ParseDuration(value)
	case "maxMessageBytes":
		cfg.Limits.MaxMessageBytes, err = strconv.ParseInt(value, 10, 64)
	case "maxLOBBytes":
		cfg.Limits.MaxLOBBytes, err = strconv.ParseInt(value, 10, 64)
	case "fetchSize":
		cfg.Limits.FetchSize, err = strconv.Atoi(value)
	case "maxResultItems":
		cfg.Limits.MaxResultItems, err = strconv.Atoi(value)
	case "charset":
		cfg.Session.Charset = value
	case "ncharset":
		cfg.Session.NCharset = value
	case "location":
		cfg.Session.Location, err = time.LoadLocation(value)
	case "rowsAffectedMode":
		switch value {
		case "matched":
			cfg.RowsAffectedMode = MatchedRows
		case "changed":
			cfg.RowsAffectedMode = ChangedRows
		default:
			err = &ConfigError{Field: "rowsAffectedMode"}
		}
	case "prepareMode":
		switch value {
		case "server":
			cfg.PrepareMode = PrepareServer
		case "client":
			cfg.PrepareMode = PrepareClient
		default:
			err = &ConfigError{Field: "prepareMode"}
		}
	case "resetMode":
		switch value {
		case "managed":
			cfg.ResetMode = ResetManaged
		case "full":
			cfg.ResetMode = ResetFull
		default:
			err = &ConfigError{Field: "resetMode"}
		}
	case "allowMultiStatements":
		switch value {
		case "true":
			cfg.AllowMultiStatements = true
		case "false":
			cfg.AllowMultiStatements = false
		default:
			err = &ConfigError{Field: "allowMultiStatements"}
		}
	default:
		return &ConfigError{Field: "DSN option"}
	}
	if err != nil {
		return &ConfigError{Field: name}
	}
	return nil
}
