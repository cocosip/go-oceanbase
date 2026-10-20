package control

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Summary is the most recent ordinary SQL command's completed warning count.
type Summary struct {
	Count uint16
	Known bool
}

// Warning contains one decoded diagnostic-area entry.
type Warning struct {
	Level   string
	Code    uint32
	Message string
}

// Manager owns control state while the caller owns the session operation.
type Manager struct {
	activeTx        *Transaction
	warnings        Summary
	warningsCleared bool
}

// New creates the initial control state after a physical connection is initialized.
func New() *Manager { return &Manager{warningsCleared: true} }

// BeginSQL starts a new ordinary-command warning snapshot.
func (m *Manager) BeginSQL() { m.warnings = Summary{}; m.warningsCleared = false }

// ObserveOK records only completed ordinary results; helpers do not call it.
func (m *Manager) ObserveOK(ok wire.OK) {
	m.warnings = Summary{Count: ok.Warnings, Known: true}
	m.warningsCleared = false
}

// Warnings returns the current warning snapshot without server I/O.
func (m *Manager) Warnings() Summary { return m.warnings }

// ClearWarnings suppresses server details until the next ordinary SQL command.
func (m *Manager) ClearWarnings() { m.warnings = Summary{Known: true}; m.warningsCleared = true }

// ResetWarnings drops the warning snapshot after reset or identity changes.
func (m *Manager) ResetWarnings() { m.warnings = Summary{}; m.warningsCleared = true }

// ReadWarnings consumes the bounded MySQL diagnostic area without changing the snapshot.
func (m *Manager) ReadWarnings(ctx context.Context, op *session.Operation) ([]Warning, error) {
	if op.Protocol().Mode != config.ModeMySQL {
		return nil, fmt.Errorf("%w: Oracle compatibility mode supplies warning counts only", dberrors.ErrUnsupported)
	}
	if m.warningsCleared {
		return nil, nil
	}
	r, err := command.Start(ctx, op, []byte("\x03SHOW WARNINGS"), false)
	if err != nil {
		return nil, err
	}
	for {
		event, err := r.Advance(ctx)
		if err != nil {
			return nil, err
		}
		if event.Kind == command.RowResult || !r.More() {
			break
		}
	}
	fail := func(cause error) ([]Warning, error) { op.Discard(); op.EndResponse(); return nil, cause }
	if len(r.Columns()) != 3 || r.Items() != 1 {
		return fail(wire.ErrMalformed)
	}
	var out []Warning
	var size int64
	for {
		values, err := r.NextValues(ctx)
		if err == io.EOF {
			if r.More() {
				return fail(wire.ErrMalformed)
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		w, err := decodeWarning(values)
		if err != nil {
			return fail(err)
		}
		n := int64(len(w.Level)) + int64(len(w.Message)) + 4
		cfg := op.Config()
		if len(out) >= cfg.Limits.MaxResultItems || n > cfg.Limits.MaxMessageBytes-size {
			return fail(wire.ErrMessageTooLarge)
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
