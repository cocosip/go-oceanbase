package command

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// closeTestConfig only satisfies configuration validation; peers are in memory.
func closeTestConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: "app", Addr: "127.0.0.1:2881", CompatibilityMode: config.CompatibilityModeMySQL})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// closeTestPipedOperation returns an operation whose transport is a peer that
// never reads, so a write only completes when its deadline allows it.
func closeTestPipedOperation(t *testing.T) *session.Operation {
	t.Helper()
	cfg := closeTestConfig(t)
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: config.CompatibilityModeMySQL, Capabilities: wire.ClientProtocol41})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

// A close that never reached the socket leaves the server handle unowned: the
// caller already dropped its bookkeeping, so the session must not be reused.
func TestCloseStatementPreSendFailureMarksSessionDirty(t *testing.T) {
	op := closeTestPipedOperation(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CloseStatement(ctx, op, 7); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error = %v, want context.Canceled", err)
	}
	if !op.Dirty() {
		t.Fatal("pre-send close left the server statement unowned without retiring the session")
	}
	if op.Closed() {
		t.Fatal("pre-send close must keep the transport usable")
	}
}

// Every other close failure still cannot prove the session is reusable.
func TestCloseStatementPostSendFailureDiscards(t *testing.T) {
	op := closeTestPipedOperation(t)
	// net.Pipe blocks the write until the peer reads, so the deadline expires
	// after the command was partially handed to the transport.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := CloseStatement(ctx, op, 7); err == nil {
		t.Fatal("blocked close reported success")
	}
	if !op.Closed() {
		t.Fatal("post-send close failure kept a possibly desynchronized session")
	}
}

// A released operation cannot send, and must not change session state.
func TestCloseStatementRefusesWithoutOwnership(t *testing.T) {
	cfg := closeTestConfig(t)
	s := session.New(cfg, &session.Negotiated{Mode: config.CompatibilityModeMySQL})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	op.Release()
	if err = CloseStatement(context.Background(), op, 7); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("released close = %v, want ErrBadConn", err)
	}
	if op.Dirty() || op.Closed() {
		t.Fatal("released close changed session state")
	}
}
