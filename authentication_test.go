package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func authGreeting(plugin string, ssl bool) []byte {
	data := []byte{10}
	data = append(data, []byte("5.7.25-OceanBase")...)
	data = append(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 42)
	data = append(data, []byte("12345678")...)
	data = append(data, 0)
	var caps uint32 = 0x00aa820f
	if ssl {
		caps |= 0x800
	}
	data = binary.LittleEndian.AppendUint16(data, uint16(caps&0xffff))
	data = append(data, 45)
	data = binary.LittleEndian.AppendUint16(data, 2)
	data = binary.LittleEndian.AppendUint16(data, uint16(caps>>16))
	data = append(data, 21)
	data = append(data, make([]byte, 10)...)
	data = append(data, []byte("901234567890")...)
	data = append(data, 0)
	data = append(data, plugin...)
	return append(data, 0)
}

func runAuthTest(t *testing.T, cfg Config, serve func(context.Context, *wire.PacketIO)) (*handshakeSession, error) {
	t.Helper()
	return runAuthConnTest(t, cfg, nil, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

func runAuthConnTest(t *testing.T, cfg Config, wrap func(net.Conn) net.Conn, serve func(context.Context, net.Conn, *wire.PacketIO)) (*handshakeSession, error) {
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
	go func() { defer close(done); serve(ctx, server, wire.NewPacketIO(authPeerConn{server}, 64<<20)) }()
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	transport := client
	if wrap != nil {
		transport = wrap(client)
	}
	result, err := created.(*connector).authenticate(ctx, transport)
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
	_, err := runAuthTest(t, Config{User: testUser, Password: "never-send-clear"}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authClear, false)); err != nil {
			t.Error(err)
			return
		}
		if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
			t.Error("clear authentication wrote bytes before TLS")
		}
	})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatalf("expected TLS gate: %v", err)
	}
}

func TestAuthSwitch(t *testing.T) {
	for _, plugin := range []string{authClear, authCachingSHA2} {
		t.Run(plugin, func(t *testing.T) {
			_, err := runAuthTest(t, Config{User: testUser, Password: "never-send-clear"}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, authGreeting(authNative, false)); err != nil {
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
				if plugin == authCachingSHA2 {
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
			if !errors.Is(err, ErrTLSRequired) {
				t.Fatalf("auth switch bypassed TLS: %v", err)
			}
		})
	}
}

func TestTLSRequired(t *testing.T) {
	_, err := runAuthTest(t, Config{User: testUser, Password: testPassword, TLSConfig: &tls.Config{ServerName: testServerName}}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authNative, false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("TLS silently downgraded")
		}
	})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatal(err)
	}
}

func TestActualMode(t *testing.T) {
	for _, tc := range []struct {
		expected Mode
		status   byte
		want     Mode
		fail     bool
	}{
		{ModeAuto, 2, ModeMySQL, false}, {ModeAuto, 6, ModeOracle, false}, {ModeOracle, 2, ModeAuto, true},
	} {
		result, err := runAuthTest(t, Config{User: testUser, Mode: tc.expected}, func(ctx context.Context, p *wire.PacketIO) {
			if err := p.WriteMessage(ctx, authGreeting(authNative, false)); err != nil {
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
			if !errors.Is(err, ErrModeMismatch) {
				t.Fatal(err)
			}
			continue
		}
		if err != nil || result.mode != tc.want {
			t.Fatalf("actual mode: %v %v", result, err)
		}
	}
}

func TestAuthFactoryTLSGate(t *testing.T) {
	calls := 0
	_, err := runAuthTest(t, Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{"custom_clear": func() AuthPlugin { return &testAuthPlugin{calls: &calls} }}}}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting("custom_clear", false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("custom plugin bypassed gate")
		}
	})
	if !errors.Is(err, ErrTLSRequired) || calls != 0 {
		t.Fatalf("sensitive plugin invoked before TLS: calls=%d err=%v", calls, err)
	}
}

type testAuthPlugin struct{ calls *int }

// The peer keeps the test-wide deadline; a client close must not fail deadline cleanup after a complete write.
type authPeerConn struct{ net.Conn }

func (authPeerConn) SetReadDeadline(time.Time) error  { return nil }
func (authPeerConn) SetWriteDeadline(time.Time) error { return nil }

func (*testAuthPlugin) Security(AuthChallenge) AuthSecurity { return AuthRequireTLS }
func (p *testAuthPlugin) Respond(context.Context, Credentials, AuthChallenge) ([]byte, error) {
	*p.calls++
	return []byte(testPassword), nil
}
