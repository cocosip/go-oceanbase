package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
)

// SavepointKind selects a transaction savepoint operation.
type SavepointKind uint8

// Savepoint operations retain the public request ordering.
const (
	CreateSavepoint SavepointKind = iota
	RollbackSavepoint
	ReleaseSavepoint
)

// SavepointRequest carries one exact savepoint identifier.
type SavepointRequest struct {
	Kind SavepointKind
	Name string
}

// SavepointSQL preserves mode-specific identifier and release restrictions.
func SavepointSQL(mode config.Mode, r SavepointRequest) (string, error) {
	name := r.Name
	if !sqltext.ValidSavepointName(name) {
		return "", fmt.Errorf("%w: savepoint identifier", dberrors.ErrBind)
	}
	quote := "`"
	if mode == config.ModeOracle {
		if r.Kind == ReleaseSavepoint {
			return "", fmt.Errorf("%w: Oracle RELEASE SAVEPOINT", dberrors.ErrUnsupported)
		}
		if len(name) > 128 {
			return "", fmt.Errorf("%w: savepoint identifier length", dberrors.ErrBind)
		}
		quote = "\""
	} else if utf8.RuneCountInString(name) > 64 {
		return "", fmt.Errorf("%w: savepoint identifier length", dberrors.ErrBind)
	}
	query := ""
	switch r.Kind {
	case CreateSavepoint:
		query = "SAVEPOINT "
	case RollbackSavepoint:
		query = "ROLLBACK TO SAVEPOINT "
	case ReleaseSavepoint:
		query = "RELEASE SAVEPOINT "
	default:
		return "", dberrors.ErrUnsupported
	}
	return query + quote + strings.ReplaceAll(name, quote, quote+quote) + quote, nil
}

// Savepoint executes within the active transaction context.
func (m *Manager) Savepoint(ctx context.Context, op *session.Operation, r SavepointRequest) error {
	query, err := SavepointSQL(op.Protocol().Mode, r)
	if err != nil {
		return err
	}
	encoded, err := command.EncodeSQL(query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
	if err != nil {
		return err
	}
	if m.activeTx == nil {
		return fmt.Errorf("oceanbase: savepoint requires active transaction")
	}
	txctx := m.activeTx.ctx
	opctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(txctx, func() { cancel(context.Cause(txctx)) })
	defer func() { stop(); cancel(nil) }()
	if txctx.Err() != nil {
		cancel(context.Cause(txctx))
	}
	if _, err := command.ExecOK(opctx, op, append([]byte{3}, encoded...)); err != nil {
		return errors.Join(err, context.Cause(opctx))
	}
	return nil
}
