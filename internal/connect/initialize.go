package connect

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
)

const sessionReadDualSuffix = " FROM dual"

// Initialize installs the configured baseline while the caller owns the operation.
func Initialize(ctx context.Context, op *session.Operation, cfg config.Config) error {
	b := baseline{cfg: cfg, op: op}
	return b.initialize(ctx)
}

type baseline struct {
	cfg config.Config
	op  *session.Operation
}

func (b baseline) initialize(ctx context.Context) error {
	if b.cfg.CreateDatabaseIfMissing && b.op.Protocol().Mode != config.ModeMySQL {
		return fmt.Errorf("%w: database creation requires MySQL compatibility mode", dberrors.ErrUnsupported)
	}
	charset := strings.ToLower(b.cfg.Session.Charset)
	if charset == "utf8mb3" {
		charset = "utf8"
	}
	names := "SET NAMES " + charset
	if collation := b.cfg.Session.Collation; collation != "" {
		if b.op.Protocol().Mode != config.ModeMySQL {
			return fmt.Errorf("%w: connection collation requires MySQL compatibility mode", dberrors.ErrUnsupported)
		}
		names += " COLLATE " + collation
	}
	if err := b.initSQL(ctx, names); err != nil {
		return err
	}
	if err := b.initSQL(ctx, "SET autocommit=1"); err != nil {
		return err
	}
	if b.cfg.Session.SkipVersionProbe {
		b.op.SetVersion("")
	} else if err := b.readOceanBaseVersion(ctx); err != nil {
		return err
	}
	zone, ok := configspec.SessionTimeZone(b.cfg.Session.Location)
	if !ok {
		return &config.ConfigError{Field: "Session.Location"}
	}
	if err := b.initSQL(ctx, "SET SESSION time_zone = "+b.quote(zone)); err != nil {
		return err
	}
	if name := b.cfg.Session.NCharset; name != "" {
		if b.op.Protocol().Mode != config.ModeOracle {
			return &config.ConfigError{Field: "Session.NCharset"}
		}
		if err := b.initSQL(ctx, "SET NCHARACTER_SET_CONNECTION = "+b.quote(name)); err != nil {
			return err
		}
	}
	if err := b.createInitialDatabase(ctx); err != nil {
		return err
	}
	variables := slices.Sorted(maps.Keys(b.cfg.Session.Variables))
	for _, name := range variables {
		if !configspec.SessionVariableName(name) {
			return &config.ConfigError{Field: "Session.Variables"}
		}
		literal, err := b.systemVariableLiteral(ctx, name, b.cfg.Session.Variables[name])
		if err != nil {
			return err
		}
		if err := b.initSQL(ctx, "SET SESSION "+name+" = "+literal); err != nil {
			return err
		}
	}
	for _, query := range b.cfg.Session.InitSQL {
		if err := b.initSQL(ctx, query); err != nil {
			return err
		}
	}
	if len(variables) != 0 || len(b.cfg.Session.InitSQL) != 0 {
		for _, query := range []string{names, "SET SESSION time_zone = " + b.quote(zone)} {
			if err := b.initSQL(ctx, query); err != nil {
				return err
			}
		}
		if name := b.cfg.Session.NCharset; name != "" {
			if err := b.initSQL(ctx, "SET NCHARACTER_SET_CONNECTION = "+b.quote(name)); err != nil {
				return err
			}
		}
	}
	if b.op.Protocol().OK.Status&1 != 0 || b.op.Protocol().OK.Status&2 == 0 {
		return &config.ConfigError{Field: "Session.InitSQL transaction state"}
	}
	return nil
}

func (b baseline) encodeSQL(query string) ([]byte, error) {
	return command.EncodeSQL(query, b.cfg.Session.Charset, b.cfg.Limits.MaxMessageBytes-1)
}

func (b baseline) initSQL(ctx context.Context, query string) error {
	encoded, err := b.encodeSQL(query)
	if err != nil {
		return err
	}
	return command.InitSQL(ctx, b.op, encoded)
}

func (b baseline) quote(value string) string {
	p := b.op.Protocol()
	return valuecodec.QuoteSessionValue(p.Mode, p.OK.Status, value)
}

func (b baseline) systemVariableLiteral(ctx context.Context, name, value string) (string, error) {
	p := b.op.Protocol()
	if _, integer := valuecodec.SessionInteger(value); integer {
		query := "SELECT @@SESSION." + name
		if p.Mode == config.ModeOracle {
			query += sessionReadDualSuffix
		}
		_, columns, err := command.QueryScalars(ctx, b.op, []byte(query), 1)
		if err != nil {
			return "", err
		}
		return valuecodec.SessionVariableLiteral(p.Mode, p.OK.Status, value, columns[0].Type), nil
	}
	return b.quote(value), nil
}

func (b baseline) createInitialDatabase(ctx context.Context) error {
	if !b.cfg.CreateDatabaseIfMissing {
		return nil
	}
	database, err := b.encodeSQL(b.cfg.Database)
	if err != nil {
		return err
	}
	identifier := sqltext.QuoteIdentifier(config.ModeMySQL, b.cfg.Database)
	if err = b.initSQL(ctx, "CREATE DATABASE IF NOT EXISTS "+identifier); err != nil {
		return err
	}
	_, err = command.ExecOK(ctx, b.op, append([]byte{2}, database...))
	return err
}

func (b baseline) readOceanBaseVersion(ctx context.Context) error {
	query := "SELECT ob_version()"
	if b.op.Protocol().Mode == config.ModeOracle {
		query += sessionReadDualSuffix
	}
	encoded, err := b.encodeSQL(query)
	if err != nil {
		return err
	}
	r, err := command.Start(ctx, b.op, append([]byte{3}, encoded...), false)
	if err != nil {
		return err
	}
	if _, err = r.Advance(ctx); err != nil {
		return err
	}
	fail := func(cause error) error { b.op.Discard(); b.op.EndResponse(); return cause }
	invalid := &config.ConfigError{Field: "server version response"}
	if len(r.Columns()) != 1 {
		return fail(invalid)
	}
	values, err := r.NextValues(ctx)
	if err != nil {
		return fail(err)
	}
	version, ok := values[0].(string)
	if !ok || len(version) == 0 || len(version) > 256 {
		return fail(invalid)
	}
	if _, err = r.NextValues(ctx); err != io.EOF {
		if err == nil {
			err = invalid
		}
		return fail(err)
	}
	if r.More() {
		return fail(invalid)
	}
	b.op.SetVersion(version)
	return nil
}
