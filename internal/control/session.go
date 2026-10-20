package control

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// SessionKind identifies a validated session helper command.
type SessionKind uint8

// Session helper operations retain the public facade's original ordering.
const (
	Schema SessionKind = iota
	Catalog
	SetSchema
	SetCatalog
	ClientInfo
	SetClientInfo
	Variable
	SetVariable
)

// SessionRequest contains scalar inputs independent of the public root contracts.
type SessionRequest struct {
	Kind        SessionKind
	Name, Value string
	Info        [3]string
}

// Session performs one validated helper without changing ordinary warning snapshots.
func (m *Manager) Session(ctx context.Context, op *session.Operation, cache *statement.Cache, resolver *catalog.Resolver, r SessionRequest, xaUsed bool) ([]sql.NullString, error) {
	query, columns, mutate, err := SessionSQL(op, r)
	if err != nil {
		return nil, err
	}
	if mutate && (xaUsed || m.activeTx != nil || op.Protocol().OK.Status&1 != 0 || op.Protocol().OK.Status&2 == 0) {
		return nil, fmt.Errorf("%w: session mutation requires autocommit without a transaction", dberrors.ErrUnsupported)
	}
	if r.Kind == SetVariable {
		literal, err := SystemVariableLiteral(ctx, op, r.Name, r.Value)
		if err != nil {
			return nil, err
		}
		query = "SET SESSION " + r.Name + " = " + literal
	}
	encoded, err := command.EncodeSQL(query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, err
	}
	if mutate {
		resolver.Clear()
		if err = cache.Clear(ctx, op); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		// A complete server error can still accompany a partial state change.
		op.MarkDirty()
		kind := byte(3)
		if op.Protocol().Mode == config.ModeMySQL && (r.Kind == SetSchema || r.Kind == SetCatalog) {
			kind = 2
		}
		_, err = command.ExecOK(ctx, op, append([]byte{kind}, encoded...))
		return nil, err
	}
	values, _, err := command.QueryScalars(ctx, op, encoded, columns)
	return values, err
}

// SessionSQL validates a helper and constructs SQL without performing I/O.
func SessionSQL(op *session.Operation, r SessionRequest) (query string, columns int, mutate bool, err error) {
	p := op.Protocol()
	if p.Mode == config.ModeOracle && (r.Kind == Catalog || r.Kind == SetCatalog) {
		return "", 0, false, fmt.Errorf("%w: Oracle compatibility mode has no catalog", dberrors.ErrUnsupported)
	}
	quote := func(value string) string { return valuecodec.QuoteSessionValue(p.Mode, p.OK.Status, value) }
	switch r.Kind {
	case Schema, Catalog:
		query, columns = "SELECT DATABASE()", 1
		if p.Mode == config.ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')"
		}
	case SetSchema, SetCatalog:
		if !valueformat.Identifier(r.Name) {
			return "", 0, false, fmt.Errorf("%w: invalid schema name", dberrors.ErrBind)
		}
		query, mutate = r.Name, true
		if p.Mode == config.ModeOracle {
			query = "ALTER SESSION SET CURRENT_SCHEMA = " + sqltext.QuoteIdentifier(p.Mode, r.Name)
		}
	case ClientInfo:
		query, columns = "SELECT @ApplicationName, @ClientUser, @ClientHostname", 3
	case SetClientInfo:
		values := r.Info
		for i, value := range values {
			values[i] = "NULL"
			if value != "" {
				values[i] = quote(value)
			}
		}
		query, mutate = "SET @ApplicationName = "+values[0]+", @ClientUser = "+values[1]+", @ClientHostname = "+values[2], true
	case Variable, SetVariable:
		if !configspec.SessionVariableName(r.Name) {
			return "", 0, false, fmt.Errorf("%w: invalid session variable name", dberrors.ErrBind)
		}
		query, columns = "SELECT @@SESSION."+r.Name, 1
		if r.Kind == SetVariable {
			name := strings.ToLower(r.Name)
			if name == "time_zone" || name == "ncharacter_set_connection" || strings.HasPrefix(name, "character_set_") || strings.HasPrefix(name, "collation_") {
				return "", 0, false, fmt.Errorf("%w: encoding and time zone are configured by Session", dberrors.ErrUnsupported)
			}
			query, columns, mutate = "SET SESSION "+r.Name+" = "+quote(r.Value), 0, true
		}
	default:
		return "", 0, false, dberrors.ErrUnsupported
	}
	if !mutate && p.Mode == config.ModeOracle {
		query += " FROM dual"
	}
	return query, columns, mutate, nil
}

// SystemVariableLiteral probes only integer candidates and shares the initializer's literal rules.
func SystemVariableLiteral(ctx context.Context, op *session.Operation, name, value string) (string, error) {
	p := op.Protocol()
	if _, integer := valuecodec.SessionInteger(value); integer {
		query := "SELECT @@SESSION." + name
		if p.Mode == config.ModeOracle {
			query += " FROM dual"
		}
		_, columns, err := command.QueryScalars(ctx, op, []byte(query), 1)
		if err != nil {
			return "", err
		}
		return valuecodec.SessionVariableLiteral(p.Mode, p.OK.Status, value, columns[0].Type), nil
	}
	return valuecodec.QuoteSessionValue(p.Mode, p.OK.Status, value), nil
}
