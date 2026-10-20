package execution

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// OpenLOB captures one native locator after consuming the entire query response.
func (e *Engine) OpenLOB(ctx context.Context, query string, args []driver.NamedValue, expectedType byte, transactionSession bool) (ref session.ResourceRef, err error) {
	if expectedType != 210 && expectedType != 211 {
		return ref, dberrors.ErrLOB
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return ref, err
	}
	defer e.release(op)
	if op.Protocol().Mode != config.ModeOracle {
		return ref, dberrors.ErrUnsupported
	}
	defer func() { ref, err = e.completeLOBOpen(ctx, op, ref, err) }()
	epoch := op.Epoch()
	rows, err := e.queryLocked(ctx, op, query, args, Options{RetainOperation: true, CaptureLOB: true})
	if err != nil {
		return ref, err
	}
	sets, err := e.collectLOBOpen(ctx, op, rows, epoch)
	if err != nil {
		return ref, err
	}
	return e.selectLOB(op, expectedType, sets, transactionSession)
}

func (e *Engine) collectLOBOpen(ctx context.Context, op *session.Operation, rows *Rows, epoch session.Epoch) ([]materialize.Set, error) {
	rows.holdRelease = true
	sets, err := rows.collect(ctx)
	rows.holdRelease = false
	if err != nil {
		return nil, rows.fail(err, true)
	}
	if op.Epoch() != epoch {
		return nil, rows.fail(dberrors.ErrResource, false)
	}
	rows.finish(ctx)
	return sets, rows.err
}

func (e *Engine) selectLOB(op *session.Operation, expectedType byte, sets []materialize.Set, transactionSession bool) (session.ResourceRef, error) {
	var refs []session.ResourceRef
	for _, set := range sets {
		for _, row := range set.Rows {
			for i, column := range set.Columns {
				if column.Type != 210 && column.Type != 211 || row[i] == nil {
					continue
				}
				if transactionSession != (e.control.ActiveTransaction() != nil) {
					return session.ResourceRef{}, dberrors.ErrResource
				}
				raw, ok := row[i].([]byte)
				if !ok {
					op.Discard()
					return session.ResourceRef{}, dberrors.ErrLOB
				}
				ref, err := e.lobs.OwnNative(op, column, raw, nil)
				if err != nil {
					op.Discard()
					return session.ResourceRef{}, err
				}
				refs = append(refs, ref)
			}
		}
	}
	if len(sets) != 1 || len(sets[0].Columns) != 1 || sets[0].Columns[0].Type != expectedType {
		return session.ResourceRef{}, dberrors.ErrLOB
	}
	if len(sets[0].Rows) == 0 {
		return session.ResourceRef{}, sql.ErrNoRows
	}
	if len(sets[0].Rows) != 1 {
		return session.ResourceRef{}, dberrors.ErrLOB
	}
	if sets[0].Rows[0][0] == nil {
		return session.ResourceRef{}, nil
	}
	if err := op.Publish(refs[0]); err != nil {
		return session.ResourceRef{}, err
	}
	return refs[0], nil
}

func (e *Engine) completeLOBOpen(ctx context.Context, op *session.Operation, ref session.ResourceRef, err error) (session.ResourceRef, error) {
	cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
	defer cancel()
	err = errors.Join(err, e.cleanup(cleanup, op, false, nil))
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		if ref != (session.ResourceRef{}) {
			err = errors.Join(err, lifetime.Close(cleanup, op, &e.cursors, &e.lobs, ref))
		}
		return session.ResourceRef{}, err
	}
	return ref, nil
}

// OpenTableBlob resolves an explicit base-table target before publishing a resource.
func (e *Engine) OpenTableBlob(ctx context.Context, target catalog.Target, transactionSession bool) (ref session.ResourceRef, err error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return ref, err
	}
	defer e.release(op)
	defer func() { ref, err = e.completeLOBOpen(ctx, op, ref, err) }()
	epoch := op.Epoch()
	plan, err := catalog.LookupTableBlob(ctx, op, target)
	if err != nil {
		return ref, err
	}
	if op.Protocol().Mode == config.ModeOracle {
		query := "SELECT " + plan.Column + plan.From()
		if e.writable(op, transactionSession) {
			query += " FOR UPDATE"
		}
		payload, err := command.Interpolate(op, query, plan.Args)
		if err != nil {
			return ref, err
		}
		epoch := op.Epoch()
		rows, err := e.start(ctx, op, append([]byte{3}, payload...), false, nil, nil, nil, Options{RetainOperation: true, CaptureLOB: true}, true, true)
		if err != nil {
			return ref, err
		}
		sets, err := e.collectLOBOpen(ctx, op, rows, epoch)
		if err != nil {
			return ref, err
		}
		return e.selectLOB(op, 210, sets, transactionSession)
	}
	result, err := catalog.QueryRows(ctx, op, catalog.Query{SQL: "SELECT OCTET_LENGTH(" + plan.Column + ")" + plan.From(), Args: plan.Args})
	if err != nil {
		return ref, err
	}
	if op.Epoch() != epoch {
		return ref, dberrors.ErrResource
	}
	value, err := catalog.Scalar(result)
	if err != nil || value == nil {
		return ref, err
	}
	if _, err = catalog.Length(value); err != nil {
		return ref, err
	}
	if transactionSession != (e.control.ActiveTransaction() != nil) {
		return ref, dberrors.ErrResource
	}
	ref, err = e.lobs.OwnTable(op, plan)
	if err == nil {
		err = op.Publish(ref)
	}
	return ref, err
}
