package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func activeRowsConn(t *testing.T) (*conn, driver.Rows, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	peerClosed := make(chan struct{})
	cfg := Config{User: testUser, Limits: Limits{CleanupTimeout: 20 * time.Millisecond}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		defer close(peerClosed)
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !readQuery(context.Background(), t, p, "SELECT waiting") {
			return
		}
		if !sendPackets(context.Background(), t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
			return
		}
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Error("active response was interleaved")
		}
	})}
	c, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := c.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r, err := dc.(*conn).QueryContext(ctx, "SELECT waiting", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = dc.(*conn).session.raw.Close(); _ = r.Close(); _ = dc.Close() })
	return dc.(*conn), r, cancel, peerClosed
}

func TestCancelRowsLifetime(t *testing.T) {
	c, rows, cancel, peerClosed := activeRowsConn(t)
	cancel()
	select {
	case <-peerClosed:
	case <-time.After(200 * time.Millisecond):
		t.Error("idle Rows context cancellation did not close transport")
	}
	if err := rows.Close(); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.IsValid() {
		t.Error("cancelled response can return to pool")
	}
}

func TestCloseBudget(t *testing.T) {
	c, _, _, _ := activeRowsConn(t)
	done := make(chan error, 1)
	go func() { done <- c.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Conn.Close waited indefinitely for active Rows")
	}
}

func TestCancelWaitingForSession(t *testing.T) {
	c, _, _, _ := activeRowsConn(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Ping(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Error(err)
		}
		if !c.IsValid() {
			t.Error("cancelled waiter poisoned active operation")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("waiting for session ignored context")
	}
}

func TestCancelCompletionRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := queryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
		if !readQuery(peerCtx, t, p, "SELECT 1") {
			return
		}
		if !sendPackets(peerCtx, t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(peerCtx); err != nil || len(data) != 1 || data[0] != 14 {
			t.Error(data, err)
			return
		}
		// The old request is cancelled after a new command has started.
		cancel()
		if !sendPackets(peerCtx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	r, err := db.QueryContext(ctx, "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
	}
	if err = r.Err(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatalf("old cancellation hit next command: %v", err)
	}
}
