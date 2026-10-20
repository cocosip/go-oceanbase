package cursor

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const cursorQuery = "SELECT id FROM cursor_fixture"

func cursorOperation(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) (context.Context, *session.Operation) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	client, peer := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close() })
	cfg, err := config.Normalize(config.Config{User: "app", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	s := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: mode, OBVersion: "4.3.0", Capabilities: wire.ClientProtocol41, OK: wire.OK{Status: 2}})
	op, err := s.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	done := make(chan struct{})
	go func() { defer close(done); serve(ctx, wire.NewPacketIO(testpeer.AuthPeerConn{Conn: peer}, 0)) }()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close(); <-done })
	return ctx, op
}

func preparePeer(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id uint32) bool {
	t.Helper()
	if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
		return false
	}
	header := binary.LittleEndian.AppendUint32([]byte{0}, id)
	return testpeer.SendPackets(ctx, t, p, append(header, make([]byte, 7)...))
}

func openPeer(ctx context.Context, t *testing.T, p *wire.PacketIO, id uint32) bool {
	t.Helper()
	payload := binary.LittleEndian.AppendUint32([]byte{23}, id)
	payload = append(payload, 1, 1, 0, 0, 0)
	return testpeer.ReadCommand(ctx, t, p, payload) && testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 66, 0})
}

func closeOwned(ctx context.Context, t *testing.T, op *session.Operation, m *Manager, ref session.ResourceRef) {
	t.Helper()
	lease, err := m.Close(ctx, op, ref)
	if err = errors.Join(err, lease.Release(ctx, op)); err != nil {
		t.Fatal(err)
	}
	op.Forget(ref)
}

func TestManagerForwardCursor(t *testing.T) {
	ctx, op := cursorOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !preparePeer(ctx, t, p, cursorQuery, 51) || !openPeer(ctx, t, p, 51) {
			return
		}
		fetch := binary.LittleEndian.AppendUint32([]byte{28, 51, 0, 0, 0}, 2)
		if !testpeer.ReadCommand(ctx, t, p, fetch) || !testpeer.SendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), binary.LittleEndian.AppendUint64([]byte{0, 0}, 8), []byte{254, 0, 0, 130, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, []byte{25, 51, 0, 0, 0}) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
	})
	var manager Manager
	ref, err := manager.Open(ctx, op, nil, cursorQuery, nil, Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if published, e := op.Published(ref); e != nil || published {
		t.Fatal("owner published before output commit", published, e)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	var busy BusyToken
	if err = busy.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	rows, retire, err := manager.Fetch(ctx, op, nil, ref, Position{}, 2, &busy)
	if err != nil || retire {
		t.Fatal(retire, err)
	}
	if err = busy.Begin(ctx); !errors.Is(err, dberrors.ErrCursorBusy) {
		t.Fatal("batch released busy token early", err)
	}
	for _, want := range []int64{7, 8} {
		v := make([]driver.Value, 1)
		if err = rows.Next(v); err != nil || v[0] != want {
			t.Fatal(v, want, err)
		}
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = busy.Begin(ctx); err != nil {
		t.Fatal("batch close retained token", err)
	}
	rows, retire, err = manager.Fetch(ctx, op, nil, ref, Position{}, 1, &busy)
	if err != nil || retire || rows.Next(make([]driver.Value, 1)) != io.EOF {
		t.Fatal("exhausted cursor fetched again", retire, err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	closeOwned(ctx, t, op, &manager, ref)
	if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
		t.Fatal(err)
	}
}

func TestManagerStaticCursor(t *testing.T) {
	for _, limit := range []int64{0, 180} {
		t.Run(map[bool]string{false: "scroll", true: "budget"}[limit != 0], func(t *testing.T) {
			ctx, op := cursorOperation(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !preparePeer(ctx, t, p, cursorQuery, 52) || !openPeer(ctx, t, p, 52) {
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, []byte{28, 52, 0, 0, 0, 128, 0, 0, 0}) {
					return
				}
				for i := uint64(1); i <= 3; i++ {
					if !testpeer.SendPackets(ctx, t, p, binary.LittleEndian.AppendUint64([]byte{0, 0}, i)) {
						return
					}
				}
				if testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 130, 0}) {
					testpeer.ReadCommand(ctx, t, p, []byte{25, 52, 0, 0, 0})
				}
			})
			var manager Manager
			ref, err := manager.Open(ctx, op, nil, cursorQuery, nil, Options{Mode: Static, MaxCacheBytes: limit}, false)
			if limit != 0 {
				if !errors.Is(err, wire.ErrMessageTooLarge) || len(op.Resources(true)) != 0 {
					t.Fatal("failed open leaked resource", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = op.Publish(ref); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				pos   Position
				count int
				want  []int64
			}{{Position{Absolute, 2}, 1, []int64{2}}, {Position{Relative, -1}, 2, []int64{1, 2}}, {Position{Absolute, 0}, 1, nil}, {Position{}, 1, []int64{1}}, {Position{Absolute, 99}, 1, nil}} {
				rows, retire, e := manager.Fetch(ctx, op, nil, ref, tc.pos, tc.count, nil)
				if e != nil || retire {
					t.Fatal(retire, e)
				}
				var got []int64
				for {
					v := make([]driver.Value, 1)
					e = rows.Next(v)
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					got = append(got, v[0].(int64))
				}
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatal(got, tc.want)
				}
				if e = rows.Close(); e != nil {
					t.Fatal(e)
				}
			}
			closeOwned(ctx, t, op, &manager, ref)
		})
	}
}

func TestManagerRefCursorLease(t *testing.T) {
	ctx, op := cursorOperation(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, []byte{28, 9, 0, 0, 0, 1, 0, 0, 0}) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 134, 0}) {
			return
		}
		if testpeer.ReadCommand(ctx, t, p, []byte{25, 9, 0, 0, 0}) {
			testpeer.ReadCommand(ctx, t, p, []byte{25, 7, 0, 0, 0})
		}
	})
	var manager Manager
	parent := statement.Adopt(op, 7, sqltext.Plan{}, nil)
	ref, err := manager.OwnRef(op, parent, 9, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Close(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	rows, retire, err := manager.Fetch(ctx, op, nil, ref, Position{}, 1, nil)
	if err != nil || retire {
		t.Fatal(retire, err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	closeOwned(ctx, t, op, &manager, ref)
}
