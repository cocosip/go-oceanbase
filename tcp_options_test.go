package oceanbase

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestTCPDialContext(t *testing.T) {
	listener, err := net.Listen(networkTCP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: 30 * time.Second,
			Interval: 10 * time.Second, Count: 3},
	}
	dial := tcpExampleDialer(dialer, false)
	var calls atomic.Int32
	cfg := Config{User: testUser, Addr: listener.Addr().String(), DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		raw, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		if local, ok := raw.LocalAddr().(*net.TCPAddr); !ok || !local.IP.Equal(dialer.LocalAddr.(*net.TCPAddr).IP) {
			t.Error("custom local interface was not used")
		}
		if raw.RemoteAddr().String() != listener.Addr().String() {
			t.Error("factory did not receive the selected endpoint")
		}
		return raw, nil
	}}
	runPlatformPeer(t, listener, cfg)
	if calls.Load() != 1 {
		t.Fatal("connection factory was bypassed or reused for individual commands", calls.Load())
	}
}

func TestTCPDialContextTLSGate(t *testing.T) {
	listener, err := net.Listen(networkTCP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	runPlatformTLSGate(t, listener, Config{User: testUser, Password: testPassword, Addr: listener.Addr().String(), DialContext: tcpExampleDialer(&net.Dialer{}, false)})
}

func TestTCPDialContextPreflight(t *testing.T) {
	dial := tcpExampleDialer(&net.Dialer{}, false)
	for _, network := range []string{networkUnix, networkPipe} {
		if raw, err := dial(context.Background(), network, "unused"); raw != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatal("TCP factory silently accepted a different transport", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if raw, err := dial(ctx, networkTCP, "127.0.0.1:2881"); raw != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("TCP factory ignored canceled context", err)
	}
	for _, name := range []string{"tcpNoDelay", "tcpAbortiveClose", "localSocketAddress", "tcpKeepAlive", "tcpRcvBuf", "tcpSndBuf", "socketFactory"} {
		_, err := ParseDSN("oceanbase://app@localhost/?" + name + "=true")
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatal("Java socket options were silently treated as Go DSN options", name, err)
		}
	}
}

func TestTCPDialContextAbortiveClose(t *testing.T) {
	listener, err := net.Listen(networkTCP, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := tcpExampleDialer(&net.Dialer{KeepAlive: -1}, true)(ctx, networkTCP, listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	if err = server.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil {
		t.Fatal(err)
	}
	var data [1]byte
	_, err = server.Read(data[:])
	var timeout net.Error
	if err == nil || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatal("explicit abortive close left the transport open", err)
	}
}
