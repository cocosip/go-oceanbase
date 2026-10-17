package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const changeUserName = "next@tenant"
const changeUserPassword = "next-secret"
const changeUserModeChange = "mode-change"

func changeUserPacket(t *testing.T, mode Mode, database string) []byte {
	t.Helper()
	response, err := auth.Native(changeUserPassword, []byte("12345678901234567890"))
	if err != nil {
		t.Fatal(err)
	}
	if mode == ModeOracle && database == "" {
		database = "next"
	}
	b := append([]byte{17}, changeUserName...)
	b = append(b, 0, byte(len(response)))
	b = append(b, response...)
	b = append(b, database...)
	b = append(b, 0)
	b = append(b, authNative...)
	return append(b, 0)
}

func TestChangeUserIsolation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
			t.Run(fmt.Sprintf("%d/%d", mode, prepare), func(t *testing.T) { testChangeUserIsolation(t, mode, prepare) })
		}
	}
}

func testChangeUserIsolation(t *testing.T, mode Mode, prepare PrepareMode) {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	cfg := Config{User: testUser, Mode: mode, PrepareMode: prepare, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		serveChangeUserIsolation(ctx, t, p, mode, prepare, status)
	})}
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	old, err := pinned.PrepareContext(ctx, testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	var blob *Blob
	var handle *resourceHandle
	if err = pinned.Raw(func(raw any) error {
		c := raw.(*conn)
		if err := c.lock(ctx); err != nil {
			return err
		}
		defer c.unlock()
		var err error
		handle, err = c.registerResourceLocked(pinned, nil)
		blob = &Blob{handle: handle}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, 1); !errors.Is(err, ErrResource) {
		t.Fatalf("old statement survived identity change: %v", err)
	}
	if _, err = blob.Size(ctx); !errors.Is(err, ErrResource) {
		t.Fatalf("old locator survived identity change: %v", err)
	}
	if _, err = (&Cursor{handle: handle}).Fetch(ctx, CursorPosition{Origin: CursorNext}, 1); !errors.Is(err, ErrResource) {
		t.Fatalf("old cursor survived identity change: %v", err)
	}
	if err = handle.close(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := pinned.PrepareContext(ctx, testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.ExecContext(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatalf("changed identity returned to pool: %v %+v", err, db.Stats())
	}
}

func TestChangeUserFailure(t *testing.T) {
	for _, path := range []string{serverErrorPath, authTLSDeniedPath, changeUserModeChange, "baseline-failure"} {
		t.Run(path, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, changeUserPacket(t, ModeMySQL, "")) {
					return
				}
				switch path {
				case serverErrorPath:
					if !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'x'}) {
						return
					}
				case authTLSDeniedPath:
					if !sendPackets(ctx, t, p, append(append([]byte{254}, authClear...), 0)) {
						return
					}
				case changeUserModeChange:
					if !sendPackets(ctx, t, p, commandPacket(0, 0, 6)) {
						return
					}
				case "baseline-failure":
					if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !readQuery(ctx, t, p, "SET NAMES utf8mb4") || !sendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
						return
					}
				}
				if data, err := p.ReadMessage(ctx); err == nil {
					t.Errorf("failed change sent more data: %x", data)
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, "")
			if err == nil || path == authTLSDeniedPath && !errors.Is(err, ErrTLSRequired) || path == changeUserModeChange && !errors.Is(err, ErrModeMismatch) {
				t.Fatalf("change failure=%v", err)
			}
			if err = pinned.PingContext(ctx); err == nil {
				t.Fatal("failed identity change kept session usable")
			}
		})
	}
}

