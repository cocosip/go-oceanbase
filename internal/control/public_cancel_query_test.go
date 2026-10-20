package control_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCancelQueryCompletionBeforeSend(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	started, dialing := make(chan struct{}), make(chan struct{})
	peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ReadQuery(ctx, t, p, "SELECT waiting") {
			return
		}
		close(started)
		select {
		case <-dialing:
		case <-ctx.Done():
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	configured, err := oceanbase.NewConnector(config.Config{User: testUser, DialContext: peer})
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
	target, err := oceanbase.QueryTarget(ctx, pinned)
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
	aux, err := oceanbase.NewConnector(config.Config{User: testUser, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		close(dialing)
		<-dialCtx.Done()
		return nil, dialCtx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.CancelQuery(ctx, aux, target); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCancelQueryFailure(t *testing.T) {
	for _, name := range []string{cancelPermissionPath, "lost-ack", "bad-kill-response", "extra-results"} {
		t.Run(name, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			started, finish := make(chan struct{}), make(chan struct{})
			var dials atomic.Int32
			configured, err := oceanbase.NewConnector(config.Config{User: testUser, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				aux := dials.Add(1) > 1
				id := uint32(42)
				if aux {
					id++
				}
				if !serveCancelConnect(ctx, t, p, 2, id) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				if aux {
					serveCancelFailure(ctx, t, p, name)
					return
				}
				if !testpeer.ReadQuery(ctx, t, p, "SELECT waiting") {
					return
				}
				close(started)
				if name == cancelPermissionPath {
					select {
					case <-finish:
					case <-ctx.Done():
						return
					}
					if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
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
			target, err := oceanbase.QueryTarget(ctx, pinned)
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
			err = oceanbase.CancelQuery(ctx, configured, target)
			if err == nil {
				t.Fatal("failure accepted")
			}
			if name == cancelPermissionPath {
				var server *oceanbase.Error
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

func TestCancelQueryValidation(t *testing.T) {
	if _, err := oceanbase.QueryTarget(context.Background(), nil); err == nil {
		t.Fatal("nil pinned connection accepted")
	}
	if err := oceanbase.CancelQuery(context.Background(), nil, oceanbase.CancelTarget{}); err == nil {
		t.Fatal("empty target accepted")
	}
}

func TestCancelQueryIdentity(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(testpeer.ModeFromStatus(status)), func(t *testing.T) { testCancelQueryIdentity(t, status, false, false) })
	}
}
