package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestResetManagedBoundary(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 1}}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 1) {
			return
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 0}, {testAutocommitOn, 2}}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.ExecContext(context.Background(), testAutocommitOff); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal("pinned connection was reset", err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(context.Background()); err != nil {
		t.Fatal("dirty pool connection not reset", err)
	}
}
func TestResetGeneration(t *testing.T) {
	var sessions atomic.Uint32
	connector, err := NewConnector(Config{User: testUser, ResetMode: ResetFull, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		id := sessions.Add(1)
		if !servePrepare(ctx, t, p, selectBindSQL, id, 1, nil) {
			return
		}
		if id == 1 {
			p.ResetSequence()
			if _, err := p.ReadMessage(ctx); err == nil {
				t.Error("unverified full reset kept old session")
			}
			return
		}
		payload := append(executeHeader(id), 0, 1, 8, 0)
		payload = binary.LittleEndian.AppendUint64(payload, 1)
		if !serveOK(ctx, t, p, payload, 2) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, id)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), selectBindSQL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ExecContext(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if sessions.Load() != 2 {
		t.Fatal("statement did not reprepare on replacement session", sessions.Load())
	}
}

func TestResetFailure(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 1}}) {
			return
		}
		if !readQuery(ctx, t, p, testRollbackSQL) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("failed reset reused session")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if _, err = raw.(driver.ExecerContext).ExecContext(context.Background(), testAutocommitOff, nil); err != nil {
		t.Fatal(err)
	}
	if err = raw.(driver.SessionResetter).ResetSession(context.Background()); !errors.Is(err, driver.ErrBadConn) || raw.(driver.Validator).IsValid() {
		t.Fatal(err)
	}
}
