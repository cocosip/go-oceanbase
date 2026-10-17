package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cocosip/go-oceanbase/internal/routing"
)

// ErrUnsupported indicates that a requested capability is unavailable.
var ErrUnsupported = errors.New("oceanbase: unsupported capability")

type connector struct {
	cfg       Config
	modeMu    sync.Mutex
	mode      Mode
	routes    *routing.Selector
	discovery *routing.Discovery
	closed    atomic.Bool
}

// NewConnector validates and snapshots cfg without opening a connection.
func NewConnector(cfg Config) (driver.Connector, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	c := &connector{cfg: cfg}
	if cfg.Discovery.Source != nil {
		c.discovery, err = cfg.Discovery.discovery(cfg.Routing)
	} else {
		c.routes, err = cfg.Routing.selector()
	}
	if err != nil {
		return nil, &ConfigError{Field: "Routing/Discovery"}
	}
	return c, nil
}

func (c *connector) Connect(ctx context.Context) (result driver.Conn, err error) {
	started := time.Now()
	requestCtx := ctx
	defer func() {
		d := RequestDiagnostic{Operation: "connect", Mode: c.cfg.Mode, Duration: time.Since(started), Failed: err != nil}
		if connected, ok := result.(*conn); ok {
			d.Mode, d.ConnectionID = connected.session.mode, connected.session.hello.ConnectionID
		}
		emitDiagnostic(requestCtx, c.cfg, d)
	}()
	if err := validateTraceContext(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		return nil, ErrConnectorClosed
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	routes, options := c.routes, c.cfg.Routing
	if c.discovery != nil {
		var count int
		routes, count, err = c.discovery.Get(ctx)
		if err != nil {
			return nil, err
		}
		if options.MaxAttempts == 0 {
			options.MaxAttempts = count
		}
		if options.AttemptTimeout == 0 {
			options.AttemptTimeout = max(c.cfg.ConnectTimeout/time.Duration(min(count, options.MaxAttempts)), time.Nanosecond)
		}
	}
	connected, err := c.connectCandidate(ctx, routes, options)
	if err != nil {
		return nil, err
	}
	if c.closed.Load() {
		connected.discard()
		return nil, ErrConnectorClosed
	}
	return connected, nil
}

// Close stops shared discovery without canceling established business sessions.
func (c *connector) Close() error {
	c.closed.Store(true)
	if c.discovery != nil {
		return c.discovery.Close()
	}
	return nil
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
var _ io.Closer = (*connector)(nil)
