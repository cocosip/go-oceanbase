// Package execution coordinates commands, feature owners and response finalization.
package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/control"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/diagnostic"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/xa"
)

// Engine composes concrete protocol owners for the root database/sql adapters.
type Engine struct {
	physical   *session.Session
	connector  *connect.Connector
	control    *control.Manager
	statements statement.Cache
	catalog    catalog.Resolver
	cursors    cursor.Manager
	lobs       lob.Manager
	xa         xa.Manager
}

// New binds one negotiated physical session to its concrete execution owners.
func New(connector *connect.Connector, physical *session.Session) *Engine {
	return &Engine{physical: physical, connector: connector, control: control.New()}
}

// Config returns the connection configuration snapshot.
func (e *Engine) Config() config.Config { return e.physical.Config() }

// Valid reports whether the session can return to its connection pool.
func (e *Engine) Valid() bool { return !e.physical.Closed() && !e.physical.Dirty() }

// Discard closes the transport without waiting for an active command.
func (e *Engine) Discard() { e.physical.Discard() }

func (e *Engine) acquire(ctx context.Context) (*session.Operation, error) {
	if err := diagnostic.ValidateTraceContext(ctx); err != nil {
		return nil, err
	}
	return e.physical.Acquire(ctx)
}

func (e *Engine) release(op *session.Operation) {
	cfg, p := op.Config(), op.Protocol()
	records, dropped := op.Release()
	diagnostic.EmitRecords(cfg, p.Mode, p.Hello.ConnectionID, records, dropped)
}

func (e *Engine) cleanup(ctx context.Context, op *session.Operation, all bool, retained []session.ResourceRef) error {
	return lifetime.Cleanup(ctx, op, &e.cursors, &e.lobs, all, retained)
}

// Close bounds cleanup and retires the physical connection.
func (e *Engine) Close() error {
	if e.physical.Closed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.Config().Limits.CleanupTimeout)
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		e.Discard()
		return err
	}
	defer e.release(op)
	e.physical.EndCancellation(true)
	if err = e.statements.Clear(ctx, op); err != nil {
		return err
	}
	op.Packets().ResetSequence()
	err = op.Packets().WriteMessage(ctx, []byte{1})
	e.Discard()
	_ = e.cleanup(ctx, op, true, nil)
	return err
}

// Ping checks the physical connection without changing SQL diagnostics.
func (e *Engine) Ping(ctx context.Context) error {
	op, err := e.acquire(ctx)
	if err != nil {
		return err
	}
	defer e.release(op)
	_, err = command.ExecOK(ctx, op, []byte{14})
	return err
}

// CloseResource retires a resource under its owning session operation.
func (e *Engine) CloseResource(ctx context.Context, ref session.ResourceRef) error {
	ctx, cancel := context.WithTimeout(ctx, e.Config().Limits.CleanupTimeout)
	defer cancel()
	op, err := e.acquire(ctx)
	if err != nil {
		if errors.Is(err, driver.ErrBadConn) && e.physical.Closed() {
			return nil
		}
		e.Discard()
		return err
	}
	defer e.release(op)
	if err = op.Validate(ref); err != nil {
		return err
	}
	return lifetime.Close(ctx, op, &e.cursors, &e.lobs, ref)
}
