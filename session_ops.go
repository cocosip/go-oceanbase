package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ClientInfo holds the three Connector/J-compatible session attributes.
// SetClientInfo replaces all three; an empty field is sent as SQL NULL.
type ClientInfo struct {
	ApplicationName string
	ClientUser      string
	ClientHostname  string
}

// WarningSummary preserves the server count without requiring a logger.
// Known is false when no complete OK/EOF supplied a count. Details are not included.
type WarningSummary struct {
	Count uint16
	Known bool
}

type sessionOperation uint8

const (
	sessionSchema sessionOperation = iota
	sessionCatalog
	sessionSetSchema
	sessionSetCatalog
	sessionClientInfo
	sessionSetClientInfo
	sessionVariable
	sessionSetVariable
	sessionWarnings
	sessionWarningDetails
	sessionClearWarnings
	sessionQueryTarget
)

type sessionOperationRequest struct {
	op       sessionOperation
	name     string
	value    string
	info     ClientInfo
	values   []sql.NullString
	warnings WarningSummary
	details  []Warning
	target   CancelTarget
}

// CurrentSchema reads the Oracle current schema or MySQL current database.
// An empty string represents no selected MySQL database.
func CurrentSchema(ctx context.Context, pinned *sql.Conn) (string, error) {
	r := &sessionOperationRequest{op: sessionSchema}
	err := runSessionOperation(ctx, pinned, r)
	return sessionOperationText(r), err
}

// CurrentCatalog reads the current MySQL database; Oracle has no catalog equivalent.
func CurrentCatalog(ctx context.Context, pinned *sql.Conn) (string, error) {
	r := &sessionOperationRequest{op: sessionCatalog}
	err := runSessionOperation(ctx, pinned, r)
	return sessionOperationText(r), err
}

func sessionOperationText(r *sessionOperationRequest) string {
	if len(r.values) == 0 {
		return ""
	}
	return r.values[0].String
}

// SetSchema selects an exact schema name (MySQL: database), without normalizing case.
// Mutations require an idle session; closing pinned discards its physical connection.
func SetSchema(ctx context.Context, pinned *sql.Conn, name string) error {
	return runSessionOperation(ctx, pinned, &sessionOperationRequest{op: sessionSetSchema, name: name})
}

// SetCatalog selects a MySQL database. Oracle returns ErrUnsupported.
func SetCatalog(ctx context.Context, pinned *sql.Conn, name string) error {
	return runSessionOperation(ctx, pinned, &sessionOperationRequest{op: sessionSetCatalog, name: name})
}

// GetClientInfo reads the three session attributes; SQL NULL becomes an empty field.
func GetClientInfo(ctx context.Context, pinned *sql.Conn) (ClientInfo, error) {
	r := &sessionOperationRequest{op: sessionClientInfo}
	err := runSessionOperation(ctx, pinned, r)
	if len(r.values) != 3 {
		return ClientInfo{}, err
	}
	return ClientInfo{r.values[0].String, r.values[1].String, r.values[2].String}, err
}

// SetClientInfo replaces the session attributes. Closing pinned discards the session.
func SetClientInfo(ctx context.Context, pinned *sql.Conn, info ClientInfo) error {
	return runSessionOperation(ctx, pinned, &sessionOperationRequest{op: sessionSetClientInfo, info: info})
}

// SessionVariable reads one validated session system variable, preserving SQL NULL.
func SessionVariable(ctx context.Context, pinned *sql.Conn, name string) (sql.NullString, error) {
	r := &sessionOperationRequest{op: sessionVariable, name: name}
	err := runSessionOperation(ctx, pinned, r)
	if len(r.values) == 0 {
		return sql.NullString{}, err
	}
	return r.values[0], err
}

// SetSessionVariable sets a session system variable. Decimal integer strings
// within int64/uint64 range use integer literals only for numeric server variables.
// String variables retain quoted values, including leading zeros.
// Encoding and time-zone variables are reserved by Config.Session. Closing pinned discards it.
func SetSessionVariable(ctx context.Context, pinned *sql.Conn, name, value string) error {
	return runSessionOperation(ctx, pinned, &sessionOperationRequest{op: sessionSetVariable, name: name, value: value})
}

// Warnings returns the last completed result count from the latest ordinary SQL command.
// Helpers and Ping preserve this snapshot; a new SQL command replaces it.
// Consume or close Rows first, and serialize operations on the pinned connection.
func Warnings(ctx context.Context, pinned *sql.Conn) (WarningSummary, error) {
	r := &sessionOperationRequest{op: sessionWarnings}
	err := runSessionOperation(ctx, pinned, r)
	return r.warnings, err
}

