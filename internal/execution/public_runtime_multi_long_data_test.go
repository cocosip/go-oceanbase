package execution_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestClientMultiStatementLongData(t *testing.T) {
	stream := &oceanbase.LongData{Reader: bytes.NewReader([]byte("abc")), Length: 3, Text: true}
	db := publictest.ConfiguredDB(t, config.Config{PrepareMode: config.PrepareClient, AllowMultiStatements: true}, func(ctx context.Context, p *wire.PacketIO) {
		p.ResetSequence()
		data, err := p.ReadMessage(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		got := string(data)
		if got != "\x03SELECT  _utf8mb4 X'616263' ; SELECT 7" {
			t.Errorf("query got %q", got)
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "SELECT ?; SELECT 7", stream); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "SELECT ?; SELECT 7", stream); !errors.Is(err, oceanbase.ErrLongData) {
		t.Fatalf("reused stream accepted: %v", err)
	}
}

func TestClientMultiStatementLongDataRequiresExplicitCapability(t *testing.T) {
	db := publictest.ConfiguredDB(t, config.Config{PrepareMode: config.PrepareClient}, func(_ context.Context, p *wire.PacketIO) {
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	stream := &oceanbase.LongData{Reader: bytes.NewReader([]byte("abc")), Length: 3, Text: true}
	if _, err := db.ExecContext(context.Background(), "SELECT ?; SELECT 7", stream); !errors.Is(err, oceanbase.ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
}
