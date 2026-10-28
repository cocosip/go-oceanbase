package command

import (
	"context"
	"errors"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func responseOperation(t *testing.T, packets [][]byte) (context.Context, *session.Session, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	client, peer := net.Pipe()
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	cfg, err := config.Normalize(config.Config{User: "app", Addr: "127.0.0.1:2881", Mode: config.ModeMySQL})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: config.ModeMySQL, Capabilities: wire.ClientProtocol41})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	go func() {
		p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: peer}, 0)
		if !testpeer.ReadCommand(ctx, t, p, []byte{3, 'q'}) {
			return
		}
		testpeer.SendPackets(ctx, t, p, packets...)
	}()
	return ctx, s, op
}

func TestQueryScalarsShape(t *testing.T) {
	column := testpeer.TextColumn("v", 253, 45, 0)
	eof := []byte{254, 0, 0, 2, 0}
	for _, tc := range []struct {
		name    string
		packets [][]byte
		valid   bool
	}{
		{"single", [][]byte{{1}, column, eof, {1, 'x'}, eof}, true},
		{"no rowset", [][]byte{{0, 0, 0, 2, 0, 0, 0}}, false},
		{"empty", [][]byte{{1}, column, eof, eof}, false},
		{"second row", [][]byte{{1}, column, eof, {1, 'x'}, {1, 'y'}}, false},
		{"extra result", [][]byte{{1}, column, eof, {1, 'x'}, {254, 0, 0, 10, 0}}, false},
		{"column mismatch", [][]byte{{2}, column, column, eof}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, s, op := responseOperation(t, tc.packets)
			values, _, err := QueryScalars(ctx, op, []byte{'q'}, 1)
			if tc.valid {
				if err != nil || len(values) != 1 || !values[0].Valid || values[0].String != "x" || op.ResponseActive() || s.Closed() {
					t.Fatalf("values=%v err=%v active=%v closed=%v", values, err, op.ResponseActive(), s.Closed())
				}
			} else if !errors.Is(err, wire.ErrMalformed) || !s.Closed() {
				t.Fatalf("err=%v closed=%v", err, s.Closed())
			}
		})
	}
}

func TestRawResponseBoundaryAndServerError(t *testing.T) {
	ctx, s, op := responseOperation(t, [][]byte{{255, 0x15, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}})
	r, err := Start(ctx, op, []byte{3, 'q'}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Advance(ctx)
	var server *dberrors.Error
	if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "42000" || server.Message != "no" || s.Closed() || op.ResponseActive() {
		t.Fatalf("err=%v closed=%v active=%v", err, s.Closed(), op.ResponseActive())
	}
}

func TestResultUint64(t *testing.T) {
	r := Result{Affected: math.MaxUint64, ID: math.MaxUint64, Known: true}
	if _, err := r.RowsAffected(); !errors.Is(err, ErrCountOverflow) {
		t.Fatal(err)
	}
	if _, err := r.LastInsertId(); !errors.Is(err, ErrCountOverflow) {
		t.Fatal(err)
	}
	if r.ID != math.MaxUint64 {
		t.Fatal("native ID truncated")
	}
}

func TestRawResponseMalformedServerError(t *testing.T) {
	ctx, s, op := responseOperation(t, [][]byte{{255, 0x15, 4, '#', '4', '2'}})
	r, err := Start(ctx, op, []byte{3, 'q'}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Advance(ctx); !errors.Is(err, wire.ErrMalformed) || !s.Closed() || op.ResponseActive() {
		t.Fatalf("err=%v closed=%v active=%v", err, s.Closed(), op.ResponseActive())
	}
}

func TestRawResponseDoesNotReleaseOperation(t *testing.T) {
	ctx, s, op := responseOperation(t, [][]byte{{1}, testpeer.TextColumn("v", 253, 45, 0), {254, 0, 0, 2, 0}, {1, 'x'}, {254, 0, 0, 2, 0}})
	r, err := Start(ctx, op, []byte{3, 'q'}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Advance(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.NextFields(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = r.NextFields(ctx); err != io.EOF {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err = s.Acquire(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("raw response released operation: %v", err)
	}
}
