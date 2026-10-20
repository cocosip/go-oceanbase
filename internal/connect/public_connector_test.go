package connect_test

import (
	"bytes"
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPingFailure(t *testing.T) {
	var pingSeen atomic.Bool
	cfg := config.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(context.Background()); err != nil || !bytes.Equal(data, []byte{14}) {
			t.Errorf("missing ping: %x %v", data, err)
			return
		}
		// Returning closes the transport before any Ping response.
		pingSeen.Store(true)
	})}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	t.Cleanup(func() { _ = db.Close() })
	if err = db.PingContext(context.Background()); err == nil {
		t.Fatal("Ping silently succeeded")
	}
	if !pingSeen.Load() {
		t.Fatal("Ping failed before sending Ping command")
	}
	if db.Stats().Idle != 0 {
		t.Fatal("failed physical connection returned to idle pool")
	}
}

func TestConnectInitializationAndPing(t *testing.T) {
	cfg := config.Config{User: testUser, Session: config.SessionOptions{Variables: map[string]string{"time_zone": testUTCOffset}, InitSQL: []string{readySQL}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		for _, sql := range []string{"SET SESSION time_zone = '+00:00'", readySQL, "SET NAMES utf8mb4", "SET SESSION time_zone = '+00:00'"} {
			if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, sql...), 2) {
				return
			}
		}
		if !testpeer.ServeOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConnectIntegerSystemVariable(t *testing.T) {
	cfg := config.Config{User: testUser, Session: config.SessionOptions{Variables: map[string]string{testSessionVariable: "600"}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) ||
			!sessionScalarTyped(ctx, t, p, testReadSessionVariable, 2, 8, "28800") ||
			!testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = 600"), 2) ||
			!testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), 2) ||
			!testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) ||
			!testpeer.ServeOK(context.Background(), t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}
