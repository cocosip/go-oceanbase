package connect

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/diagnostic"
	"github.com/cocosip/go-oceanbase/internal/routing"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/transport"
)

func (c *Connector) connectCandidate(ctx context.Context, routes *routing.Selector, options config.RoutingOptions) (established, error) {
	var last error
	attempts := 0
	for attempts < options.MaxAttempts {
		if err := ctx.Err(); err != nil {
			return established{}, err
		}
		addresses, delay := routes.Order(time.Now())
		if len(addresses) == 0 {
			if err := waitCandidate(ctx, delay); err != nil {
				return established{}, err
			}
			continue
		}
		for _, address := range addresses {
			attempts++
			attemptCtx, stop := context.WithTimeout(ctx, options.AttemptTimeout)
			connected, retry, err := c.dialCandidate(attemptCtx, address.Addr)
			stop()
			if err == nil {
				routes.Success(address.Addr)
				return connected, nil
			}
			last = err
			if ctx.Err() != nil {
				return established{}, ctx.Err()
			}
			if !retry {
				return established{}, err
			}
			routes.Failure(address.Addr, time.Now())
			if attempts == options.MaxAttempts {
				return established{}, last
			}
			if err := waitCandidate(ctx, options.RetryDelay); err != nil {
				return established{}, err
			}
		}
	}
	return established{}, last
}

func (c *Connector) dialCandidate(ctx context.Context, addr string) (established, bool, error) {
	dial := c.cfg.DialContext
	if dial == nil {
		dial = transport.Dial
	}
	raw, err := dial(ctx, c.cfg.Network, addr)
	if err != nil {
		if !nilConnection(raw) {
			_ = raw.Close()
		}
		return established{}, RetryDialError(err), err
	}
	if nilConnection(raw) {
		return established{}, false, fmt.Errorf("oceanbase: dialer returned a nil connection")
	}
	if err = ctx.Err(); err != nil {
		_ = raw.Close()
		return established{}, true, err
	}
	if c.cfg.ReadTimeout > 0 {
		raw = transport.WithReadTimeout(raw, c.cfg.ReadTimeout)
	}
	// TLS verifies the selected host while the shared connector snapshot stays immutable.
	cfg := c.cfg
	cfg.Addr = addr
	protocol, err := Authenticate(ctx, cfg, raw)
	if err != nil {
		return established{}, RetryHandshakeError(err), err
	}
	physical := session.New(cfg, protocol)
	if err = c.checkMode(protocol.Mode, false); err == nil {
		var op *session.Operation
		op, err = physical.Acquire(ctx)
		if err == nil {
			err = Initialize(ctx, op, cfg)
			records, dropped := op.Release()
			diagnostic.EmitRecords(cfg, protocol.Mode, protocol.Hello.ConnectionID, records, dropped)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = c.checkMode(protocol.Mode, true)
	}
	if err != nil {
		physical.Discard()
		return established{}, false, err
	}
	return established{session: physical, mode: protocol.Mode, id: protocol.Hello.ConnectionID}, false, nil
}

func nilConnection(raw net.Conn) bool {
	if raw == nil {
		return true
	}
	v := reflect.ValueOf(raw)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
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
