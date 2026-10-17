package oceanbase

import (
	"context"
	"fmt"
	"net"
	"time"
)

func ExampleConfig_tcpOptions() {
	dialer := &net.Dialer{
		// Replace this with a local interface address, or use nil for automatic selection.
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
		KeepAliveConfig: net.KeepAliveConfig{
			Enable: true, Idle: 30 * time.Second, Interval: 10 * time.Second, Count: 3,
		},
	}
	connector, err := NewConnector(Config{
		User: "tcp_app", DialContext: tcpExampleDialer(dialer, false),
	})
	// Constructing a Connector does not dial. The socket policy runs on each new connection.
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}

// This is application example code; no additional driver options or factory interfaces are needed.
func tcpExampleDialer(dialer *net.Dialer, abortiveClose bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" {
			return nil, fmt.Errorf("%w: example dialer requires TCP", ErrUnsupported)
		}
		raw, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		tcp, ok := raw.(*net.TCPConn)
		if !ok {
			_ = raw.Close()
			return nil, fmt.Errorf("%w: example dialer requires a TCP connection", ErrUnsupported)
		}
		if err = tcp.SetNoDelay(true); err == nil {
			err = tcp.SetReadBuffer(64 << 10)
		}
		if err == nil && dialer.KeepAliveConfig.Enable {
			// Dialer applies keep-alive on a best-effort basis; check explicit policy errors here.
			err = tcp.SetKeepAliveConfig(dialer.KeepAliveConfig)
		}
		if err == nil {
			err = tcp.SetWriteBuffer(64 << 10)
		}
		if err == nil {
			linger := -1
			if abortiveClose {
				linger = 0 // Explicitly discards pending data; it does not roll back database work.
			}
			err = tcp.SetLinger(linger)
		}
		if err != nil {
			_ = raw.Close()
			return nil, err
		}
		return raw, nil
	}
}