func runSessionOperation(ctx context.Context, pinned *sql.Conn, r *sessionOperationRequest) error {
	if pinned == nil {
		return fmt.Errorf("oceanbase: a non-nil *sql.Conn is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := pinned.ExecContext(ctx, "", r)
	return err
}

func (c *conn) execSessionOperation(ctx context.Context, r *sessionOperationRequest) (driver.Result, error) {
	if r == nil {
		return nil, ErrBind
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if c.responseActive {
		return nil, errResponseActive
	}
	switch r.op {
	case sessionQueryTarget:
		r.target = c.queryTargetLocked()
		return result{known: true}, nil
	case sessionWarnings:
		r.warnings = c.lastWarnings
		return result{known: true}, nil
	case sessionClearWarnings:
		c.lastWarnings, c.warningsCleared = WarningSummary{Known: true}, true
		return result{known: true}, nil
	case sessionWarningDetails:
		var err error
		r.details, err = c.readWarningsLocked(ctx)
		return result{known: true}, err
	}
	query, columns, mutate, err := c.sessionOperationSQL(r)
	if err != nil {
		return nil, err
	}
	if mutate && (c.xaUsed || c.activeTx != nil || c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0) {
		return nil, fmt.Errorf("%w: session mutation requires autocommit without a transaction", ErrUnsupported)
	}
	if r.op == sessionSetVariable {
		literal, err := c.systemVariableLiteral(ctx, r.name, r.value)
		if err != nil {
			return nil, err
		}
		query = "SET SESSION " + r.name + " = " + literal
	}
	encoded, err := c.encodeSQL(query)
	if err != nil {
		return nil, err
	}
	if mutate {
		c.clearDescriptors()
		if err = c.clearStatementsLocked(ctx); err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		// A complete server error can still accompany a partial state change.
		c.sessionDirty = true
		command := byte(3)
		if c.session.mode == ModeMySQL && (r.op == sessionSetSchema || r.op == sessionSetCatalog) {
			command = 2 // COM_INIT_DB uses the encoded name without SQL quoting.
		}
		err = c.commandOK(ctx, append([]byte{command}, encoded...))
	} else {
		r.values, _, err = c.sessionValuesLocked(ctx, encoded, columns)
	}
	return result{known: true}, err
}

func (c *conn) sessionOperationSQL(r *sessionOperationRequest) (query string, columns int, mutate bool, err error) {
	if c.session.mode == ModeOracle && (r.op == sessionCatalog || r.op == sessionSetCatalog) {
		return "", 0, false, fmt.Errorf("%w: Oracle compatibility mode has no catalog", ErrUnsupported)
	}
	switch r.op {
	case sessionSchema, sessionCatalog:
		query, columns = "SELECT DATABASE()", 1
		if c.session.mode == ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV', 'CURRENT_SCHEMA')"
		}
	case sessionSetSchema, sessionSetCatalog:
		if !validBlobIdentifier(r.name) {
			return "", 0, false, fmt.Errorf("%w: invalid schema name", ErrBind)
		}
		query, mutate = r.name, true
		if c.session.mode == ModeOracle {
			query = "ALTER SESSION SET CURRENT_SCHEMA = " + quoteBlobIdentifier(c.session.mode, r.name)
		}
	case sessionClientInfo:
		query, columns = "SELECT @ApplicationName, @ClientUser, @ClientHostname", 3
	case sessionSetClientInfo:
		values := []string{r.info.ApplicationName, r.info.ClientUser, r.info.ClientHostname}
		for i, value := range values {
			values[i] = "NULL"
			if value != "" {
				values[i] = c.quoteSessionValue(value)
			}
		}
		query, mutate = "SET @ApplicationName = "+values[0]+", @ClientUser = "+values[1]+", @ClientHostname = "+values[2], true
	case sessionVariable, sessionSetVariable:
		if !sessionVariableName(r.name) {
			return "", 0, false, fmt.Errorf("%w: invalid session variable name", ErrBind)
		}
		query, columns = "SELECT @@SESSION."+r.name, 1
		if r.op == sessionSetVariable {
			name := strings.ToLower(r.name)
			if name == "time_zone" || name == "ncharacter_set_connection" || strings.HasPrefix(name, "character_set_") || strings.HasPrefix(name, "collation_") {
				return "", 0, false, fmt.Errorf("%w: encoding and time zone are configured by Session", ErrUnsupported)
			}
			query, columns, mutate = "SET SESSION "+r.name+" = "+c.quoteSessionValue(r.value), 0, true
		}
	default:
		return "", 0, false, ErrUnsupported
	}
	if !mutate && c.session.mode == ModeOracle {
		query += sessionReadDualSuffix
	}
	return query, columns, mutate, nil
}

func (c *conn) sessionValuesLocked(ctx context.Context, query []byte, columns int) ([]sql.NullString, []wire.Column, error) {
	rows, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, query...), false, nil, true)
	if err != nil {
		return nil, nil, err
	}
	if len(rows.columns) != columns || rows.items != 1 {
		c.discard()
		return nil, nil, rows.fail(wire.ErrMalformed, true)
	}
	descriptions := rows.columns
	values := make([]driver.Value, columns)
	if err = rows.Next(values); err != nil {
		if err != io.EOF {
			return nil, nil, err
		}
		c.discard()
		return nil, nil, rows.fail(wire.ErrMalformed, true)
	}
	err = rows.Next(make([]driver.Value, columns))
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	if err != io.EOF || rows.more {
		c.discard()
		return nil, nil, rows.fail(wire.ErrMalformed, true)
	}
	out := make([]sql.NullString, columns)
	for i, value := range values {
		if err = out[i].Scan(value); err != nil {
			return nil, nil, err
		}
	}
	return out, descriptions, nil
}
