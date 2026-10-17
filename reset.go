package oceanbase

import (
	"context"
	"database/sql/driver"
	"time"
)

func (c *conn) ResetSession(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		c.discard()
		return driver.ErrBadConn
	}
	defer c.unlock()
	c.endCancelExecution(true)
	c.clearDescriptors()
	c.lastWarnings = WarningSummary{}
	c.warningsCleared = true
	if c.closed.Load() || c.sessionDirty {
		c.discard()
		return driver.ErrBadConn
	}
	if interval := c.cfg.Routing.ValidationInterval; interval > 0 && time.Since(c.lastUsed) >= interval {
		if err := c.commandOK(ctx, []byte{14}); err != nil {
			c.discard()
			return driver.ErrBadConn
		}
	}
	if err := c.invalidateResourcesLocked(ctx, true); err != nil {
		c.discard()
		return driver.ErrBadConn
	}
	// No reliable OceanBase COM_RESET_CONNECTION condition is established yet.
	// Full reset uses a fresh physical session, so database/sql also rebuilds its statements.
	if c.cfg.ResetMode == ResetFull || c.activeTx != nil {
		c.discard()
		return driver.ErrBadConn
	}
	if c.session.ok.Status&1 != 0 {
		if err := c.commandOK(ctx, []byte("\x03ROLLBACK")); err != nil {
			c.discard()
			return driver.ErrBadConn
		}
	}
	if err := c.restoreAutocommit(ctx); err != nil {
		c.discard()
		return driver.ErrBadConn
	}
	return nil
}

var _ driver.SessionResetter = (*conn)(nil)
