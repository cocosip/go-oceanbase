package statement

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func protocolOperation(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) (context.Context, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	cfg, err := config.Normalize(config.Config{User: "app", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: mode, OBVersion: testNativeVersion, Capabilities: wire.ClientProtocol41})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	go serve(ctx, wire.NewPacketIO(testpeer.AuthPeerConn{Conn: peer}, 0))
	return ctx, op
}

func TestUploadLongSharedSource(t *testing.T) {
	ctx, op := protocolOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		for _, index := range []uint16{0, 2} {
			want := binary.LittleEndian.AppendUint32([]byte{24}, 7)
			want = binary.LittleEndian.AppendUint16(want, index)
			if !testpeer.ReadCommand(ctx, t, p, append(want, "abc"...)) {
				return
			}
		}
	})
	state := &atomic.Bool{}
	a := &streaminput.Source{Reader: strings.NewReader("abc"), Length: 3, State: state}
	b := *a
	if err := UploadLongData(ctx, op, 7, []any{a, int64(1), &b}); err != nil {
		t.Fatal(err)
	}
	if !state.Load() || op.ResponseActive() || op.Closed() {
		t.Fatal("upload ownership", state.Load(), op.ResponseActive(), op.Closed())
	}
	if err := UploadLongData(ctx, op, 7, []any{a}); !errors.Is(err, dberrors.ErrLongData) || op.Closed() {
		t.Fatal("prevalidation side effect", err)
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestUploadLongNoReplayAndPrevalidation(t *testing.T) {
	op := testOperation(t, config.Config{Limits: config.Limits{MaxLOBBytes: 1024}}, config.ModeMySQL, "")
	a := &streaminput.Source{Reader: errorReader{err: driver.ErrBadConn}, Length: -1, State: &atomic.Bool{}}
	invalid := &streaminput.Source{Reader: strings.NewReader("x"), Length: 2, MaxBytes: 1, State: &atomic.Bool{}}
	if err := UploadLongData(context.Background(), op, 7, []any{a, invalid}); !errors.Is(err, wire.ErrMessageTooLarge) || a.State.Load() || op.Closed() {
		t.Fatal("prevalidation consumed source", err)
	}
	err := UploadLongData(context.Background(), op, 7, []any{a})
	if !errors.Is(err, dberrors.ErrLongData) || errors.Is(err, driver.ErrBadConn) || !op.Closed() || !a.State.Load() {
		t.Fatal("reader error could replay", err)
	}
}

func TestStatementCacheOwnership(t *testing.T) {
	op := testOperation(t, config.Config{Execution: config.ExecutionOptions{StatementCacheSize: 1, StatementCacheSQLLimit: 1024}}, config.ModeMySQL, "")
	ctx := context.Background()
	var cache Cache
	a, err := PrepareAutomatic(ctx, op, "SELECT 1", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Recycle(ctx, op, a, true); err != nil {
		t.Fatal(err)
	}
	if s, e := cache.Take(ctx, op, "SELECT 1"); e != nil || s != a {
		t.Fatal(s, e)
	}
	if s, e := cache.Take(ctx, op, "SELECT 1"); e != nil || s != nil {
		t.Fatal("checked-out statement remained cached", s, e)
	}
	if err = cache.Recycle(ctx, op, a, true); err != nil {
		t.Fatal(err)
	}
	b, err := PrepareAutomatic(ctx, op, "SELECT 2", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = cache.Recycle(ctx, op, b, true); err != nil || !a.closed {
		t.Fatal("oldest not evicted", err)
	}
	op.SetOK(wire.OK{Status: 512})
	if s, e := cache.Take(ctx, op, "SELECT 2"); e != nil || s != nil || !b.closed {
		t.Fatal("escaping mismatch reused statement", s, e)
	}
	for _, query := range []string{"SET x=1", "SELECT 1;", "COMMIT", "SELECT 1; SELECT 2"} {
		if CacheableSQL(op, query) {
			t.Fatal("unsafe cache key", query)
		}
	}
}
