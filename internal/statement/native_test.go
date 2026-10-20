package statement

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func combinedHeader(id uint32, columns, params uint16, flags byte) []byte {
	b := binary.LittleEndian.AppendUint32([]byte{0}, id)
	b = binary.LittleEndian.AppendUint16(b, columns)
	b = binary.LittleEndian.AppendUint16(b, params)
	return append(b, 0, 0, 0, flags, 0, 0, 0, 1)
}

func TestNativeCallRawBoundary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		rows            int
		hidden, complex bool
	}{
		{"scalar", 1, true, false}, {"empty", 0, true, false}, {"second row", 2, true, false},
		{"ordinary rows are not output", 1, false, false}, {"deferred complex", 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query := "BEGIN ? := 1; END;"
			params := []wire.Parameter{{Type: 6}}
			want, err := wire.EncodePrepareExecute(0, []byte(query), params, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			closed := make(chan struct{})
			ctx, op := protocolOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				defer close(closed)
				if !testpeer.ReadCommand(ctx, t, p, want) {
					return
				}
				flags := byte(0)
				if tc.hidden {
					flags = 1
				}
				column := testpeer.TextColumn("v", 8, 63, 0)
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
				if tc.complex {
					column = append(testpeer.TextColumn("v", 160, 45, 0), 1, 'S', 1, 'T', 1)
					row = []byte{0, 0, 1, 0, 0, 2, '4', '2'}
				}
				packets := [][]byte{combinedHeader(71, 1, 1, flags), testpeer.TextColumn("p", 6, 63, 0), {254, 0, 0, 6, 0}, column, {254, 0, 0, 6, 0}}
				for range tc.rows {
					packets = append(packets, row)
				}
				packets = append(packets, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0})
				if testpeer.SendPackets(ctx, t, p, packets...) {
					testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint32([]byte{25}, 71))
				}
			})
			got, err := NativeCallRaw(ctx, op, query, params, 1)
			valid := tc.hidden && tc.rows == 1
			if valid {
				if err != nil || !got.Started || !got.Seen || len(got.Columns) != 1 || len(got.Rows) != 1 || got.ResultItems != 2 {
					t.Fatal(got, err)
				}
				if tc.complex {
					if got.Values != nil || got.Columns[0].ComplexShape != nil {
						t.Fatal("complex value was materialized", got)
					}
				} else if len(got.Values) != 1 || got.Values[0] != int64(42) {
					t.Fatal(got.Values)
				}
			} else if !errors.Is(err, dberrors.ErrOut) || !got.Started {
				t.Fatal(got, err)
			}
			if op.Closed() || op.ResponseActive() {
				t.Fatal("completed shape failure lost synchronization")
			}
			select {
			case <-closed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestNativeCallRawPreflight(t *testing.T) {
	op := testOperation(t, config.Config{}, config.ModeOracle, "2.2.74")
	got, err := NativeCallRaw(context.Background(), op, "q", nil, 1)
	if !errors.Is(err, dberrors.ErrUnsupported) || got.Started {
		t.Fatal(got, err)
	}
	op.SetVersion(testNativeVersion)
	if err = op.BeginResponse(); err != nil {
		t.Fatal(err)
	}
	got, err = NativeCallRaw(context.Background(), op, "q", nil, 1)
	if !errors.Is(err, dberrors.ErrUnsupported) || got.Started || !op.ResponseActive() {
		t.Fatal(got, err)
	}
	op.EndResponse()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err = NativeCallRaw(ctx, op, "q", nil, 1)
	if !errors.Is(err, context.Canceled) || got.Started || op.ResponseActive() {
		t.Fatal(got, err)
	}
}

func TestNativeCallRawStartedOnPartialWrite(t *testing.T) {
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	fault := testutil.NewFaultConn(client, testutil.Faults{MaxWrite: 2, WriteFailAt: 2})
	cfg, err := config.Normalize(config.Config{User: "app"})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: config.ModeOracle, OBVersion: testNativeVersion})
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	done := make(chan struct{})
	go func() { defer close(done); _, _ = io.Copy(io.Discard, peer) }()
	got, err := NativeCallRaw(context.Background(), op, "BEGIN ? := 1; END;", []wire.Parameter{{Type: 6}}, 1)
	if !errors.Is(err, testutil.ErrInjected) || !got.Started || !op.Closed() || op.ResponseActive() {
		t.Fatal(got, err)
	}
	<-done
}

func TestNativeCallRawWithoutRowsSequence(t *testing.T) {
	query := "BEGIN NULL; END;"
	want, err := wire.EncodePrepareExecute(0, []byte(query), nil, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	ctx, op := protocolOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, want) {
			return
		}
		header := combinedHeader(71, 0, 0, 0)
		header[16] = 0
		if !testpeer.SendPackets(ctx, t, p, header) {
			return
		}
		p.RestartResponseSequence()
		if testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 6, 0, 0, 0}) {
			testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint32([]byte{25}, 71))
		}
	})
	got, err := NativeCallRaw(ctx, op, query, nil, 0)
	if !errors.Is(err, dberrors.ErrOut) || !got.Started || got.Seen || got.ResultItems != 1 || op.Closed() || op.ResponseActive() {
		t.Fatal(got, err)
	}
}
