//go:build windows

package transport_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	winio "github.com/Microsoft/go-winio"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/transport"
)

const platformPipeNetwork = "pipe"

func TestPlatformTransportNamedPipe(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformPeer(t, listener, config.Config{Network: platformPipeNetwork, Addr: path, User: testpeer.User})
}

func TestPlatformTransportNamedPipeUTF8(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-数据库-😀-�-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformPeer(t, listener, config.Config{Network: platformPipeNetwork, Addr: path, User: testpeer.User})
}

func TestPlatformTransportNamedPipeRetry(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-retry-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	cfg := config.Config{Network: platformPipeNetwork, Addr: path, User: testpeer.User,
		Routing: config.RoutingOptions{MaxAttempts: 2, RetryDelay: time.Millisecond, BlacklistTimeout: time.Millisecond},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			attempts++
			if attempts == 1 {
				return transport.Dial(ctx, network, addr+"-missing")
			}
			return transport.Dial(ctx, network, addr)
		},
	}
	runPlatformPeer(t, listener, cfg)
	if attempts != 2 {
		t.Fatalf("pipe dial attempts: got %d, want 2", attempts)
	}
}
