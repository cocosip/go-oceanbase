package lob

import (
	"context"
	"errors"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type record struct {
	native *Native
	table  *catalog.TableBlobPlan
	parent *statement.Use
	scope  session.StatementScope
}

// Manager owns LOB implementations and their parent leases under the session gate.
type Manager struct {
	records map[session.ResourceRef]*record
}

// OwnNative validates a locator and retains its parent statement lease.
func (m *Manager) OwnNative(op *session.Operation, column wire.Column, raw []byte, parent *statement.Statement) (session.ResourceRef, error) {
	n, err := AdoptLocator(column, raw)
	if err != nil {
		return session.ResourceRef{}, err
	}
	return m.own(op, &record{native: n, scope: op.StatementScope()}, parent)
}

// OwnTable registers a validated table-backed BLOB plan.
func (m *Manager) OwnTable(op *session.Operation, plan *catalog.TableBlobPlan) (session.ResourceRef, error) {
	return m.own(op, &record{table: plan, scope: op.StatementScope()}, nil)
}

func (m *Manager) own(op *session.Operation, r *record, parent *statement.Statement) (session.ResourceRef, error) {
	var err error
	if parent != nil {
		r.parent, err = parent.AcquireLease(op)
		if err != nil {
			return session.ResourceRef{}, err
		}
	}
	ref, err := op.Register(session.LOBResource)
	if err != nil {
		return session.ResourceRef{}, errors.Join(err, r.parent.Release(context.Background(), op))
	}
	if m.records == nil {
		m.records = make(map[session.ResourceRef]*record)
	}
	m.records[ref] = r
	return ref, nil
}

// Close retires the concrete resource; lifetime releases the returned parent afterwards.
func (m *Manager) Close(ctx context.Context, op *session.Operation, ref session.ResourceRef) (*statement.Use, error) {
	r := m.records[ref]
	if r == nil {
		return nil, nil
	}
	delete(m.records, ref)
	var err error
	if r.native != nil && r.scope == op.StatementScope() {
		err = Free(ctx, op, r.native)
	}
	return r.parent, err
}

// Execute validates a published handle before dispatching to its implementation.
func (m *Manager) Execute(ctx context.Context, op *session.Operation, ref session.ResourceRef, operation Operation, offset, count int64, data []byte, text string, writableTransaction bool) (Result, error) {
	published, err := op.Published(ref)
	if err != nil || !published {
		return Result{}, dberrors.ErrResource
	}
	r := m.records[ref]
	if r == nil {
		return Result{}, dberrors.ErrResource
	}
	if r.table != nil {
		return (&tableOperation{op: op, ref: ref, writable: writableTransaction}).execute(ctx, r.table, operation, offset, count, data, text)
	}
	if r.native == nil {
		return Result{}, dberrors.ErrResource
	}
	return ExecuteNative(ctx, op, r.native, operation, offset, count, data, text)
}
