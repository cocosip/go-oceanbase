package connect_test

import (
	context "context"
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	command "github.com/cocosip/go-oceanbase/internal/command"
	connect "github.com/cocosip/go-oceanbase/internal/connect"
	control "github.com/cocosip/go-oceanbase/internal/control"
	cursor "github.com/cocosip/go-oceanbase/internal/cursor"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	session "github.com/cocosip/go-oceanbase/internal/session"
	statement "github.com/cocosip/go-oceanbase/internal/statement"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func ownedMixedSession(t *testing.T, cfg config.Config) (*session.Session, *session.Operation) {
	t.Helper()
	cfg, err := config.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := connect.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := configured.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Discard)
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return s, op
}
func closeOwnedMixedSession(t *testing.T, s *session.Session, op *session.Operation) {
	t.Helper()
	if s.Closed() {
		return
	}
	op.EndCancellation(true)
	op.Packets().ResetSequence()
	if err := op.Packets().WriteMessage(context.Background(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	s.Discard()
}
func TestVersionProbeSessionState(t *testing.T) {
	for _, skip := range []bool{false, true} {
		for _, status := range []byte{2, 6} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%t/%d/%s", skip, status, framing), func(t *testing.T) {
					cfg := config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: skip}, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
						ctx := context.Background()
						if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !serveVersionProbeBaseline(ctx, t, p, status, skip) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})}
					if framing == testCompression {
						cfg.Protocol.Compression = config.FeatureRequired
					}
					if framing == testOB20 {
						cfg.Protocol.OB20 = config.FeatureRequired
					}
					normalized, err := config.Normalize(cfg)
					if err != nil {
						t.Fatal(err)
					}
					configured, err := connect.New(normalized)
					if err != nil {
						t.Fatal(err)
					}
					cfg.Session.SkipVersionProbe = !skip
					s, err := configured.Connect(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer s.Discard()
					op, err := s.Acquire(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					defer op.Release()
					for range 2 {
						want := testOutVersion
						if skip {
							want = ""
						}
						p := op.Protocol()
						if p.OBVersion != want || p.Mode != testpeer.ModeFromStatus(status) || op.Config().Session.SkipVersionProbe != skip {
							t.Fatalf("probe changed mode or cached policy/version: %d %q", p.Mode, p.OBVersion)
						}
						if _, err = command.ExecOK(context.Background(), op, []byte{14}); err != nil {
							t.Fatal(err)
						}
					}
					closeOwnedMixedSession(t, s, op)
				})
			}
		}
	}
}
func TestProxyUserChangeUserState(t *testing.T) {
	for _, framing := range []string{testStandard, testCompression, testOB20} {
		for _, charset := range []string{testUTF8MB4, localFilenameCharset} {
			t.Run(framing+"/"+charset, func(t *testing.T) { checkProxyUserSessionState(t, framing, charset) })
		}
	}
}
func checkProxyUserSessionState(t *testing.T, framing, charset string) {
	t.Helper()
	cfg := config.Config{User: testUser, ProxyUser: testProxyUser, Mode: config.ModeOracle, Session: config.SessionOptions{Charset: charset},
		Protocol: config.ProtocolOptions{Compression: config.FeatureOff, OB20: config.FeatureOff, Checksum: config.FeatureOff, NewMetadata: config.FeatureOff, ProxyChecksum: config.FeatureOff, FullLinkTrace: config.FeatureOff}}
	if framing == testCompression {
		cfg.Protocol.Compression = config.FeatureRequired
	}
	if framing == testOB20 {
		cfg.Protocol.OB20 = config.FeatureRequired
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(framing == testCompression)) || !testpeer.ReadAuthResponse(ctx, t, p) {
			return
		}
		var variables map[string]string
		if framing == testOB20 {
			variables = map[string]string{testProxyCapability: "256"}
		}
		if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(6, variables)) {
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
		if !serveAttributeBaseline(ctx, t, p, 6, charset) {
			return
		}
		ctx = context.Background()
		for _, proxy := range []string{"测试", ""} {
			attrs := map[string]string{testDriverDialectAttribute: testDriverDialectValue, testClientAttributeCapability: "5", testProxyRequestCapability: "1048576"}
			if proxy != "" {
				attrs[testProxyUserAttribute] = proxy
				if charset == localFilenameCharset {
					attrs[testProxyUserAttribute] = testAttributeGBKValue
				}
			}
			if framing == testOB20 {
				attrs[testProxyRequestCapability] = "1081600"
			}
			encoded, err := wire.EncodeHandshakeResponse(wire.HandshakeResponse{Capabilities: wire.ClientConnectAttrs, Attributes: attrs})
			if err != nil {
				t.Error(err)
				return
			}
			want := append(changeUserPacket(t, config.ModeOracle, ""), encoded[34:]...)
			if !testpeer.ServeOK(ctx, t, p, want, 6) || !serveAttributeBaseline(ctx, t, p, 6, charset) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	s, op := ownedMixedSession(t, cfg)
	ctx := context.Background()
	m := control.New()
	cache, resolver, cursors, lobs := &statement.Cache{}, &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{}
	for _, proxy := range []string{"测试", ""} {
		if err := m.ChangeUser(ctx, op, cache, resolver, cursors, lobs, config.Credentials{User: changeUserName, Password: changeUserPassword, ProxyUser: proxy}, "", false); err != nil {
			t.Fatal(err)
		}
		if op.Protocol().ProxyUser != proxy || op.Config().ProxyUser != proxy || op.Config().Password != "" {
			t.Fatal("changed proxy identity not published or password retained")
		}
		if _, err := command.ExecOK(ctx, op, []byte{14}); err != nil {
			t.Fatal(err)
		}
	}
	closeOwnedMixedSession(t, s, op)
}
