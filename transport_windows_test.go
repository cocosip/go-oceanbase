//go:build windows

package oceanbase

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
)

func TestPlatformTransportNamedPipe(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformPeer(t, listener, Config{Network: networkPipe, Addr: path, User: testUser})
}

func TestPlatformTransportNamedPipeRetry(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-retry-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	cfg := Config{Network: networkPipe, Addr: path, User: testUser,
		Routing: RoutingOptions{MaxAttempts: 2, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			attempts++
			if attempts == 1 {
				return dialTransport(ctx, network, addr+"-missing")
			}
			return dialTransport(ctx, network, addr)
		},
	}
	runPlatformPeer(t, listener, cfg)
	if attempts != 2 {
		t.Fatalf("pipe dial attempts: got %d, want 2", attempts)
	}
}

func TestPlatformTransportPipeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if raw, err := dialTransport(ctx, networkPipe, `\\.\pipe\go-oceanbase-missing`); !errors.Is(err, context.Canceled) || raw != nil {
		t.Fatal("canceled pipe dial", err)
	}
}

func TestPlatformTransportNamedPipeTLSGate(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-tls-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformTLSGate(t, listener, Config{User: testUser, Password: testPassword, Network: networkPipe, Addr: path})
}
