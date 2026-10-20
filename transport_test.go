package oceanbase

import (
	context "context"
	sql "database/sql"
	errors "errors"
	net "net"
	testing "testing"
	time "time"

	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func runPlatformPeer(t *testing.T, listener net.Listener, cfg Config) {
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
		p := wire.NewPacketIO(authPeerConn{Conn: server}, 64<<20)
		ctx := context.Background()
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	c, err := NewConnector(cfg)
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

func runPlatformTLSGate(t *testing.T, listener net.Listener, cfg Config) {
	t.Helper()
	defer func() { _ = listener.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = raw.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p := wire.NewPacketIO(raw, 64<<20)
		if err = p.WriteMessage(ctx, authGreeting("mysql_clear_password", false)); err != nil {
			t.Error(err)
			return
		}
		if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
			t.Error("IPC sent plaintext credentials")
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, ErrTLSRequired) {
		t.Fatal("IPC got plaintext exemption", err)
	}
}
