package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type savepointOperation uint8

const (
	savepointCreate savepointOperation = iota
	savepointRollback
	savepointRelease
)

type savepointRequest struct {
	operation savepointOperation
	name      string
}

// Savepoint creates a named savepoint on the transaction's physical session.
func Savepoint(ctx context.Context, tx *sql.Tx, name string) error {
	return doSavepoint(ctx, tx, name, savepointCreate)
}

// RollbackTo rolls back to a savepoint without ending the transaction.
func RollbackTo(ctx context.Context, tx *sql.Tx, name string) error {
	return doSavepoint(ctx, tx, name, savepointRollback)
}

// ReleaseSavepoint releases a savepoint; Oracle compatibility mode has no RELEASE operation.
func ReleaseSavepoint(ctx context.Context, tx *sql.Tx, name string) error {
	return doSavepoint(ctx, tx, name, savepointRelease)
}
func doSavepoint(ctx context.Context, tx *sql.Tx, name string, operation savepointOperation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil {
		return fmt.Errorf("oceanbase: nil transaction")
	}
	if !validSavepointName(name) {
		return fmt.Errorf("%w: savepoint identifier", ErrBind)
	}
	_, err := tx.ExecContext(ctx, "", savepointRequest{operation: operation, name: name})
	return err
}
func validSavepointName(name string) bool {
	return name != "" && len(name) <= 256 && !strings.ContainsRune(name, 0) && utf8.ValidString(name)
}
func (c *conn) execSavepoint(ctx context.Context, request savepointRequest) (driver.Result, error) {
	name := request.name
	if !validSavepointName(name) {
		return nil, fmt.Errorf("%w: savepoint identifier", ErrBind)
	}
	quote := "`"
	if c.session.mode == ModeOracle {
		if request.operation == savepointRelease {
			return nil, fmt.Errorf("%w: Oracle RELEASE SAVEPOINT", ErrUnsupported)
		}
		if len(name) > 128 {
			return nil, fmt.Errorf("%w: savepoint identifier length", ErrBind)
		}
		quote = "\""
	} else if utf8.RuneCountInString(name) > 64 {
		return nil, fmt.Errorf("%w: savepoint identifier length", ErrBind)
	}
	command := ""
	switch request.operation {
	case savepointCreate:
		command = "SAVEPOINT "
	case savepointRollback:
		command = "ROLLBACK TO SAVEPOINT "
	case savepointRelease:
		command = "RELEASE SAVEPOINT "
	default:
		return nil, ErrUnsupported
	}
	command += quote + strings.ReplaceAll(name, quote, quote+quote) + quote
	encoded, err := c.encodeSQL(command)
	if err != nil {
		return nil, err
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if c.activeTx == nil {
		return nil, fmt.Errorf("oceanbase: savepoint requires active transaction")
	}
	txctx := c.activeTx.ctx
	opctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(txctx, func() { cancel(context.Cause(txctx)) })
	defer func() { stop(); cancel(nil) }()
	if txctx.Err() != nil {
		cancel(context.Cause(txctx))
	}
	if err := c.commandOK(opctx, append([]byte{3}, encoded...)); err != nil {
		return nil, errors.Join(err, context.Cause(opctx))
	}
	return result{known: true}, nil
}
