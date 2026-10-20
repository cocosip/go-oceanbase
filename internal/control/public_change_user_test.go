package control_test

import (
	bytes "bytes"
	context "context"
	tls "crypto/tls"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestChangeUserFraming(t *testing.T) {
	for _, framing := range []string{testCompression, testOB20} {
		t.Run(framing, func(t *testing.T) {
			db := localInfileDB(t, config.ModeMySQL, true, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				data, err := p.ReadMessage(ctx)
				prefix := changeUserPacket(t, config.ModeMySQL, "")
				if err != nil || !p.Framed() || !bytes.HasPrefix(data, prefix) || !bytes.Contains(data[len(prefix):], []byte("__proxy_capability_flag")) {
					t.Error("framed change-user layout or attributes missing", err)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(cfg *config.Config) {
				if framing == testCompression {
					cfg.Protocol.Compression = config.FeatureRequired
				} else {
					cfg.Protocol.OB20 = config.FeatureRequired
				}
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChangeUserVerifiedTLS(t *testing.T) {
	clientCfg, serverCfg := testpeer.TLSConfig(t)
	cfg := config.Config{User: testUser, TLSConfig: clientCfg, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return testpeer.RoutingTLSPeer(ctx, t, func(server net.Conn) {
			p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
			if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, true)) {
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
			if err := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); err != nil {
				t.Error(err)
				return
			}
			if _, err := p.ReadMessage(ctx); err != nil {
				t.Error(err)
				return
			}
			if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
				return
			}
			ctx = context.Background()
			if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, config.ModeMySQL, "")) || !testpeer.SendPackets(ctx, t, p, append(append([]byte{254}, authentication.Clear...), 0)) {
				return
			}
			if data, err := p.ReadMessage(ctx); err != nil || !bytes.Equal(data, append([]byte(changeUserPassword), 0)) {
				t.Error("verified TLS did not carry clear response", err)
				return
			}
			if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
	}}
	connector, err := oceanbase.NewConnector(cfg)
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
	if err = oceanbase.ChangeUser(context.Background(), pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestChangeUserCancel(t *testing.T) {
	received := make(chan struct{})
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, config.ModeMySQL, "")) {
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
	go func() {
		done <- oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, "")
	}()
	<-received
	cancel()
	if err = <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(context.Background()); err == nil {
		t.Fatal("canceled change retained a usable session")
	}
}

func TestChangeUserPluginLifecycle(t *testing.T) {
	for _, requireTLS := range []bool{false, true} {
		t.Run(fmt.Sprint(requireTLS), func(t *testing.T) {
			var created, closed atomic.Int32
			cfg := config.Config{User: testUser, Auth: config.AuthOptions{Plugins: map[string]config.AuthFactory{testCustomPlugin: func() config.AuthPlugin {
				p := &lifecycleAuth{closed: &closed}
				if created.Add(1) == 2 && requireTLS {
					p.security = config.AuthRequireTLS
				}
				return p
			}}}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, config.ModeMySQL, "")) || !testpeer.SendPackets(ctx, t, p, append(append(append([]byte{254}, testCustomPlugin...), 0), []byte("12345678901234567890")...)) {
					return
				}
				if data, err := p.ReadMessage(ctx); err != nil || string(data) != "response" {
					t.Error("missing switched response", err)
					return
				}
				if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				if !requireTLS {
					b := append([]byte{17}, changeUserName...)
					b = append(b, 0, 8)
					b = append(b, "response"...)
					b = append(b, 0)
					b = append(b, testCustomPlugin...)
					b = append(b, 0)
					if !testpeer.ReadCommand(ctx, t, p, b) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
						return
					}
				}
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})}
			connector, err := oceanbase.NewConnector(cfg)
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
			if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
				t.Fatal(err)
			}
			err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, "")
			if !requireTLS && err != nil || requireTLS && !errors.Is(err, dberrors.ErrTLSRequired) {
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

func TestChangeUserDatabase(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			status := byte(2)
			if mode == config.ModeOracle {
				status = 6
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, mode, "NewSchema")) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			pinned, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, "NewSchema"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChangeUserEncoding(t *testing.T) {
	b, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: wire.ClientProtocol41 | wire.ClientSecureConnection | wire.ClientPluginAuth, User: "u", Auth: []byte{0, 255}, Database: "d", Plugin: "p"})
	if err != nil || !bytes.Equal(b, []byte{17, 'u', 0, 2, 0, 255, 'd', 0, 'p', 0}) {
		t.Fatalf("OceanBase change-user layout=%x %v", b, err)
	}
}

func TestChangeUserValidation(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}, {testRollbackSQL, 2}}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	for _, credentials := range []config.Credentials{{}, {User: "invalid\x00user"}, {User: "\xff"}} {
		if err = oceanbase.ChangeUser(ctx, pinned, credentials, ""); err == nil {
			t.Error("invalid user accepted")
		}
	}
	if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName}, "invalid\x00db"); err == nil {
		t.Error("invalid database accepted")
	}
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName}, ""); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatalf("transaction identity change=%v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = oceanbase.ChangeUser(ctx, nil, config.Credentials{User: changeUserName}, ""); err == nil {
		t.Error("nil connection accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = oceanbase.ChangeUser(canceled, pinned, config.Credentials{User: changeUserName}, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Raw(func(raw any) error {
		if !raw.(driver.Validator).IsValid() {
			return errors.New("pre-send validation poisoned session")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestChangeUserFailure(t *testing.T) {
	for _, path := range []string{serverErrorPath, authTLSDeniedPath, changeUserModeChange, "baseline-failure"} {
		t.Run(path, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, config.ModeMySQL, "")) {
					return
				}
				switch path {
				case serverErrorPath:
					if !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'x'}) {
						return
					}
				case authTLSDeniedPath:
					if !testpeer.SendPackets(ctx, t, p, append(append([]byte{254}, authentication.Clear...), 0)) {
						return
					}
				case changeUserModeChange:
					if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 6)) {
						return
					}
				case "baseline-failure":
					if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, 2)) || !testpeer.ReadQuery(ctx, t, p, "SET NAMES utf8mb4") || !testpeer.SendPackets(ctx, t, p, []byte{255, 21, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
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
			err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, "")
			if err == nil || path == authTLSDeniedPath && !errors.Is(err, dberrors.ErrTLSRequired) || path == changeUserModeChange && !errors.Is(err, dberrors.ErrModeMismatch) {
				t.Fatalf("change failure=%v", err)
			}
			if err = pinned.PingContext(ctx); err == nil {
				t.Fatal("failed identity change kept session usable")
			}
		})
	}
}
