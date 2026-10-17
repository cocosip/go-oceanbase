package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"time"

	"github.com/cocosip/go-oceanbase/internal/routing"
)

// ErrUnsupported indicates that a requested capability is unavailable.
var ErrUnsupported = errors.New("oceanbase: unsupported capability")

type connector struct {
	cfg    Config
	modeMu sync.Mutex
	mode   Mode
	routes *routing.Selector
}

// NewConnector validates and snapshots cfg without opening a connection.
func NewConnector(cfg Config) (driver.Connector, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	routes, err := cfg.Routing.selector()
	if err != nil {
		return nil, &ConfigError{Field: "Routing"}
	}
	return &connector{cfg: cfg, routes: routes}, nil
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
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	connected, err := c.connectCandidate(ctx)
	if err != nil {
		return nil, err
	}
	return connected, nil
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
