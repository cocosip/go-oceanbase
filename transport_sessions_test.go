package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPlatformTransportUnixMixedSessions(t *testing.T) {
	// Subtest-derived TempDir names can exceed sockaddr_un's platform limit.
	directory, err := os.MkdirTemp("", "ob-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				path := filepath.Join(directory, "mixed.sock")
				listener, err := net.Listen(networkUnix, path)
				if err != nil {
					t.Fatal(err)
				}
				runPlatformMixedSession(t, listener, Config{Network: networkUnix, Addr: path, Mode: mode}, framing)
			})
		}
	}
}

func runPlatformMixedSession(t *testing.T, listener net.Listener, cfg Config, framing string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	status := byte(2)
	if cfg.Mode == ModeOracle {
		status = 6
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		server, err := listener.Accept()
		if err != nil {
			if !t.Failed() {
				t.Error(err)
			}
			return
		}
		defer func() { _ = server.Close() }()
		if err = server.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
		if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
		for i := range 2 {
			if !serveDriverSessionCycle(ctx, t, p, cfg.Mode, i) {
				return
			}
		}
		if !readCommand(ctx, t, p, []byte{1}) {
			return
		}
		// Windows AF_UNIX can omit peer EOF even after a successful net.UnixConn.Close.
		if runtime.GOOS == "windows" && cfg.Network == networkUnix {
			return
		}
		if _, err = p.ReadMessage(ctx); !errors.Is(err, io.EOF) {
			t.Error("native IPC peer did not observe EOF after QUIT", err)
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	cfg.User = testUser
	cfg.Execution = ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}
	cfg.Protocol = ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff}
	if framing == testCompression {
		cfg.Protocol.Compression = FeatureRequired
	}
	if framing == testOB20 {
		cfg.Protocol.OB20 = FeatureRequired
	}
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err = db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var physical *conn
	err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil })
	closeErr := pinned.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	for i := range 2 {
		runDriverSessionCycle(ctx, t, db, physical, i)
		checkBenchmarkIdle(t, physical, db.Stats())
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 || !physical.closed.Load() {
		t.Fatal("native IPC did not release its pool and original physical session", stats)
	}
	if cfg.Network == networkUnix {
		if err = physical.session.raw.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
			t.Fatal("native IPC underlying transport is still open after DB.Close", err)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("native IPC peer did not finish its QUIT handling")
	}
}
