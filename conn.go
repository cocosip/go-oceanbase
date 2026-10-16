package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"
	"sync"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type conn struct {
	mu      sync.Mutex
	session *handshakeSession
	cfg     Config
	closed  bool
}

func (c *conn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("%w: prepared statements", ErrUnsupported)
}
func (c *conn) Begin() (driver.Tx, error) { return nil, fmt.Errorf("%w: transactions", ErrUnsupported) }

func (c *conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer cancel()
	c.session.packets.ResetSequence()
	err := c.session.packets.WriteMessage(ctx, []byte{1})
	c.discard()
	return err
}

func (c *conn) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commandOK(ctx, []byte{14})
}

func (c *conn) IsValid() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}

// discard is called with the operation lock held or before publishing the connection.
func (c *conn) discard() {
	if !c.closed {
		c.closed = true
		_ = c.session.raw.Close()
	}
}

func (c *conn) commandOK(ctx context.Context, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.closed {
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
	c.session.ok = ok
	return nil
}

var (
	_ driver.Conn      = (*conn)(nil)
	_ driver.Pinger    = (*conn)(nil)
	_ driver.Validator = (*conn)(nil)
)
