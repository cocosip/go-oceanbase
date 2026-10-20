package execution_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func metadataStreamingPeer(t *testing.T, p *wire.PacketIO, rows int, tailError bool) {
	t.Helper()
	ctx := context.Background()
	p.ResetSequence()
	request, err := p.ReadMessage(ctx)
	if err != nil || len(request) == 0 || request[0] != 3 {
		t.Error("missing public metadata query", err)
		return
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("catalog", 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
		return
	}
	value := strings.Repeat("x", 64)
	for range rows {
		if err = p.WriteMessage(ctx, append([]byte{byte(len(value))}, value...)); err != nil {
			return
		}
	}
	tail := []byte{254, 0, 0, 2, 0}
	if tailError {
		tail = []byte("\xff\x28\x04#42000metadata tail failed")
	}
	if err = p.WriteMessage(ctx, tail); err != nil {
		return
	}
	p.ResetSequence()
	request, err = p.ReadMessage(ctx)
	if err != nil {
		return
	}
	// Permit cleanup after the regression assertion fails before its expected Ping.
	if len(request) == 1 && request[0] == 1 {
		return
	}
	if len(request) != 1 || request[0] != 14 {
		t.Errorf("unexpected post-metadata command: %x", request)
		return
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
		return
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func TestPublicMetadataDefersTailServerError(t *testing.T) {
	db := publictest.QueryVersionDB(t, "4.3.5.1", func(_ context.Context, p *wire.PacketIO) { metadataStreamingPeer(t, p, 1, true) })
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	rows, err := oceanbase.Metadata(t.Context(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataCatalogs})
	if err != nil {
		t.Fatalf("metadata must return the first row before its tail error: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var value string
	if !rows.Next() || rows.Scan(&value) != nil || value != strings.Repeat("x", 64) {
		t.Fatal("first metadata row was lost", value, rows.Err())
	}
	if rows.Next() {
		t.Fatal("error packet became a metadata row")
	}
	var server *oceanbase.Error
	if !errors.As(rows.Err(), &server) || server.Code != 1064 {
		t.Fatal("tail server error did not reach Rows.Err", rows.Err())
	}
	if err = pinned.PingContext(t.Context()); err != nil {
		t.Fatal("complete server error lost connection boundary", err)
	}
}

func TestPublicMetadataStreamsBeyondMessageLimit(t *testing.T) {
	const count = 64
	db := publictest.QueryVersionDB(t, "4.3.5.1", func(_ context.Context, p *wire.PacketIO) { metadataStreamingPeer(t, p, count, false) }, func(cfg *config.Config) { cfg.Limits.MaxMessageBytes = 1024 })
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	rows, err := oceanbase.Metadata(t.Context(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataCatalogs})
	if err != nil {
		t.Fatalf("individual metadata packets fit the message limit: %v", err)
	}
	defer func() { _ = rows.Close() }()
	got := 0
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil || value != strings.Repeat("x", 64) {
			t.Fatal(value, err)
		}
		got++
	}
	if got != count || rows.Err() != nil {
		t.Fatalf("got %d rows, want %d: %v", got, count, rows.Err())
	}
	if err = pinned.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
