package call_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexOutputCloseDeadline(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("BEGIN p(?); END;"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p,
			publictest.CombinedHeader(24, 1, 1, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0},
			testpeer.TextColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 1, 0, 0, 0, 0, 0, 0, 0}, []byte{254, 0, 0, 6, 0},
			[]byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 16}, []byte{0, 0, 0, 1, '7'}, []byte{254, 0, 0, 6, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 24)) {
			return
		}
		p.ResetSequence()
		if _, err = p.ReadMessage(ctx); err != nil {
			t.Error("missing descriptor query", err)
			return
		}
		// Leave the descriptor query unanswered until the client closes its transport.
		peerCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		_, _ = p.ReadMessage(peerCtx)
	}, func(cfg *config.Config) {
		cfg.Limits.CleanupTimeout = 50 * time.Millisecond
		cfg.Limits.MaxMessageBytes = 1 << 16
	})
	defer func() { _ = c.Close() }()
	v := oceanbase.Object{Type: testComplexUnchanged}
	rows, err := c.QueryContext(context.Background(), "BEGIN p(?); END;", []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &v}}})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = rows.Close()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second || c.IsValid() || v.Type != testComplexUnchanged {
		t.Fatal(err, time.Since(started), c.IsValid(), v)
	}
}
