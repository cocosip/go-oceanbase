package execution

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
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func rowsProtocolEngine(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) (context.Context, *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	client, peer := net.Pipe()
	cfg, err := config.Normalize(config.Config{User: "app", Mode: mode})
	if err != nil {
		t.Fatal(err)
	}
	physical := session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, 0), Mode: mode, Capabilities: wire.ClientProtocol41 | wire.ClientLocalFiles, OK: wire.OK{Status: 2}})
	engine := New(nil, physical)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, wire.NewPacketIO(testpeer.AuthPeerConn{Conn: peer}, 0))
	}()
	t.Cleanup(func() { _ = client.Close(); _ = peer.Close(); <-done })
	return ctx, engine
}

func TestRowsMixedResults(t *testing.T) {
	ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, []byte("\x03mixed")) || !testpeer.SendPackets(ctx, t, p,
			[]byte{0, 0, 0, 10, 0, 3, 0}, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 10, 0}, []byte{1, '7'}, []byte{254, 4, 0, 10, 0}, []byte{0, 2, 9, 2, 0, 5, 0}) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
	})
	var recorder Recorder
	var attempt Attempt
	rows, err := engine.Query(ctx, "mixed", nil, Options{Recorder: &recorder, Attempt: &attempt})
	if err != nil {
		t.Fatal(err)
	}
	if got := recorder.Snapshot(); got.Done || len(got.Results) != 2 || got.Results[0].Warnings.Count != 3 {
		t.Fatal(got)
	}
	value := make([]driver.Value, 1)
	if err = rows.Next(value); err != nil || value[0] != int64(7) {
		t.Fatal(value, err)
	}
	if err = rows.Next(value); err != io.EOF || !rows.HasNextResultSet() {
		t.Fatal(err)
	}
	if err = rows.NextResultSet(); err != io.EOF {
		t.Fatal(err)
	}
	if got := recorder.Snapshot(); !got.Done || len(got.Results) != 3 || !got.Results[1].Complete || got.Results[1].Warnings.Count != 4 || got.Results[2].RowsAffected != 2 {
		t.Fatal(got)
	}
	if !attempt.Started || !attempt.Acknowledged || attempt.Result.Affected != 2 || rows.Last().ID != 9 {
		t.Fatal(attempt, rows.Last())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err = engine.Ping(ctx); err != nil {
		t.Fatal("final result retained operation", err)
	}
}

func TestRowsServerErrorPreservesSession(t *testing.T) {
	for _, midRows := range []bool{false, true} {
		t.Run(map[bool]string{false: "header", true: "row"}[midRows], func(t *testing.T) {
			ctx, engine := rowsProtocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, []byte("\x03failure")) {
					return
				}
				if midRows && !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, 2, 0}) {
					return
				}
				if testpeer.SendPackets(ctx, t, p, []byte("\xff\x28\x04#42000statement failed")) {
					testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
				}
			})
			var recorder Recorder
			rows, err := engine.Query(ctx, "failure", nil, Options{Recorder: &recorder})
			if midRows {
				if err != nil {
					t.Fatal(err)
				}
				err = rows.Next(make([]driver.Value, 1))
			}
			var server *dberrors.Error
			if !errors.As(err, &server) || server.Code != 1064 || !engine.Valid() {
				t.Fatal(err, engine.Valid())
			}
			got := recorder.Snapshot()
			if !got.Done || !errors.As(got.Err, &server) || len(got.Results) != 1 || got.Results[0].Complete == midRows {
				t.Fatal(got)
			}
			if err = engine.Ping(ctx); err != nil {
				t.Fatal("server error lost boundary or operation", err)
			}
		})
	}
}

func TestRowsBuffersResourcesBeforeRelease(t *testing.T) {
	ctx, engine := rowsProtocolEngine(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, []byte("\x03resources")) || !testpeer.SendPackets(ctx, t, p,
			[]byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 14, 0}, []byte{0, 0, 9, 0, 0, 0}, []byte{254, 0, 0, 14, 0},
			[]byte{1}, testpeer.TextColumn("tail", 253, 45, 0), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 1, 'x'}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, []byte{28, 9, 0, 0, 0, 128, 0, 0, 0}) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), []byte{254, 0, 0, 134, 0}) || !testpeer.ReadCommand(ctx, t, p, []byte{25, 9, 0, 0, 0}) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte{14}, 6)
	})
	op, err := engine.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := engine.start(ctx, op, []byte("\x03resources"), true, nil, nil, nil, Options{}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rows.released || rows.snapshot == nil {
		t.Fatal("resource rowset retained live response")
	}
	if err = engine.Ping(ctx); err != nil {
		t.Fatal("buffered resources retained operation", err)
	}
	value := make([]driver.Value, 1)
	if err = rows.Next(value); err != nil {
		t.Fatal(err)
	}
	child, ok := value[0].(driver.Rows)
	if !ok || child.Next(value) != nil || value[0] != int64(7) {
		t.Fatal(value)
	}
	if err = rows.NextResultSet(); err != nil || !reflect.DeepEqual(rows.Columns(), []string{"tail"}) {
		t.Fatal("buffered column metadata did not advance", rows.Columns(), err)
	}
	if err = rows.Next(value); err != nil || value[0] != "x" {
		t.Fatal(value, err)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}
