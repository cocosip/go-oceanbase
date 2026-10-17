package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
)

const (
	charsetUTF8         = "utf8"
	charsetUTF8MB4      = "utf8mb4"
	charsetBinary       = "binary"
	fieldNCharset       = "Session.NCharset"
	charsetUTF8MB3Alias = "utf8mb3"
)

func cloneSessionOptions(options SessionOptions) (SessionOptions, error) {
	if options.Charset == "" {
		options.Charset = charsetUTF8MB4
	}
	if options.Location == nil {
		options.Location = time.UTC
	}
	if strings.ContainsRune(options.NCharset, 0) {
		return SessionOptions{}, &ConfigError{Field: fieldNCharset}
	}
	options.Charset = strings.ToLower(options.Charset)
	if options.Collation != "" && (len(options.Collation) > 128 || !sessionVariableName(options.Collation)) {
		return SessionOptions{}, &ConfigError{Field: "Session.Collation"}
	}
	options.Collation = strings.ToLower(options.Collation)
	options.NCharset = strings.ToLower(options.NCharset)
	if options.NCharset != "" {
		if _, err := charset.Encode(options.NCharset, ""); err != nil || options.NCharset == charsetBinary {
			return SessionOptions{}, &ConfigError{Field: fieldNCharset}
		}
	}
	if _, err := sessionTimeZone(options.Location); err != nil {
		return SessionOptions{}, err
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
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	return c.initializeLocked(ctx)
}

func (c *conn) initializeLocked(ctx context.Context) error {
	charset := strings.ToLower(c.cfg.Session.Charset)
	if charset == charsetUTF8MB3Alias {
		charset = charsetUTF8
	}
	names := "SET NAMES " + charset
	if collation := c.cfg.Session.Collation; collation != "" {
		if c.session.mode != ModeMySQL {
			return fmt.Errorf("%w: connection collation requires MySQL compatibility mode", ErrUnsupported)
		}
		names += " COLLATE " + collation
	}
	if err := c.initSQL(ctx, names); err != nil {
		return err
	}
	if err := c.initSQL(ctx, "SET autocommit=1"); err != nil {
		return err
	}
	if err := c.readOceanBaseVersion(ctx); err != nil {
		return err
	}
	zone, err := sessionTimeZone(c.cfg.Session.Location)
	if err != nil {
		return err
	}
	if err = c.initSQL(ctx, "SET SESSION time_zone = "+c.quoteSessionValue(zone)); err != nil {
		return err
	}
	if name := c.cfg.Session.NCharset; name != "" {
		if c.session.mode != ModeOracle {
			return &ConfigError{Field: fieldNCharset}
		}
		if err := c.initSQL(ctx, "SET NCHARACTER_SET_CONNECTION = "+c.quoteSessionValue(name)); err != nil {
			return err
		}
	}
	variables := make([]string, 0, len(c.cfg.Session.Variables))
	for name := range c.cfg.Session.Variables {
		variables = append(variables, name)
	}
	slices.Sort(variables)
	for _, name := range variables {
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
	if len(variables) != 0 || len(c.cfg.Session.InitSQL) != 0 {
		// Decoding must use the same encoding and zone as the published session.
		for _, query := range []string{names, "SET SESSION time_zone = " + c.quoteSessionValue(zone)} {
			if err := c.initSQL(ctx, query); err != nil {
				return err
			}
		}
		if name := c.cfg.Session.NCharset; name != "" {
			if err := c.initSQL(ctx, "SET NCHARACTER_SET_CONNECTION = "+c.quoteSessionValue(name)); err != nil {
				return err
			}
		}
	}
	if c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0 {
		return &ConfigError{Field: "Session.InitSQL transaction state"}
	}
	return nil
}

func sessionTimeZone(loc *time.Location) (string, error) {
	if loc == nil {
		loc = time.UTC
	}
	t := time.Unix(0, 0).In(loc)
	start, end := t.ZoneBounds()
	if start.IsZero() && end.IsZero() {
		_, offset := t.Zone()
		if offset%60 != 0 || offset < -12*3600 || offset > 14*3600 {
			return "", &ConfigError{Field: "Session.Location"}
		}
		sign := "+"
		if offset < 0 {
			sign = "-"
			offset = -offset
		}
		return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60), nil
	}
	if loc == time.Local || !zoneToken(loc.String(), 128) {
		return "", &ConfigError{Field: "Session.Location"}
	}
	return loc.String(), nil
}

func (c *conn) readOceanBaseVersion(ctx context.Context) error {
	const responseField = "server version response"
	query := "SELECT ob_version()"
	if c.session.mode == ModeOracle {
		query += " FROM dual"
	}
	encoded, err := c.encodeSQL(query)
	if err != nil {
		return err
	}
	rows, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, encoded...), false, nil, true)
	if err != nil {
		return err
	}
	if len(rows.columns) != 1 {
		return rows.fail(&ConfigError{Field: responseField}, true)
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil {
		return rows.fail(err, true)
	}
	version, ok := values[0].(string)
	if !ok || len(version) == 0 || len(version) > 256 {
		return rows.fail(&ConfigError{Field: responseField}, true)
	}
	if err = rows.Next(values); err != io.EOF {
		if err == nil {
			err = &ConfigError{Field: responseField}
		}
		return rows.fail(err, true)
	}
	if rows.more {
		return rows.fail(&ConfigError{Field: responseField}, true)
	}
	c.session.obVersion = version
	return nil
}

func (c *conn) initSQL(ctx context.Context, query string) error {
	encoded, err := c.encodeSQL(query)
	if err != nil {
		return err
	}
	rows, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, encoded...), false, nil, true)
	if err != nil {
		return err
	}
	return rows.drainAll(ctx)
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
