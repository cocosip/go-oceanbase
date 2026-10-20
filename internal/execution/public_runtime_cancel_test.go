package execution_test

import (
	context "context"
	errors "errors"
	testing "testing"
	time "time"
	_ "time/tzdata"

	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCancelCompletionRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := publictest.QueryDB(t, func(peerCtx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(peerCtx, t, p, "SELECT 1") {
			return
		}
		if !testpeer.SendPackets(peerCtx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(peerCtx); err != nil || len(data) != 1 || data[0] != 14 {
			t.Error(data, err)
			return
		}
		// The old request is cancelled after a new command has started.
		cancel()
		if !testpeer.SendPackets(peerCtx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
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
