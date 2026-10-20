package execution_test

import (
	"context"
	"errors"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestResourceCloseAfterCanceledQuery(t *testing.T) {
	started := make(chan struct{})
	db := publictest.QueryVersionDB(t, "4.3.5.1", func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT id FROM t", 7, 0, nil) {
			return
		}
		execute, err := wire.EncodeStmtExecute(7, nil, 4096)
		if err != nil {
			t.Error(err)
			return
		}
		execute[5] = 1
		if !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0}) || !testpeer.ReadQuery(ctx, t, p, "slow") {
			return
		}
		close(started)
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	cur, err := oceanbase.OpenCursor(t.Context(), pinned, "SELECT id FROM t", oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { <-started; cancel() }()
	_, err = pinned.QueryContext(ctx, "slow")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("query did not cancel", err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal("closing a resource after physical cancellation must stay idempotent", err)
	}
}
