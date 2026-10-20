package connect_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestConnectionAttributesChangeUser(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, variant := range []struct{ framing, charset string }{{testStandard, testUTF8MB4}, {testCompression, testUTF8MB4}, {testOB20, testUTF8MB4}, {testStandard, localFilenameCharset}, {testCompression, localFilenameCharset}, {testOB20, localFilenameCharset}} {
			framing := variant.framing
			t.Run(fmt.Sprintf("%d/%s/%s", mode, framing, variant.charset), func(t *testing.T) {
				cfg := config.Config{User: testUser, Mode: mode, Interactive: true, Session: config.SessionOptions{Charset: variant.charset}, ConnectionAttributes: map[string]string{testAttributeKey: testAttributeValue, "应用": "测试"}, Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff}}
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
					initial, err := p.ReadMessage(ctx)
					if err != nil || len(initial) < 32 {
						t.Error("missing initial response", err)
						return
					}
					caps := uint64(binary.LittleEndian.Uint32(initial))
					if caps&(1<<10) == 0 || caps&wire.ClientConnectAttrs == 0 {
						t.Error("missing requested capabilities")
						return
					}
					status := byte(2)
					if mode == config.ModeOracle {
						status = 6
					}
					var variables map[string]string
					if framing == testOB20 {
						variables = map[string]string{testProxyCapability: "256"}
					}
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, variables)) {
						return
					}
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
					if !serveAttributeBaseline(ctx, t, p, status, variant.charset) {
						return
					}
					ctx = context.Background()
					database := ""
					if mode == config.ModeOracle {
						database = "next"
					}
					want := changeUserPacket(t, mode, database)
					key, value := "应用", "测试"
					if variant.charset == localFilenameCharset {
						key, value = "\xd3\xa6\xd3\xc3", testAttributeGBKValue
					}
					attrs, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: map[string]string{testDriverDialectAttribute: testDriverDialectValue, testAttributeKey: testAttributeValue, key: value, testClientAttributeCapability: "3", testProxyRequestCapability: map[bool]string{false: "1048648", true: "1360200"}[framing == testOB20]}})
					if err != nil {
						t.Error(err)
						return
					}
					want = append(want, attrs[34:]...)
					if !testpeer.ServeOK(ctx, t, p, want, status) || !serveAttributeBaseline(ctx, t, p, status, variant.charset) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
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
				if err = pinned.PingContext(ctx); err != nil {
					t.Fatal("unread ChangeUser tail", err)
				}
			})
		}
	}
}
