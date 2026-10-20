package obproxy

import (
	"context"
	"net"
	"testing"
)

// NewTestRuntime constructs only a test runtime; native initialization is supplied by the fixture.
func NewTestRuntime(t *testing.T, socket string, initialize func() error) (string, func(context.Context, string, string) (net.Conn, error), func(context.Context) error) {
	spec := obProxyTestSpec(t)
	if socket != "" {
		spec.socket = socket
	}
	r := &obProxyRuntime{initialize: func(obProxySpec) error { return initialize() }}
	return spec.socket, r.dialer(spec), func(ctx context.Context) error { return r.start(ctx, spec) }
}
