package oceanbase

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
)

func cloneSessionOptions(options SessionOptions) (SessionOptions, error) {
	if options.Charset == "" {
		options.Charset = "utf8mb4"
	}
	if options.Location == nil {
		options.Location = time.UTC
	}
	if strings.ContainsRune(options.NCharset, 0) {
		return SessionOptions{}, &ConfigError{Field: "Session.NCharset"}
	}
	options.Variables = maps.Clone(options.Variables)
	options.InitSQL = slices.Clone(options.InitSQL)
	for name, value := range options.Variables {
		if !sessionVariableName(name) || strings.ContainsRune(value, 0) {
			return SessionOptions{}, &ConfigError{Field: "Session.Variables"}
		}
	}
	for _, query := range options.InitSQL {
		if strings.TrimSpace(query) == "" || strings.ContainsRune(query, 0) {
			return SessionOptions{}, &ConfigError{Field: "Session.InitSQL"}
		}
	}
	return options, nil
}

func (c *conn) initialize(ctx context.Context) error {
	charset := strings.ToLower(c.cfg.Session.Charset)
	if charset == "utf8mb3" {
		charset = "utf8"
	}
	if err := c.initSQL(ctx, "SET NAMES "+charset); err != nil {
		return err
	}
	if err := c.initSQL(ctx, "SET autocommit=1"); err != nil {
		return err
	}
	if name := c.cfg.Session.NCharset; name != "" {
		if c.session.mode != ModeOracle {
			return &ConfigError{Field: "Session.NCharset"}
		}
		if err := c.initSQL(ctx, "SET NCHARACTER_SET_CONNECTION = "+c.quoteSessionValue(name)); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(c.cfg.Session.Variables))
	for name := range c.cfg.Session.Variables {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if !sessionVariableName(name) {
			return &ConfigError{Field: "Session.Variables"}
		}
		query := "SET SESSION " + name + " = " + c.quoteSessionValue(c.cfg.Session.Variables[name])
		if err := c.initSQL(ctx, query); err != nil {
			return err
		}
	}
	for _, query := range c.cfg.Session.InitSQL {
		if err := c.initSQL(ctx, query); err != nil {
			return err
		}
	}
	if c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0 {
		return &ConfigError{Field: "Session.InitSQL transaction state"}
	}
	return nil
}

func (c *conn) initSQL(ctx context.Context, query string) error {
	return c.commandOK(ctx, append([]byte{3}, query...))
}

func sessionVariableName(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range []byte(name) {
		if ch == '_' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (i > 0 && ch >= '0' && ch <= '9') {
			continue
		}
		return false
	}
	return true
}

func (c *conn) quoteSessionValue(value string) string {
	if c.session.mode == ModeMySQL && c.session.ok.Status&512 == 0 {
		value = strings.ReplaceAll(value, "\\", "\\\\")
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
