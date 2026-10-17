package oceanbase

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/cocosip/go-oceanbase/internal/routing"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type handshakeTransportError struct{ error }

func (e *handshakeTransportError) Unwrap() error { return e.error }

func (c *connector) connectCandidate(ctx context.Context, routes *routing.Selector, options RoutingOptions) (*conn, error) {
	var last error
	attempts := 0
	for attempts < options.MaxAttempts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		addresses, delay := routes.Order(time.Now())
		if len(addresses) == 0 {
			if err := waitCandidate(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		for _, address := range addresses {
			attempts++
			attemptCtx, stop := context.WithTimeout(ctx, options.AttemptTimeout)
			session, retry, err := c.dialCandidate(attemptCtx, address.Addr)
			stop()
			if err == nil {
				routes.Success(address.Addr)
				return session, nil
			}
			last = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retry {
				return nil, err
			}
			routes.Failure(address.Addr, time.Now())
			if attempts == options.MaxAttempts {
				return nil, last
			}
			if err := waitCandidate(ctx, options.RetryDelay); err != nil {
				return nil, err
			}
		}
	}
	return nil, last
}

func (c *connector) dialCandidate(ctx context.Context, addr string) (*conn, bool, error) {
	dial := c.cfg.DialContext
	if dial == nil {
		dial = dialTransport
	}
	raw, err := dial(ctx, c.cfg.Network, addr)
	if err != nil {
		if raw != nil {
			_ = raw.Close()
		}
		return nil, retryDialError(err), err
	}
	if raw == nil {
		return nil, false, fmt.Errorf("oceanbase: dialer returned a nil connection")
	}
	if err = ctx.Err(); err != nil {
		_ = raw.Close()
		return nil, true, err
	}
	if c.cfg.ReadTimeout > 0 {
		raw = &readTimeoutConn{Conn: raw, timeout: c.cfg.ReadTimeout}
	}
	// A new value avoids copying the shared mode mutex; TLS verifies the selected host.
	cfg := c.cfg
	cfg.Addr = addr
	authenticator := &connector{cfg: cfg}
	session, err := authenticator.authenticate(ctx, raw)
	if err != nil {
		return nil, retryHandshakeError(err), err
	}
	connected := &conn{session: session, cfg: cfg, mu: make(chan struct{}, 1)}
	if err = c.checkMode(session.mode, false); err == nil {
		err = connected.initialize(ctx)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = c.checkMode(session.mode, true)
	}
	if err != nil {
		connected.discard()
		return nil, false, err
	}
	connected.lastUsed = time.Now()
	connected.lastWarnings = WarningSummary{}
	connected.warningsCleared = true
	return connected, false, nil
}

func retryHandshakeError(err error) bool {
	// crypto/tls wraps fatal protocol/credential alerts in net.OpError too.
	var network *net.OpError
	if errors.As(err, &network) && (network.Op == "remote error" || network.Op == "local error") {
		return false
	}
	var transport *wire.IOError
	var handshake *handshakeTransportError
	return errors.As(err, &transport) || errors.As(err, &handshake)
}

func retryDialError(err error) bool {
	var network *net.OpError
	var dns *net.DNSError
	if errors.As(err, &dns) && !dns.IsTimeout && !dns.IsTemporary {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
}

func waitCandidate(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
