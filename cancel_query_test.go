package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const cancelPermissionPath = "permission"

func TestCancelQueryIdentity(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(modeFromStatus(status)), func(t *testing.T) { testCancelQueryIdentity(t, status, false) })
	}
}

func testCancelQueryIdentity(t *testing.T, status byte, createDatabase bool) {
	t.Helper()
	started, killed := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var dials atomic.Int32
	cfg := Config{User: testUser, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		aux := dials.Add(1) > 1
		id := uint32(42)
		if aux {
			id++
		}
		if !serveCancelConnect(ctx, t, p, status, id) || !serveBaseline(ctx, t, p, status) {
			return
		}
		if !aux && createDatabase {
			if !serveOK(ctx, t, p, []byte(testCreateDatabaseQuery), status) || !serveOK(ctx, t, p, []byte(testCreateDatabaseSelect), status) {
				return
			}
		}
		if aux {
			if !serveOK(ctx, t, p, []byte("\x03KILL QUERY 42"), status) {
				return
			}
			close(killed)
		} else {
			if !readQuery(ctx, t, p, "SELECT waiting") {
				return
			}
			close(started)
			select {
			case <-killed:
			case <-ctx.Done():
				return
			}
			if !sendPackets(ctx, t, p, []byte{255, 37, 5, '#', '7', '0', '1', '0', '0', 'x'}) || !serveOK(ctx, t, p, []byte{14}, status) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	})}
	if createDatabase {
		cfg.Database, cfg.CreateDatabaseIfMissing = testCreateDatabaseName, true
	}
	configured, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	target, err := QueryTarget(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := pinned.ExecContext(ctx, "SELECT waiting"); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = CancelQuery(ctx, configured, target); err != nil {
		t.Fatal(err)
	}
	var server *Error
	if err = <-done; !errors.As(err, &server) || server.Code != 1317 {
		t.Fatalf("original query result=%v", err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, configured, target); !errors.Is(err, ErrResource) || dials.Load() != 2 {
		t.Fatalf("idle target dialed auxiliary=%d error=%v", dials.Load(), err)
	}
}

func modeFromStatus(status byte) Mode {
	if status&4 != 0 {
		return ModeOracle
	}
	return ModeMySQL
}

func TestCancelQueryValidation(t *testing.T) {
	if _, err := QueryTarget(context.Background(), nil); err == nil {
		t.Fatal("nil pinned connection accepted")
	}
	if err := CancelQuery(context.Background(), nil, CancelTarget{}); err == nil {
		t.Fatal("empty target accepted")
	}
}

func serveCancelConnect(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, id uint32) bool {
	t.Helper()
	greeting := authGreeting(authNative, false)
	binary.LittleEndian.PutUint32(greeting[bytes.IndexByte(greeting, 0)+1:], id)
	return sendPackets(ctx, t, p, greeting) && readAuthResponse(ctx, t, p) && sendPackets(ctx, t, p, []byte{0, 0, 0, status, 0, 0, 0})
}

func readAuthResponse(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return false
	}
	return true
}

func TestCancelQueryFailure(t *testing.T) {
	for _, name := range []string{cancelPermissionPath, "lost-ack", "bad-kill-response", "extra-results"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			started, finish := make(chan struct{}), make(chan struct{})
			var dials atomic.Int32
			configured, err := NewConnector(Config{User: testUser, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				aux := dials.Add(1) > 1
				id := uint32(42)
				if aux {
					id++
				}
				if !serveCancelConnect(ctx, t, p, 2, id) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				if aux {
					serveCancelFailure(ctx, t, p, name)
					return
				}
				if !readQuery(ctx, t, p, "SELECT waiting") {
					return
				}
				close(started)
				if name == cancelPermissionPath {
					select {
					case <-finish:
					case <-ctx.Done():
						return
					}
					if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				} else if _, e := p.ReadMessage(ctx); e == nil {
					t.Error("uncertain kill left original transport open")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			target, err := QueryTarget(ctx, pinned)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := pinned.ExecContext(ctx, "SELECT waiting"); done <- e }()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			err = CancelQuery(ctx, configured, target)
			if err == nil {
				t.Fatal("failure accepted")
			}
			if name == cancelPermissionPath {
				var server *Error
				if !errors.As(err, &server) || server.Code != 1227 {
					t.Fatalf("lost permission error: %v", err)
				}
				close(finish)
				if err = <-done; err != nil {
					t.Fatal(err)
				}
				if err = pinned.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err = <-done; err == nil || errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("uncertain original result permits replay: %v", err)
			}
		})
	}
}

func TestCancelQueryCompletionBeforeSend(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	started, dialing := make(chan struct{}), make(chan struct{})
	peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !readQuery(ctx, t, p, "SELECT waiting") {
			return
		}
		close(started)
		select {
		case <-dialing:
		case <-ctx.Done():
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	configured, err := NewConnector(Config{User: testUser, DialContext: peer})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	target, err := QueryTarget(ctx, pinned)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := pinned.ExecContext(ctx, "SELECT waiting"); done <- e }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	aux, err := NewConnector(Config{User: testUser, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-dialCtx.Done()
		return nil, dialCtx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = CancelQuery(ctx, aux, target); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func serveCancelFailure(ctx context.Context, t *testing.T, p *wire.PacketIO, name string) {
	t.Helper()
	if !readQuery(ctx, t, p, "KILL QUERY 42") {
		return
	}
	switch name {
	case "lost-ack":
		return
	case cancelPermissionPath:
		if !sendPackets(ctx, t, p, []byte{255, 203, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
			return
		}
	case "bad-kill-response":
		if !sendPackets(ctx, t, p, []byte{0}) {
			return
		}
	case "extra-results":
		if !sendPackets(ctx, t, p, []byte{0, 0, 0, 10, 0, 0, 0}) {
			return
		}
	}
	p.ResetSequence()
	if name == cancelPermissionPath {
		serveQuit(t, p)
	} else if _, e := p.ReadMessage(ctx); e == nil {
		t.Error("malformed helper stayed open")
	}
}
