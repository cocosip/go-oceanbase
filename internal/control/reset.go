package control

import (
	"context"
	"database/sql/driver"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// Reset restores a managed pool baseline under the caller's cleanup deadline.
func (m *Manager) Reset(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, cursors *cursor.Manager, lobs *lob.Manager) error {
	op.EndCancellation(true)
	resolver.Clear()
	m.ResetWarnings()
	bad := func() error { op.Discard(); return driver.ErrBadConn }
	if op.Closed() || op.Dirty() {
		return bad()
	}
	if interval := op.Config().Routing.ValidationInterval; interval > 0 && time.Since(op.LastUsed()) >= interval {
		if _, err := command.ExecOK(ctx, op, []byte{14}); err != nil {
			return bad()
		}
	}
	if err := invalidateResources(ctx, op, cursors, lobs, true); err != nil {
		return bad()
	}
	// Full reset uses a fresh physical session until a server reset contract is established.
	if op.Config().ResetMode == config.ResetFull || m.activeTx != nil {
		return bad()
	}
	if op.Protocol().OK.Status&1 != 0 {
		if _, err := command.ExecOK(ctx, op, []byte("\x03ROLLBACK")); err != nil {
			return bad()
		}
	}
	if err := command.RestoreAutocommit(ctx, op); err != nil {
		return bad()
	}
	return nil
}
