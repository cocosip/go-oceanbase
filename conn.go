package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type conn struct {
	mu              chan struct{}
	session         *handshakeSession
	cfg             Config
	closed          atomic.Bool
	activeTx        *tx
	resources       resourceState                     // guarded by mu
	responseActive  bool                              // guarded by mu; internal commands require a complete response boundary
	descriptors     map[descriptorKey]*typeDescriptor // guarded by mu; one command lifetime
	descriptorBytes int64
	statements      statementCache
}

func (c *conn) Close() error {
	if c.closed.Load() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	if err := c.lock(ctx); err != nil {
		c.discard()
		return err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil
	}
	if err := c.clearStatementsLocked(ctx); err != nil {
		return err
	}
	c.session.packets.ResetSequence()
	err := c.session.packets.WriteMessage(ctx, []byte{1})
	c.discard()
	_ = c.cleanupResourcesLocked(ctx, true)
	return err
}

func (c *conn) Ping(ctx context.Context) error {
	if err := c.lock(ctx); err != nil {
		return err
	}
	defer c.unlock()
	return c.commandOK(ctx, []byte{14})
}

func (c *conn) IsValid() bool {
	return !c.closed.Load()
}

func (c *conn) lock(ctx context.Context) error {
	if err := validateTraceContext(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.mu <- struct{}{}:
		if err := ctx.Err(); err != nil {
			c.unlock()
			return err
		}
		c.enableDiagnostics()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *conn) unlock() {
	records, dropped := c.diagnosticSnapshots()
	<-c.mu
	c.emitDiagnostics(records, dropped)
}

// discard may run from cancellation while the operation still owns protocol state.
func (c *conn) discard() {
	if c.closed.CompareAndSwap(false, true) {
		_ = c.session.raw.Close()
	}
}

func (c *conn) commandOK(ctx context.Context, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed.Load() {
		return driver.ErrBadConn
	}
	p := c.session.packets
	p.ResetSequence()
	if err := p.WriteMessage(ctx, payload); err != nil {
		c.discard()
		return err
	}
	data, err := p.ReadMessage(ctx)
	if err != nil {
		c.discard()
		return err
	}
	if len(data) > 0 && data[0] == 0xff {
		err = decodeServerError(data)
		if err == wire.ErrMalformed {
			c.discard()
		}
		return err
	}
	ok, err := wire.DecodeOK(data, c.session.capabilities)
	if err != nil {
		c.discard()
		return err
	}
	if ok.Status&8 != 0 {
		c.discard()
		return fmt.Errorf("%w: unexpected additional command results", ErrUnsupported)
	}
	c.observeResourceStatus(ok.Status)
	c.session.ok = ok
	return nil
}

var (
	_ driver.Conn      = (*conn)(nil)
	_ driver.Pinger    = (*conn)(nil)
	_ driver.Validator = (*conn)(nil)
)
