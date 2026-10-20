package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/control"
	"github.com/cocosip/go-oceanbase/internal/execution"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
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
	return sqltext.ValidSavepointName(name)
}

// ErrCommitUnknown means the driver cannot establish whether COMMIT took effect.
// The transaction must not be replayed or reported as rolled back.
var ErrCommitUnknown = dberrors.ErrCommitUnknown

// ErrTxCleanup means COMMIT or ROLLBACK succeeded but session restoration failed.
var ErrTxCleanup = dberrors.ErrTxCleanup

type tx struct {
	engine      *execution.Engine
	transaction *control.Transaction
}

func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	t, err := c.engine.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &tx{engine: c.engine, transaction: t}, nil
}
func (t *tx) Commit() error                            { return t.engine.FinishTransaction(t.transaction, true) }
func (t *tx) Rollback() error                          { return t.engine.FinishTransaction(t.transaction, false) }
func (c *conn) ResetSession(ctx context.Context) error { return c.engine.ResetSession(ctx) }
func (c *conn) execSavepoint(ctx context.Context, r savepointRequest) (driver.Result, error) {
	return command.Result{Known: true}, c.engine.Savepoint(ctx, control.SavepointRequest{Kind: control.SavepointKind(r.operation), Name: r.name})
}

var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.SessionResetter = (*conn)(nil)
var _ driver.Tx = (*tx)(nil)
