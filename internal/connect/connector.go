// Package connect establishes, authenticates and initializes physical sessions.
package connect

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/diagnostic"
	"github.com/cocosip/go-oceanbase/internal/routing"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// Connector owns shared selection, discovery, mode pinning and login identity pinning.
type Connector struct {
	cfg         config.Config
	modeMu      sync.Mutex
	mode        config.Mode
	identityMu  sync.Mutex
	identitySet bool
	user        string
	proxyUser   string
	routes      *routing.Selector
	discovery   *routing.Discovery
	closed      atomic.Bool
}

// New adopts an already normalized and snapshotted configuration without dialing.
func New(cfg config.Config) (*Connector, error) {
	c := &Connector{cfg: cfg}
	var err error
	if cfg.Discovery.Source != nil {
		c.discovery, err = newDiscovery(cfg.Discovery, cfg.Routing)
	} else {
		routes := cfg.Routing
		if cfg.Network != "tcp" {
			routes.Endpoints = []config.Endpoint{{Addr: cfg.Addr, Role: routes.Role}}
		}
		c.routes, err = routingSelector(routes)
	}
	if err != nil {
		return nil, &config.ConfigError{Field: "Routing/Discovery"}
	}
	return c, nil
}

type established struct {
	session *session.Session
	mode    config.Mode
	id      uint32
}

// Connect returns a fully initialized physical session within the total connection budget.
func (c *Connector) Connect(ctx context.Context) (result *session.Session, err error) {
	started := time.Now()
	requestCtx := ctx
	mode, id := c.cfg.Mode, uint32(0)
	defer func() {
		diagnostic.Emit(requestCtx, c.cfg, config.RequestDiagnostic{Operation: "connect", Mode: mode, ConnectionID: id, Duration: time.Since(started), Failed: err != nil})
	}()
	if err := diagnostic.ValidateTraceContext(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		return nil, dberrors.ErrConnectorClosed
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
		connected.session.Discard()
		return nil, dberrors.ErrConnectorClosed
	}
	mode, id = connected.mode, connected.id
	return connected.session, nil
}

// Close stops discovery without canceling established sessions.
func (c *Connector) Close() error {
	c.closed.Store(true)
	if c.discovery != nil {
		return c.discovery.Close()
	}
	return nil
}

// Config returns the immutable connector snapshot; callers must not mutate its maps.
func (c *Connector) Config() config.Config { return c.cfg }

// Closed reports whether new connections are disabled.
func (c *Connector) Closed() bool { return c.closed.Load() }

// DialCandidate opens and initializes exactly addr without discovery or failover.
// cfg must already be normalized, including any private-login policy overrides.
func DialCandidate(ctx context.Context, cfg config.Config, addr string) (*session.Session, error) {
	c := &Connector{cfg: cfg}
	connected, _, err := c.dialCandidate(ctx, addr)
	return connected.session, err
}

func (c *Connector) checkMode(actual config.Mode, pin bool) error {
	c.modeMu.Lock()
	defer c.modeMu.Unlock()
	if c.mode != config.ModeAuto && c.mode != actual {
		return dberrors.ErrModeMismatch
	}
	if pin {
		c.mode = actual
	}
	return nil
}

// checkIdentity pins the logical login identity for all physical sessions in this Connector.
// Passwords are intentionally excluded so credential rotation can continue without mixing users.
func (c *Connector) checkIdentity(protocol *session.Negotiated) error {
	c.identityMu.Lock()
	defer c.identityMu.Unlock()
	if !c.identitySet {
		c.user, c.proxyUser, c.identitySet = protocol.User, protocol.ProxyUser, true
		return nil
	}
	if c.user != protocol.User || c.proxyUser != protocol.ProxyUser {
		return dberrors.ErrIdentityMismatch
	}
	return nil
}
