package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func protocolEngine(t *testing.T, mode config.Mode, serve func(context.Context, *wire.PacketIO)) (context.Context, *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, server := net.Pipe()
	t.Cleanup(func() { cancel(); _ = client.Close(); _ = server.Close() })
	cfg, err := config.Normalize(config.Config{Mode: mode, User: "app"})
	if err != nil {
		t.Fatal(err)
	}
	p := &session.Negotiated{Raw: client, Packets: wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes), Mode: mode, OBVersion: "4.3.5", OK: wire.OK{Status: 2}}
	go serve(ctx, wire.NewPacketIO(server, cfg.Limits.MaxMessageBytes))
	return ctx, New(nil, session.New(cfg, p))
}

func TestQueryServerErrorKeepsSession(t *testing.T) {
	ctx, e := protocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, []byte("\x03bad")) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'b', 'a', 'd'}) {
			return
		}
		if testpeer.ReadCommand(ctx, t, p, []byte{14}) {
			testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0})
		}
	})
	recorder := new(Recorder)
	_, err := e.Query(ctx, "bad", nil, Options{Recorder: recorder})
	var server *dberrors.Error
	if !errors.As(err, &server) {
		t.Fatal(err)
	}
	snapshot := recorder.Snapshot()
	if !snapshot.Done || len(snapshot.Results) != 1 || snapshot.Results[0].Kind != ResultFailure {
		t.Fatal(snapshot)
	}
	if err = e.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQueryRecordsRowsAndFollowingCommand(t *testing.T) {
	ctx, e := protocolEngine(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if testpeer.ReadCommand(ctx, t, p, []byte("\x03select 42;update t set v=1")) {
			testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2'}, []byte{254, 0, 0, 10, 0}, []byte{0, 1, 0, 2, 0, 0, 0})
		}
	})
	recorder := new(Recorder)
	rows, err := e.Query(ctx, "select 42;update t set v=1", nil, Options{Recorder: recorder})
	if err != nil {
		t.Fatal(err)
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil || values[0] != "42" {
		t.Fatal(values, err)
	}
	if err = rows.Next(values); err != io.EOF {
		t.Fatal(err)
	}
	if err = rows.NextResultSet(); err != io.EOF {
		t.Fatal(err)
	}
	snapshot := recorder.Snapshot()
	if !snapshot.Done || snapshot.Err != nil || len(snapshot.Results) != 2 || !snapshot.Results[0].Complete || snapshot.Results[1].RowsAffected != 1 {
		t.Fatal(snapshot)
	}
}