func TestChangeUserValidation(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, credentials := range []Credentials{{}, {User: "invalid\x00user"}, {User: "\xff"}} {
		if err = ChangeUser(ctx, pinned, credentials, ""); err == nil {
			t.Error("invalid user accepted")
		}
	}
	if err = ChangeUser(ctx, pinned, Credentials{User: changeUserName}, "invalid\x00db"); err == nil {
		t.Error("invalid database accepted")
	}
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = ChangeUser(ctx, pinned, Credentials{User: changeUserName}, ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("transaction identity change=%v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = ChangeUser(ctx, nil, Credentials{User: changeUserName}, ""); err == nil {
		t.Error("nil connection accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = ChangeUser(canceled, pinned, Credentials{User: changeUserName}, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if !raw.(*conn).IsValid() {
			return errors.New("pre-send validation poisoned session")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChangeUserEncoding(t *testing.T) {
	b, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: wire.ClientProtocol41 | wire.ClientSecureConnection | wire.ClientPluginAuth, User: "u", Auth: []byte{0, 255}, Database: "d", Plugin: "p"})
	if err != nil || !bytes.Equal(b, []byte{17, 'u', 0, 2, 0, 255, 'd', 0, 'p', 0}) {
		t.Fatalf("OceanBase change-user layout=%x %v", b, err)
	}
}

func TestChangeUserDatabase(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, changeUserPacket(t, mode, "NewSchema")) || !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, "NewSchema"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChangeUserPluginLifecycle(t *testing.T) {
	for _, requireTLS := range []bool{false, true} {
		t.Run(fmt.Sprint(requireTLS), func(t *testing.T) {
			var created, closed atomic.Int32
			cfg := Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{testCustomPlugin: func() AuthPlugin {
				p := &lifecycleAuth{closed: &closed}
				if created.Add(1) == 2 && requireTLS {
					p.security = AuthRequireTLS
				}
				return p
			}}}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !readCommand(ctx, t, p, changeUserPacket(t, ModeMySQL, "")) || !sendPackets(ctx, t, p, append(append(append([]byte{254}, testCustomPlugin...), 0), []byte("12345678901234567890")...)) {
					return
				}
				if data, err := p.ReadMessage(ctx); err != nil || string(data) != "response" {
					t.Error("missing switched response", err)
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !serveBaseline(ctx, t, p, 2) {
					return
				}
				if !requireTLS {
					b := append([]byte{17}, changeUserName...)
					b = append(b, 0, 8)
					b = append(b, "response"...)
					b = append(b, 0)
					b = append(b, testCustomPlugin...)
					b = append(b, 0)
					if !readCommand(ctx, t, p, b) || !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !serveBaseline(ctx, t, p, 2) {
						return
					}
				}
				if !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})}
			connector, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, ""); err != nil {
				t.Fatal(err)
			}
			err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, "")
			if !requireTLS && err != nil || requireTLS && !errors.Is(err, ErrTLSRequired) {
				t.Fatal(err)
			}
			if created.Load() != 2 || closed.Load() != 2 {
				t.Fatal("plugin reused or not closed", created.Load(), closed.Load())
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChangeUserCancel(t *testing.T) {
	received := make(chan struct{})
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readCommand(ctx, t, p, changeUserPacket(t, ModeMySQL, "")) {
			return
		}
		close(received)
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("canceled authentication did not close transport")
		}
	})
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, "") }()
	<-received
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(context.Background()); err == nil {
		t.Fatal("canceled change retained a usable session")
	}
}

func TestChangeUserVerifiedTLS(t *testing.T) {
	clientCfg, serverCfg := testTLSConfig(t)
	cfg := Config{User: testUser, TLSConfig: clientCfg, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return routingTLSPeer(ctx, t, func(server net.Conn) {
			p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
			if !sendPackets(ctx, t, p, authGreeting(authNative, true)) {
				return
			}
			if _, err := p.ReadMessage(ctx); err != nil {
				t.Error(err)
				return
			}
			secure := tls.Server(server, serverCfg)
			if err := secure.HandshakeContext(ctx); err != nil {
				t.Error(err)
				return
			}
			if err := p.ReplaceConn(authPeerConn{secure}); err != nil {
				t.Error(err)
				return
			}
			if _, err := p.ReadMessage(ctx); err != nil {
				t.Error(err)
				return
			}
			if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !serveBaseline(ctx, t, p, 2) {
				return
			}
			ctx = context.Background()
			if !readCommand(ctx, t, p, changeUserPacket(t, ModeMySQL, "")) || !sendPackets(ctx, t, p, append(append([]byte{254}, authClear...), 0)) {
				return
			}
			if data, err := p.ReadMessage(ctx); err != nil || !bytes.Equal(data, append([]byte(changeUserPassword), 0)) {
				t.Error("verified TLS did not carry clear response", err)
				return
			}
			if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !serveBaseline(ctx, t, p, 2) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
	}}
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if err = ChangeUser(context.Background(), pinned, Credentials{changeUserName, changeUserPassword}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChangeUserFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			db := localInfileDB(t, ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				data, err := p.ReadMessage(ctx)
				prefix := changeUserPacket(t, ModeMySQL, "")
				if err != nil || !p.Framed() || !bytes.HasPrefix(data, prefix) || !bytes.Contains(data[len(prefix):], []byte("__proxy_capability_flag")) {
					t.Error("framed change-user layout or attributes missing", err)
					return
				}
				if !sendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !serveBaseline(ctx, t, p, 2) || !serveOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			}, func(cfg *Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				} else {
					cfg.Protocol.OB20 = FeatureRequired
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = ChangeUser(ctx, pinned, Credentials{changeUserName, changeUserPassword}, ""); err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveChangeUserIsolation(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, prepare PrepareMode, status byte) {
	t.Helper()

	if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
		return
	}
	ctx = context.Background()
	if prepare == PrepareServer && !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
		return
	}
	if !readCommand(ctx, t, p, changeUserPacket(t, mode, "")) || !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) {
		return
	}
	if prepare == PrepareServer {
		if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) || !serveOK(ctx, t, p, optimizationExecute(7, 1, false), status) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
	} else if !serveOK(ctx, t, p, []byte("\x03INSERT INTO t VALUES ( 1 )"), status) {
		return
	}
	if !serveOK(ctx, t, p, []byte{14}, status) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}
