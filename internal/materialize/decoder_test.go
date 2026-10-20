package materialize

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func decoderOperation(t *testing.T) *session.Operation {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: "app", Mode: config.ModeOracle})
	if err != nil {
		t.Fatal(err)
	}
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	s := session.New(cfg, &session.Negotiated{Conn: client, Raw: client, Packets: wire.NewPacketIO(client, 0), Mode: config.ModeOracle})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return op
}

func TestBufferedLOBLengthOverflow(t *testing.T) {
	op := decoderOperation(t)
	raw := make([]byte, 36)
	copy(raw, []byte{'!', 'B', 'O', 'L', 2})
	raw[9] = 1
	binary.LittleEndian.PutUint64(raw[28:], math.MaxUint64)
	values := []driver.Value{raw}
	d := New(op, new(catalog.Resolver))
	if err := d.LOBValues(context.Background(), []wire.Column{{Type: 210, Charset: 63}}, values, math.MaxInt64); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestRowCapturesResourceValues(t *testing.T) {
	op := decoderOperation(t)
	raw := []byte{1, 2, 3}
	columns := []wire.Column{{Type: 160}, {Type: 210}, {Type: 163}}
	got, err := Row(op, columns, true, [][]byte{raw, raw, {42, 0, 0, 0}}, true)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 99
	if got[0].([]byte)[0] != 1 || got[1].([]byte)[0] != 1 || got[2] != RefCursorID(42) {
		t.Fatal(got)
	}
	if _, err = Row(op, columns, false, [][]byte{nil, nil, nil}, true); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err = Row(op, columns, true, [][]byte{nil, nil, {0, 0, 0, 0}}, true); !errors.Is(err, dberrors.ErrCursor) {
		t.Fatal(err)
	}
}

func TestDecodeSetWaitsForResponseAndRejectsExpiredEpoch(t *testing.T) {
	op := decoderOperation(t)
	d := New(op, new(catalog.Resolver))
	if err := op.BeginResponse(); err != nil {
		t.Fatal(err)
	}
	var used int64
	if err := d.DecodeSet(context.Background(), &Set{}, &used); !errors.Is(err, catalog.ErrResponseActive) {
		t.Fatal(err)
	}
	op.EndResponse()
	op.AdvanceTransaction()
	if err := d.DecodeSet(context.Background(), &Set{}, &used); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal(err)
	}
}
