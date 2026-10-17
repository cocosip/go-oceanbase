package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const authBadPromptPath = "bad-prompt"
const authCallbackCancelledPath = "callback-cancelled"

func TestAuthPluginPAM(t *testing.T) {
	client, server := testTLSConfig(t)
	var rounds []int
	callback := func(_ context.Context, prompt PAMChallenge) (string, error) {
		rounds = append(rounds, prompt.Round)
		if prompt.Round == 1 {
			if prompt.Prompt != "Password: " || prompt.Echo || prompt.Last {
				t.Errorf("first prompt: %+v", prompt)
			}
			return "pam-first-factor", nil
		}
		if prompt.Prompt != "OTP: " || !prompt.Echo || !prompt.Last {
			t.Errorf("second prompt: %+v", prompt)
		}
		return "otp", nil
	}
	_, err := runAuthConnTest(t, Config{User: testUser, TLSConfig: client, Auth: AuthOptions{PAM: callback}}, nil, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
		if e := p.WriteMessage(ctx, authGreeting(authNative, true)); e != nil {
			t.Error(e)
			return
		}
		if _, e := p.ReadMessage(ctx); e != nil {
			t.Error(e)
			return
		}
		secure := tls.Server(raw, server)
		if e := secure.HandshakeContext(ctx); e != nil {
			t.Error(e)
			return
		}
		if e := p.ReplaceConn(authPeerConn{secure}); e != nil {
			t.Error(e)
			return
		}
		if _, e := p.ReadMessage(ctx); e != nil {
			t.Error(e)
			return
		}
		for i, packet := range [][]byte{append([]byte("\xfedialog\x00\x04"), []byte("Password: \x00")...), []byte("\x03OTP: \x00")} {
			if e := p.WriteMessage(ctx, packet); e != nil {
				t.Error(e)
				return
			}
			response, e := p.ReadMessage(ctx)
			want := []string{"pam-first-factor\x00", "otp\x00"}[i]
			if e != nil || string(response) != want {
				t.Errorf("PAM reply %d: %q %v", i, response, e)
				return
			}
		}
		if e := p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
			t.Error(e)
		}
	})
	if err != nil || len(rounds) != 2 {
		t.Fatalf("rounds %v: %v", rounds, err)
	}
}

func TestAuthPluginPAMGate(t *testing.T) {
	var called bool
	created, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PAM: func(context.Context, PAMChallenge) (string, error) { called = true; return testPassword, nil }}})
	if err != nil {
		t.Fatal(err)
	}
	for _, more := range []bool{false, true} {
		plugin, e := created.(*connector).newAuthPlugin("dialog")
		if e != nil {
			t.Fatal(e)
		}
		_, e = authResponse(context.Background(), plugin, "dialog", Credentials{}, AuthChallenge{More: more, Nonce: []byte{4}, Data: []byte{4}})
		if !errors.Is(e, ErrTLSRequired) || called {
			t.Fatal("PAM callback ran without verified TLS", e)
		}
	}
}

func TestAuthPluginPAMFallback(t *testing.T) {
	created, err := NewConnector(Config{User: testUser})
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := created.(*connector).newAuthPlugin("dialog")
	if err != nil {
		t.Fatal(err)
	}
	challenge := AuthChallenge{Nonce: []byte("\x04Password: "), TLSVerified: true}
	got, err := authResponse(context.Background(), plugin, "dialog", Credentials{Password: testPassword}, challenge)
	if err != nil || !bytes.Equal(got, []byte("secret\x00")) {
		t.Fatalf("first: %x %v", got, err)
	}
	challenge.More, challenge.Data = true, []byte("\x05OTP: ")
	if _, err = authResponse(context.Background(), plugin, "dialog", Credentials{Password: testPassword}, challenge); !errors.Is(err, ErrUnsupported) {
		t.Fatal("silently reused password for a second factor", err)
	}
}

func TestAuthPluginPAMFailure(t *testing.T) {
	for _, path := range []string{authCallbackCancelledPath, callbackErrorPath, "nul-answer", authBadPromptPath} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			called := false
			created, err := NewConnector(Config{User: testUser, Auth: AuthOptions{PAM: func(context.Context, PAMChallenge) (string, error) {
				called = true
				switch path {
				case authCallbackCancelledPath:
					cancel()
					return "sensitive-answer", nil
				case callbackErrorPath:
					return "", context.DeadlineExceeded
				default:
					return "bad\x00answer", nil
				}
			}}})
			if err != nil {
				t.Fatal(err)
			}
			plugin, err := created.(*connector).newAuthPlugin(authPAM)
			if err != nil {
				t.Fatal(err)
			}
			challenge := AuthChallenge{Nonce: []byte{4, 'x'}, TLSVerified: true}
			if path == authBadPromptPath {
				challenge.Nonce = []byte{6, 'x'}
			}
			response, err := authResponse(ctx, plugin, authPAM, Credentials{}, challenge)
			if err == nil || len(response) != 0 {
				t.Fatal("failed PAM produced credentials", err)
			}
			if path == authBadPromptPath && called {
				t.Fatal("callback received malformed prompt")
			}
			if path == authCallbackCancelledPath && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthPluginPAMUnverifiedHandshake(t *testing.T) {
	for _, initial := range []bool{false, true} {
		called := false
		_, err := runAuthTest(t, Config{User: testUser, Auth: AuthOptions{PAM: func(context.Context, PAMChallenge) (string, error) { called = true; return "sensitive-answer", nil }}}, func(ctx context.Context, p *wire.PacketIO) {
			name := authNative
			if initial {
				name = authPAM
			}
			if e := p.WriteMessage(ctx, authGreeting(name, false)); e != nil {
				t.Error(e)
				return
			}
			if !initial {
				if _, e := p.ReadMessage(ctx); e != nil {
					t.Error(e)
					return
				}
				if e := p.WriteMessage(ctx, []byte("\xfedialog\x00\x04Password: \x00")); e != nil {
					t.Error(e)
					return
				}
			}
			if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
				t.Error("unverified PAM wrote credentials")
			}
		})
		if !errors.Is(err, ErrTLSRequired) || called {
			t.Fatal("PAM TLS gate", err)
		}
	}
}

func TestAuthPluginPAMSwitchMalformed(t *testing.T) {
	created, err := NewConnector(Config{User: testUser})
	if err != nil {
		t.Fatal(err)
	}
	use := &authPluginUse{plugin: builtinAuth{name: authNative}}
	name, challenge, err := created.(*connector).switchAuthentication(&handshakeSession{tlsVerified: true}, use, []byte("\xfedialog\x00\x04Password: \x00\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authResponse(context.Background(), use.plugin, name, Credentials{Password: testPassword}, challenge); err == nil {
		t.Fatal("double NUL prompt accepted")
	}
}
