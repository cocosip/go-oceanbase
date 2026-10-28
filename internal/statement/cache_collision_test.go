package statement

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// TestCacheCollisionEvictionFailureReleasesNewStatement locks that a failed
// eviction during a key collision still retires the newly prepared statement,
// instead of leaking its server-side handle.
func TestCacheCollisionEvictionFailureReleasesNewStatement(t *testing.T) {
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	fault := testutil.NewFaultConn(client, testutil.Faults{WriteFailAt: 1})
	cfg := config.Config{Session: config.SessionOptions{Charset: "utf8mb4"}, Limits: config.Limits{MaxMessageBytes: 4096}, Execution: config.ExecutionOptions{StatementCacheSize: 4, StatementCacheSQLLimit: 1024}}
	s := session.New(cfg, &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: config.ModeMySQL})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	ctx := context.Background()

	var cache Cache
	previous := Adopt(op, 11, sqlPlan("SELECT 1"), nil)
	previous.automatic = true
	if err = cache.Recycle(ctx, op, previous, true); err != nil {
		t.Fatal(err)
	}
	replacement := Adopt(op, 12, sqlPlan("SELECT 1"), nil)
	replacement.automatic = true
	if err = cache.Recycle(ctx, op, replacement, true); !errors.Is(err, testutil.ErrInjected) {
		t.Fatalf("eviction failure not reported: %v", err)
	}
	if !replacement.closed || !replacement.closeRequested {
		t.Fatalf("new statement leaked: closed=%v requested=%v", replacement.closed, replacement.closeRequested)
	}
	if len(cache.entries) != 0 || cache.order.Len() != 0 {
		t.Fatal("failed eviction left cache state behind")
	}
}
