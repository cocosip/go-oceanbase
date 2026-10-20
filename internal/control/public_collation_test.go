package control_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	atomic "sync/atomic"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	auth "github.com/cocosip/go-oceanbase/internal/auth"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCollationFullReset(t *testing.T) {
	var sessions atomic.Uint32
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: config.ModeMySQL, ResetMode: config.ResetFull,
		Session: config.SessionOptions{Collation: testCollationUTF8, InitSQL: []string{"SET NAMES latin1"}},
		DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
			if !testpeer.ServeConnect(ctx, t, p, 2) || !serveCollationBaseline(ctx, t, p, testCollationSQL) {
				return
			}
			id := sessions.Add(1)
			ctx = context.Background()
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			if id == 1 {
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("full reset retained the old collation session")
				}
				return
			}
			testpeer.ServeQuit(t, p)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	for range 2 {
		if err := db.PingContext(context.Background()); err != nil {
			t.Fatal("replacement session lost collation baseline", err)
		}
	}
	if sessions.Load() != 2 {
		t.Fatal("full reset did not create two physical sessions", sessions.Load())
	}
}

func TestCollationLifecycle(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, tc := range []struct {
			charset, collation, names, text string
			columnCharset                   uint16
		}{
			{"utf8mb4", testCollationUTF8, testCollationSQL, "测试", 46},
			{localFilenameCharset, testCollationGBK, "SET NAMES gbk COLLATE gbk_bin", testAttributeGBKValue, 87},
			{"utf8mb3", "utf8_general_ci", "SET NAMES utf8 COLLATE utf8_general_ci", "测试", 33},
		} {
			t.Run(framing+"/"+tc.charset, func(t *testing.T) {
				cfg := config.Config{User: testUser, Mode: config.ModeMySQL, Session: config.SessionOptions{Charset: tc.charset, Collation: tc.collation, InitSQL: []string{"SET NAMES latin1"}}, Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff, Checksum: config.FeatureOff, NewMetadata: config.FeatureOff, ProxyChecksum: config.FeatureOff, FullLinkTrace: config.FeatureOff}}
				if framing == testCompression {
					cfg.Protocol.Compression = config.FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = config.FeatureRequired
				}
				cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(framing == testCompression)) {
						return
					}
					if _, err := p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					var variables map[string]string
					if framing == testOB20 {
						variables = map[string]string{testProxyCapability: "256"}
					}
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, variables)) {
						return
					}
					var err error
					if framing == testCompression {
						err = p.EnableCompression()
					}
					if framing == testOB20 {
						err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
					}
					if err != nil {
						t.Error(err)
						return
					}
					if !serveCollationBaseline(ctx, t, p, tc.names) {
						return
					}
					ctx = context.Background()
					response, err := auth.Native(changeUserPassword, []byte("12345678901234567890"))
					if err != nil {
						t.Error(err)
						return
					}
					attrs := map[string]string{testDriverDialectAttribute: testDriverDialectValue, testClientAttributeCapability: "1", testProxyRequestCapability: "1048576"}
					if framing == testOB20 {
						attrs[testProxyRequestCapability] = "1081600"
					}
					packet, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs | wire.ClientSecureConnection | wire.ClientPluginAuth | wire.ClientPluginAuthLenEnc, User: changeUserName, Auth: response, Plugin: authentication.Native, Attributes: attrs})
					if err != nil {
						t.Error(err)
						return
					}
					if !testpeer.ServeOK(ctx, t, p, packet, 2) || !serveCollationBaseline(ctx, t, p, tc.names) || !testpeer.ReadQuery(ctx, t, p, "SELECT '"+tc.text+"'") {
						return
					}
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 253, tc.columnCharset, 0), []byte{254, 0, 0, 2, 0}, append([]byte{byte(len(tc.text))}, tc.text...), []byte{254, 0, 0, 2, 0}) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				created, err := oceanbase.NewConnector(cfg)
				if err != nil {
					t.Fatal(err)
				}
				db := sql.OpenDB(created)
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
				var got string
				if err = pinned.QueryRowContext(ctx, "SELECT '测试'").Scan(&got); err != nil || got != "测试" {
					t.Fatal("collation reset changed text encoding", got, err)
				}
			})
		}
	}
}

func TestCollationServerFailure(t *testing.T) {
	for _, collation := range []string{testCollationLatin1, "utf8mb4_unknown"} {
		t.Run(collation, func(t *testing.T) {
			created, err := oceanbase.NewConnector(config.Config{User: testUser, Mode: config.ModeMySQL, Session: config.SessionOptions{Collation: collation}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ReadQuery(ctx, t, p, "SET NAMES utf8mb4 COLLATE "+collation) || !testpeer.SendPackets(ctx, t, p, []byte{255, 229, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("failed collation continued initialization")
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = created.Connect(context.Background())
			var serverErr *oceanbase.Error
			if !errors.As(err, &serverErr) || serverErr.Code != 1253 {
				t.Fatal("server collation failure hidden", err)
			}
		})
	}
}

func TestCollationModeGuard(t *testing.T) {
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Session: config.SessionOptions{Collation: testCollationUTF8}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 6) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("Oracle received collation initialization SQL")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("auto-detected Oracle did not reject collation", err)
	}
}
