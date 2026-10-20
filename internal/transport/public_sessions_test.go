package transport_test

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func runPlatformPeer(t *testing.T, listener net.Listener, cfg config.Config) {
	t.Helper()
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = server.Close() }()
		if err = server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
		ctx := context.Background()
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("platform peer did not close")
	}
}

func TestPlatformTransportUnix(t *testing.T) {
	listener, path := testpeer.ListenUnix(t, testpeer.UnixTempDir(t), "ob.sock")
	runPlatformPeer(t, listener, config.Config{Network: "unix", Addr: path, User: testpeer.User})
}
