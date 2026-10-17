package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

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

func (c *conn) readWarningsLocked(ctx context.Context) ([]Warning, error) {
	if c.session.mode != ModeMySQL {
		return nil, fmt.Errorf("%w: Oracle compatibility mode supplies warning counts only", ErrUnsupported)
	}
	if c.warningsCleared {
		return nil, nil
	}
	r, err := c.queryCommandOwnedLocked(ctx, []byte("\x03SHOW WARNINGS"), false, nil, true)
	if err != nil {
		return nil, err
	}
	if len(r.columns) != 3 || r.items != 1 {
		c.discard() // An unexpected OK may already have released the internal Rows.
		return nil, r.fail(wire.ErrMalformed, true)
	}
	var out []Warning
	var size int64
	values := make([]driver.Value, 3)
	for {
		err = r.Next(values)
		if err == io.EOF {
			if r.more {
				return nil, r.fail(wire.ErrMalformed, true)
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		w, err := decodeWarning(values)
		if err != nil {
			return nil, r.fail(err, true)
		}
		n := int64(len(w.Level)) + int64(len(w.Message)) + 4
		if len(out) >= c.cfg.Limits.MaxResultItems || n > c.cfg.Limits.MaxMessageBytes-size {
			return nil, r.fail(wire.ErrMessageTooLarge, true)
		}
		size += n
		out = append(out, w)
	}
}

func decodeWarning(values []driver.Value) (Warning, error) {
	var fields [3]sql.NullString
	for i := range fields {
		if err := fields[i].Scan(values[i]); err != nil || !fields[i].Valid {
			return Warning{}, wire.ErrMalformed
		}
	}
	code, err := strconv.ParseUint(fields[1].String, 10, 32)
	if err != nil || fields[0].String == "" {
		return Warning{}, wire.ErrMalformed
	}
	return Warning{Level: fields[0].String, Code: uint32(code), Message: fields[2].String}, nil
}
