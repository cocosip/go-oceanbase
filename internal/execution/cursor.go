package execution

import (
	"context"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/session"
)

func (e *Engine) writable(op *session.Operation, transactionSession bool) bool {
	tx := e.control.ActiveTransaction()
	return transactionSession && tx != nil && !tx.ReadOnly() && op.Protocol().OK.Status&8192 == 0
}

// OpenCursor publishes a cursor after its opening response is complete.
func (e *Engine) OpenCursor(ctx context.Context, query string, args []driver.NamedValue, options cursor.Options, transactionSession bool) (ref session.ResourceRef, err error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return ref, err
	}
	defer e.release(op)
	ref, err = e.cursors.Open(ctx, op, &e.catalog, query, args, options, e.writable(op, transactionSession))
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = op.Publish(ref)
	}
	if err != nil && ref != (session.ResourceRef{}) {
		err = errors.Join(err, e.retire(op, ref))
	}
	return ref, err
}

func (e *Engine) retire(op *session.Operation, ref session.ResourceRef) error {
	ctx, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
	defer cancel()
	return lifetime.Close(ctx, op, &e.cursors, &e.lobs, ref)
}

// FetchCursor reads a bounded cursor batch under its resource epoch.
func (e *Engine) FetchCursor(ctx context.Context, ref session.ResourceRef, pos cursor.Position, count int, busy *cursor.BusyToken) (driver.Rows, bool, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer e.release(op)
	rows, retire, err := e.cursors.Fetch(ctx, op, &e.catalog, ref, pos, count, busy)
	if retire {
		err = errors.Join(err, e.retire(op, ref))
	}
	return rows, retire, err
}

// MutateCursor applies a positioned mutation in the owning transaction.
func (e *Engine) MutateCursor(ctx context.Context, ref session.ResourceRef, request cursor.Request, transactionSession bool) (command.Result, bool, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return command.Result{}, false, err
	}
	defer e.release(op)
	retired := false
	result, retire, err := e.cursors.Mutate(ctx, op, &e.catalog, ref, request, e.writable(op, transactionSession), func() error {
		retired = true
		return e.retire(op, ref)
	})
	if retire && !retired {
		err = errors.Join(err, e.retire(op, ref))
	}
	return result, retire, err
}

// RefreshCursor replaces a sensitive cursor row with its current values.
func (e *Engine) RefreshCursor(ctx context.Context, ref session.ResourceRef, request cursor.Request, transactionSession bool, busy *cursor.BusyToken) (driver.Rows, bool, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer e.release(op)
	rows, retire, err := e.cursors.Refresh(ctx, op, &e.catalog, ref, request, e.writable(op, transactionSession), busy)
	if retire {
		err = errors.Join(err, e.retire(op, ref))
	}
	return rows, retire, err
}
