package execution_test

import (
	"context"
	"errors"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestUnsupportedPrepareFallback(t *testing.T) {
	db := publictest.ConfiguredDB(t, config.Config{EmulateUnsupportedPstmts: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, "SELECT ?"...)) || !testpeer.SendPackets(ctx, t, p, prepareError(1295, "unsupported")) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT  7 "), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), "SELECT ?", 7); err != nil {
		t.Fatal(err)
	}
}

func TestUnsupportedPrepareFallbackDisabled(t *testing.T) {
	db := publictest.ConfiguredDB(t, config.Config{}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, "SELECT ?"...)) || !testpeer.SendPackets(ctx, t, p, prepareError(1295, "unsupported")) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var serverErr *oceanbase.Error
	if _, err := db.ExecContext(context.Background(), "SELECT ?", 7); !errors.As(err, &serverErr) || serverErr.Code != 1295 {
		t.Fatalf("got %v", err)
	}
}

func TestUnsupportedPrepareFallbackOnlyMatches1295(t *testing.T) {
	db := publictest.ConfiguredDB(t, config.Config{EmulateUnsupportedPstmts: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, "SELECT ?"...)) || !testpeer.SendPackets(ctx, t, p, prepareError(1064, "syntax")) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var serverErr *oceanbase.Error
	if _, err := db.ExecContext(context.Background(), "SELECT ?", 7); !errors.As(err, &serverErr) || serverErr.Code != 1064 {
		t.Fatalf("got %v", err)
	}
}

func prepareError(code uint16, message string) []byte {
	return append([]byte{255, byte(code), byte(code >> 8), '#', 'H', 'Y', '0', '0', '0'}, message...)
}
