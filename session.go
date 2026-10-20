package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/control"
	"github.com/cocosip/go-oceanbase/internal/session"
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

// Warning is one MySQL compatibility-mode SHOW WARNINGS row.
// Message can contain SQL or sensitive values. SQLSTATE is not supplied by this command.
type Warning struct {
	Level   string
	Code    uint32
	Message string
}

// ReadWarnings reads the current server diagnostic area on a pinned MySQL-mode connection.
// Read it immediately after consuming Rows: intervening server commands (including helpers
// and internal SQL) may replace the area independently of the Warnings count snapshot.
// The server may retain fewer details than its warning count. Oracle returns ErrUnsupported.
// MaxResultItems bounds the row count and MaxMessageBytes bounds the decoded content;
// exceeding either returns an error without partial details and discards the session.
func ReadWarnings(ctx context.Context, pinned *sql.Conn) ([]Warning, error) {
	r := &sessionOperationRequest{op: sessionWarningDetails}
	err := runSessionOperation(ctx, pinned, r)
	return r.details, err
}

// ClearWarnings clears the local count and suppresses ReadWarnings until the next ordinary
// SQL command. It sends no SQL and does not clear the server's own diagnostic area.
func ClearWarnings(ctx context.Context, pinned *sql.Conn) error {
	return runSessionOperation(ctx, pinned, &sessionOperationRequest{op: sessionClearWarnings})
}

type changeUserRequest struct {
	credentials Credentials
	database    string
}

// ChangeUser reauthenticates a pinned connection and reruns Config.Session initialization.
// It requires autocommit without a transaction. Old statements and resource handles expire.
// An empty database selects no MySQL database or the new Oracle user's default schema.
// After sending the change, failure closes the session; success remains usable until
// pinned.Close discards it. The original Connector and its credentials are not changed.
// Credentials.ProxyUser explicitly selects the proxy target; empty clears proxy login.
func ChangeUser(ctx context.Context, pinned *sql.Conn, credentials Credentials, database string) error {
	if pinned == nil {
		return fmt.Errorf("oceanbase: a non-nil *sql.Conn is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := pinned.ExecContext(ctx, "", changeUserRequest{credentials: credentials, database: database})
	return err
}

// CancelTarget identifies the first ordinary SQL command following QueryTarget.
// Copies share one cancellation attempt; completion, reset and identity changes expire it.
type CancelTarget struct{ target session.Target }

// QueryTarget arms a one-shot target before starting SQL on pinned.
// Do not call it while Rows or another operation owns the pinned connection.
func QueryTarget(ctx context.Context, pinned *sql.Conn) (CancelTarget, error) {
	r := &sessionOperationRequest{op: sessionQueryTarget}
	err := runSessionOperation(ctx, pinned, r)
	return r.target, err
}

// CancelQuery sends KILL QUERY through a new, private connection to the original entry.
// configured must come from NewConnector and authenticate as the target user/tenant.
// A verified TLS target requires verified TLS here too. No discovery or failover runs.
// Success acknowledges KILL only; read the original operation's result independently.
// Server-side ID reuse after a remote disconnect cannot be excluded by this protocol.
func CancelQuery(ctx context.Context, configured driver.Connector, target CancelTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := target.target.Info(); !ok {
		return fmt.Errorf("%w: invalid cancel target", ErrResource)
	}
	c, ok := configured.(*connector)
	if !ok || c == nil {
		return fmt.Errorf("%w: CancelQuery requires this driver's Connector", ErrUnsupported)
	}
	return control.CancelQuery(ctx, c.runtime, target.target)
}

func (c *conn) execChangeUser(ctx context.Context, r changeUserRequest) (driver.Result, error) {
	return command.Result{Known: true}, c.engine.ChangeUser(ctx, r.credentials, r.database)
}

func (c *conn) execSessionOperation(ctx context.Context, r *sessionOperationRequest) (driver.Result, error) {
	if r == nil {
		return nil, ErrBind
	}
	var err error
	switch r.op {
	case sessionQueryTarget:
		r.target.target, err = c.engine.QueryTarget(ctx)
	case sessionWarnings:
		var summary control.Summary
		summary, err = c.engine.Warnings(ctx)
		r.warnings = WarningSummary{Count: summary.Count, Known: summary.Known}
	case sessionClearWarnings:
		err = c.engine.ClearWarnings(ctx)
	case sessionWarningDetails:
		var details []control.Warning
		details, err = c.engine.ReadWarnings(ctx)
		if details != nil {
			r.details = make([]Warning, len(details))
			for i, w := range details {
				r.details[i] = Warning{Level: w.Level, Code: w.Code, Message: w.Message}
			}
		}
	default:
		r.values, err = c.engine.Session(ctx, control.SessionRequest{Kind: control.SessionKind(r.op), Name: r.name, Value: r.value, Info: [3]string{r.info.ApplicationName, r.info.ClientUser, r.info.ClientHostname}})
	}
	return command.Result{Known: true}, err
}
