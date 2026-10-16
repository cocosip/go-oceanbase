package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
)

// ErrUnsupported indicates that a requested capability is unavailable.
var ErrUnsupported = errors.New("oceanbase: unsupported capability")

type connector struct {
	cfg    Config
	modeMu sync.Mutex
	mode   Mode
}

// NewConnector validates and snapshots cfg without opening a connection.
func NewConnector(cfg Config) (driver.Connector, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &connector{cfg: cfg}, nil
}

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	logOperation(ctx, c.cfg.Logger, slog.LevelDebug, "connect")
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	dial := c.cfg.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	raw, err := dial(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		if raw != nil {
			_ = raw.Close()
		}
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("oceanbase: dialer returned a nil connection")
	}
	if err = ctx.Err(); err != nil {
		_ = raw.Close()
		return nil, err
	}
	session, err := c.authenticate(ctx, raw)
	if err != nil {
		return nil, err
	}
	conn := &conn{session: session, cfg: c.cfg, mu: make(chan struct{}, 1)}
	if err = c.checkMode(session.mode, false); err == nil {
		err = conn.initialize(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = c.checkMode(session.mode, true)
	}
	if err != nil {
		conn.discard()
		return nil, err
	}
	return conn, nil
}

func (c *connector) checkMode(actual Mode, pin bool) error {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if c.mode != ModeAuto && c.mode != actual {
		return ErrModeMismatch
	}
	if pin {
		c.mode = actual
	}
	return nil
}

func (*connector) Driver() driver.Driver { return drv{} }

var _ driver.Connector = (*connector)(nil)
