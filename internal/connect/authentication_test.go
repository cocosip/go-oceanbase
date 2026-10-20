package connect

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

var authGreeting = testpeer.AuthGreeting

func runAuthTest(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) (*session.Negotiated, error) {
	t.Helper()
	return runAuthConnTest(t, cfg, nil, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

func runAuthConnTest(t *testing.T, cfg config.Config, wrap func(net.Conn) net.Conn, serve func(context.Context, net.Conn, *wire.PacketIO)) (*session.Negotiated, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	client, err := net.DialTimeout("tcp", listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	server, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	if err := server.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); serve(ctx, server, wire.NewPacketIO(authPeerConn{Conn: server}, 64<<20)) }()
	created, err := config.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	transport := client
	if wrap != nil {
		transport = wrap(client)
	}
	result, err := Authenticate(ctx, created, transport)
	select {
	case <-done:
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		<-done
		t.Fatal("protocol peer did not finish")
	}
	return result, err
}

func TestAuthSecurityGate(t *testing.T) {
	_, err := runAuthTest(t, config.Config{User: testUser, Password: "never-send-clear"}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authentication.Clear, false)); err != nil {
			t.Error(err)
			return
		}
		if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
			t.Error("clear authentication wrote bytes before TLS")
		}
	})
	if !errors.Is(err, dberrors.ErrTLSRequired) {
		t.Fatalf("expected TLS gate: %v", err)
	}
}

func TestAuthSwitch(t *testing.T) {
	for _, plugin := range []string{authentication.Clear, authentication.CachingSHA2} {
		t.Run(plugin, func(t *testing.T) {
			_, err := runAuthTest(t, config.Config{User: testUser, Password: "never-send-clear"}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, authGreeting(authentication.Native, false)); err != nil {
					t.Error(err)
					return
				}
				data, err := p.ReadMessage(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if bytes.Contains(data, []byte("never-send-clear")) {
					t.Error("password sent in initial response")
				}
				switchPacket := append([]byte{0xfe}, plugin...)
				switchPacket = append(switchPacket, 0)
				switchPacket = append(switchPacket, []byte("12345678901234567890")...)
				switchPacket = append(switchPacket, 0)
				if err = p.WriteMessage(ctx, switchPacket); err != nil {
					t.Error(err)
					return
				}
				if plugin == authentication.CachingSHA2 {
					if _, err = p.ReadMessage(ctx); err != nil {
						t.Error(err)
						return
					}
					if err = p.WriteMessage(ctx, []byte{1, 4}); err != nil {
						t.Error(err)
						return
					}
				}
				if data, err = p.ReadMessage(ctx); err == nil || len(data) != 0 {
					t.Error("unsafe auth switch/full-auth sent credentials")
				}
			})
			if !errors.Is(err, dberrors.ErrTLSRequired) {
				t.Fatalf("auth switch bypassed TLS: %v", err)
			}
		})
	}
}

func TestTLSRequired(t *testing.T) {
	_, err := runAuthTest(t, config.Config{User: testUser, Password: testPassword, TLSConfig: &tls.Config{ServerName: testServerName}}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authentication.Native, false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("TLS silently downgraded")
		}
	})
	if !errors.Is(err, dberrors.ErrTLSRequired) {
		t.Fatal(err)
	}
}

func TestActualMode(t *testing.T) {
	for _, tc := range []struct {
		expected config.Mode
		status   byte
		want     config.Mode
		fail     bool
	}{
		{config.ModeAuto, 2, config.ModeMySQL, false}, {config.ModeAuto, 6, config.ModeOracle, false}, {config.ModeOracle, 2, config.ModeAuto, true},
	} {
		result, err := runAuthTest(t, config.Config{User: testUser, Mode: tc.expected}, func(ctx context.Context, p *wire.PacketIO) {
			if err := p.WriteMessage(ctx, authGreeting(authentication.Native, false)); err != nil {
				t.Error(err)
				return
			}
			if _, err := p.ReadMessage(ctx); err != nil {
				t.Error(err)
				return
			}
			if err := p.WriteMessage(ctx, []byte{0, 0, 0, tc.status, 0, 0, 0}); err != nil {
				t.Error(err)
			}
			if tc.fail {
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("mode mismatch retained connection")
				}
			}
		})
		if tc.fail {
			if !errors.Is(err, dberrors.ErrModeMismatch) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || result.Mode != tc.want {
			t.Fatalf("actual mode: %v %v", result, err)
		}
	}
}

func TestAuthFactoryTLSGate(t *testing.T) {
	calls := 0
	_, err := runAuthTest(t, config.Config{User: testUser, Auth: config.AuthOptions{Plugins: map[string]config.AuthFactory{"custom_clear": func() config.AuthPlugin { return &testAuthPlugin{calls: &calls} }}}}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting("custom_clear", false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("custom plugin bypassed gate")
		}
	})
	if !errors.Is(err, dberrors.ErrTLSRequired) || calls != 0 {
		t.Fatalf("sensitive plugin invoked before TLS: calls=%d err=%v", calls, err)
	}
}

type testAuthPlugin struct{ calls *int }

// The peer keeps the test-wide deadline; a client close must not fail deadline cleanup after a complete write.
type authPeerConn = testpeer.AuthPeerConn

func (*testAuthPlugin) Security(config.AuthChallenge) config.AuthSecurity {
	return config.AuthRequireTLS
}

func (p *testAuthPlugin) Respond(context.Context, config.Credentials, config.AuthChallenge) ([]byte, error) {
	*p.calls++
	return []byte(testPassword), nil
}
