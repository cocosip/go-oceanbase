package execution

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/control"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/xa"
)

// BeginTx validates options before beginning a caller-owned transaction.
func (e *Engine) BeginTx(ctx context.Context, opts driver.TxOptions) (*control.Transaction, error) {
	if e.Config().Routing.Role == config.RoleReadOnly && !opts.ReadOnly {
		return nil, fmt.Errorf("%w: read-write transaction on read-only service", dberrors.ErrUnsupported)
	}
	if _, err := control.TransactionCommands(e.Config().Mode, opts); err != nil {
		return nil, err
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer e.release(op)
	return e.control.Begin(ctx, op, &e.cursors, &e.lobs, opts, e.xa.Used())
}

// FinishTransaction cleans transaction resources before COMMIT or ROLLBACK.
func (e *Engine) FinishTransaction(t *control.Transaction, commit bool) error {
	ctx := t.Context()
	cancel := func() {}
	if !commit {
		ctx, cancel = context.WithTimeout(context.Background(), e.Config().Limits.CleanupTimeout)
	}
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		e.Discard()
		return err
	}
	defer e.release(op)
	return e.control.Finish(ctx, op, &e.cursors, &e.lobs, t, commit)
}

// ResetSession restores the connection pool baseline with bounded cleanup.
func (e *Engine) ResetSession(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, e.Config().Limits.CleanupTimeout)
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		e.Discard()
		return driver.ErrBadConn
	}
	defer e.release(op)
	return e.control.Reset(ctx, op, &e.catalog, &e.cursors, &e.lobs)
}

// ChangeUser replaces credentials only at a complete response boundary.
func (e *Engine) ChangeUser(ctx context.Context, credentials config.Credentials, database string) error {
	ctx, cancel := context.WithTimeout(ctx, e.Config().ConnectTimeout)
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return catalog.ErrResponseActive
	}
	return e.control.ChangeUser(ctx, op, &e.statements, &e.catalog, &e.cursors, &e.lobs, credentials, database, e.xa.Used())
}

// Session performs a validated session operation and preserves SQL warnings.
func (e *Engine) Session(ctx context.Context, request control.SessionRequest) ([]sql.NullString, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return nil, catalog.ErrResponseActive
	}
	return e.control.Session(ctx, op, &e.statements, &e.catalog, request, e.xa.Used())
}

// Savepoint applies a savepoint operation under the session gate.
func (e *Engine) Savepoint(ctx context.Context, request control.SavepointRequest) error {
	op, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(op)
	return e.control.Savepoint(ctx, op, request)
}

// Warnings reads the last ordinary SQL warning count.
func (e *Engine) Warnings(ctx context.Context) (control.Summary, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return control.Summary{}, err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return control.Summary{}, catalog.ErrResponseActive
	}
	return e.control.Warnings(), nil
}

// ClearWarnings clears the server diagnostic area.
func (e *Engine) ClearWarnings(ctx context.Context) error {
	op, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return catalog.ErrResponseActive
	}
	e.control.ClearWarnings()
	return nil
}

// ReadWarnings reads MySQL warning details under the session gate.
func (e *Engine) ReadWarnings(ctx context.Context) ([]control.Warning, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return nil, catalog.ErrResponseActive
	}
	return e.control.ReadWarnings(ctx, op)
}

// QueryTarget identifies the active query for cancellation.
func (e *Engine) QueryTarget(ctx context.Context) (session.Target, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return session.Target{}, err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return session.Target{}, catalog.ErrResponseActive
	}
	return op.ArmTarget(), nil
}

// CancelQuery operates on the separate auxiliary connector without taking the target gate.
func (e *Engine) CancelQuery(ctx context.Context, target session.Target) error {
	return control.CancelQuery(ctx, e.connector, target)
}

// XA dispatches a validated distributed transaction request.
func (e *Engine) XA(ctx context.Context, request *xa.Request) error {
	if request == nil {
		return dberrors.ErrXA
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(op)
	if op.ResponseActive() {
		return catalog.ErrResponseActive
	}
	if err = e.xa.Validate(op, request, e.control.ActiveTransaction() != nil); err != nil {
		return err
	}
	if op.Protocol().Mode == config.ModeOracle && !statement.SupportsPrepareExecute(op.Protocol().OBVersion) {
		return errors.Join(dberrors.ErrXA, dberrors.ErrUnsupported)
	}
	op.AdvanceResources()
	cleanup, cancel := context.WithTimeout(ctx, e.Config().Limits.CleanupTimeout)
	err = e.cleanup(cleanup, op, true, nil)
	cancel()
	if err != nil {
		return err
	}
	e.catalog.Clear()
	if err = ctx.Err(); err != nil {
		return err
	}
	return e.xa.Execute(ctx, op, &e.catalog, request)
}
