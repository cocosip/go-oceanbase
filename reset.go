package oceanbase

import (
	"context"
	"database/sql/driver"
)

func (c *conn) ResetSession(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		c.discard()
		return driver.ErrBadConn
	}
	defer c.unlock()
	c.clearDescriptors()
	if c.closed.Load() {
		return driver.ErrBadConn
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
