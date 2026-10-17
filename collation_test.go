package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const testCollationUTF8 = "utf8mb4_bin"
const testCollationGBK = "gbk_bin"
const testCollationLatin1 = "latin1_bin"
const testCollationSQL = "SET NAMES utf8mb4 COLLATE utf8mb4_bin"

func TestCollationConfig(t *testing.T) {
	created, err := NewConnector(Config{User: testUser, Session: SessionOptions{Collation: "UTF8MB4_BIN"}})
	if err != nil || created.(*connector).cfg.Session.Collation != testCollationUTF8 {
		t.Fatal("collation not normalized", err)
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/?charset=GBK&collation=GBK_BIN")
	if err != nil || cfg.Session.Charset != localFilenameCharset || cfg.Session.Collation != testCollationGBK {
		t.Fatal("DSN session options lost", err)
	}
	for _, value := range []string{"utf8mb4_bin;SELECT private", "utf8mb4_bin\x00private", "utf8mb4_bin private", "utf8mb4_bin'private", "私有", "UTF8MB4_K", "utf8mb4_bin\xff", strings.Repeat("a", 129)} {
		_, err := NewConnector(Config{User: testUser, Session: SessionOptions{Collation: value}})
		var configErr *ConfigError
		if !errors.As(err, &configErr) || configErr.Field != "Session.Collation" || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe collation accepted or exposed: %v", err)
		}
	}
	if _, err = NewConnector(Config{User: testUser, Mode: ModeOracle, Session: SessionOptions{Collation: testCollationUTF8}}); err == nil {
		t.Fatal("Oracle collation silently accepted")
	}
}

func TestCollationModeGuard(t *testing.T) {
	created, err := NewConnector(Config{User: testUser, Session: SessionOptions{Collation: testCollationUTF8}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 6) {
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("Oracle received collation initialization SQL")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatal("auto-detected Oracle did not reject collation", err)
	}
}

func TestCollationServerFailure(t *testing.T) {
	for _, collation := range []string{testCollationLatin1, "utf8mb4_unknown"} {
		t.Run(collation, func(t *testing.T) {
			created, err := NewConnector(Config{User: testUser, Mode: ModeMySQL, Session: SessionOptions{Collation: collation}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, 2) || !readQuery(ctx, t, p, "SET NAMES utf8mb4 COLLATE "+collation) || !sendPackets(ctx, t, p, []byte{255, 229, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
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
			var serverErr *Error
			if !errors.As(err, &serverErr) || serverErr.Code != 1253 {
				t.Fatal("server collation failure hidden", err)
			}
		})
	}
}

func TestCollationLifecycle(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, tc := range []struct {
			charset, collation, names, text string
			columnCharset                   uint16
		}{
			{charsetUTF8MB4, testCollationUTF8, testCollationSQL, "测试", 46},
			{localFilenameCharset, testCollationGBK, "SET NAMES gbk COLLATE gbk_bin", testAttributeGBKValue, 87},
			{charsetUTF8MB3Alias, "utf8_general_ci", "SET NAMES utf8 COLLATE utf8_general_ci", "测试", 33},
		} {
			t.Run(framing+"/"+tc.charset, func(t *testing.T) {
				cfg := Config{User: testUser, Mode: ModeMySQL, Session: SessionOptions{Charset: tc.charset, Collation: tc.collation, InitSQL: []string{"SET NAMES latin1"}}, Protocol: ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff, Checksum: FeatureOff, NewMetadata: FeatureOff, ProxyChecksum: FeatureOff, FullLinkTrace: FeatureOff}}
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = FeatureRequired
				}
				cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, protocolGreeting(framing == testCompression)) {
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
					if !sendPackets(ctx, t, p, protocolOK(2, variables)) {
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
					attrs := map[string]string{testClientAttributeCapability: "1"}
					if framing == testOB20 {
						attrs["__proxy_capability_flag"] = "33024"
					}
					packet, err := wire.EncodeChangeUser(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs | wire.ClientSecureConnection | wire.ClientPluginAuth | wire.ClientPluginAuthLenEnc, User: changeUserName, Auth: response, Plugin: authNative, Attributes: attrs})
					if err != nil {
						t.Error(err)
						return
					}
					if !serveOK(ctx, t, p, packet, 2) || !serveCollationBaseline(ctx, t, p, tc.names) || !readQuery(ctx, t, p, "SELECT '"+tc.text+"'") {
						return
					}
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("v", 253, tc.columnCharset, 0), []byte{254, 0, 0, 2, 0}, append([]byte{byte(len(tc.text))}, tc.text...), []byte{254, 0, 0, 2, 0}) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				created, err := NewConnector(cfg)
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
				if err = ChangeUser(ctx, pinned, Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
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

func TestCollationFullReset(t *testing.T) {
	var sessions atomic.Uint32
	created, err := NewConnector(Config{User: testUser, Mode: ModeMySQL, ResetMode: ResetFull,
		Session: SessionOptions{Collation: testCollationUTF8, InitSQL: []string{"SET NAMES latin1"}},
		DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
			if !serveConnect(ctx, t, p, 2) || !serveCollationBaseline(ctx, t, p, testCollationSQL) {
				return
			}
			id := sessions.Add(1)
			ctx = context.Background()
			if !serveOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			if id == 1 {
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("full reset retained the old collation session")
				}
				return
			}
			serveQuit(t, p)
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

func serveCollationBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, names string) bool {
	t.Helper()
	return serveOK(ctx, t, p, []byte("\x03"+names), 2) && serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), 2) && serveVersion(ctx, t, p, 2, false) && serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2) && serveOK(ctx, t, p, []byte("\x03SET NAMES latin1"), 2) && serveOK(ctx, t, p, []byte("\x03"+names), 2) && serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), 2)
}

func ExampleSessionOptions_collation() {
	connector, err := NewConnector(Config{User: "app", Session: SessionOptions{Charset: "utf8mb4", Collation: "utf8mb4_bin"}})
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}
