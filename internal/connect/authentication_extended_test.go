package connect

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func pluginGreeting(name string, nonce []byte) []byte {
	data := authGreeting(name, false)
	start := bytes.Index(data, []byte("12345678"))
	copy(data[start:start+8], nonce[:8])
	length := start + 8 + 1 + 2 + 1 + 2 + 2
	data[length] = byte(len(nonce) + 1)
	data = append(data[:length+11], nonce[8:]...)
	data = append(data, 0)
	data = append(data, name...)
	return append(data, 0)
}

func TestAuthPluginExtendedHandshake(t *testing.T) {
	for _, name := range []string{"mysql_old_password", "client_ed25519", authentication.Native, authentication.CachingSHA2} {
		for _, initial := range []bool{false, true} {
			t.Run(name+map[bool]string{true: "/initial", false: "/switch"}[initial], func(t *testing.T) {
				nonce := []byte("12345678901234567890")
				nonce[len(nonce)-1] = 0
				var want []byte
				switch name {
				case "client_ed25519":
					nonce = make([]byte, 32) // Binary trailing NUL belongs to the challenge.
					want, _ = auth.Ed25519(testPassword, nonce)
				case authentication.Native:
					want, _ = auth.Native(testPassword, nonce)
				case authentication.CachingSHA2:
					want, _ = auth.CachingSHA2(testPassword, nonce)
				default:
					want, _ = auth.OldPassword(testPassword, nonce)
				}
				_, err := runAuthTest(t, config.Config{User: testUser, Password: testPassword, Auth: config.AuthOptions{AllowOldPassword: true}}, func(ctx context.Context, p *wire.PacketIO) {
					greeting := authGreeting(authentication.Native, false)
					if initial {
						greeting = pluginGreeting(name, nonce)
					}
					if e := p.WriteMessage(ctx, greeting); e != nil {
						t.Error(e)
						return
					}
					response, e := p.ReadMessage(ctx)
					if e != nil {
						t.Error(e)
						return
					}
					if initial {
						if !bytes.Contains(response, want) {
							t.Error("missing initial plugin response")
						}
					} else {
						data := append(append([]byte{0xfe}, name...), 0)
						if e = p.WriteMessage(ctx, append(data, nonce...)); e != nil {
							t.Error(e)
							return
						}
						response, e = p.ReadMessage(ctx)
						if e != nil || !bytes.Equal(response, want) {
							t.Errorf("switch response length %d: %v", len(response), e)
							return
						}
					}
					if e = p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
						t.Error(e)
					}
				})
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAuthPluginOldPasswordPolicy(t *testing.T) {
	var called bool
	for _, override := range []bool{false, true} {
		cfg := config.Config{User: testUser}
		if override {
			cfg.Auth.Plugins = map[string]config.AuthFactory{"mysql_old_password": func() config.AuthPlugin { called = true; return nil }}
		}
		c, err := config.Normalize(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = authentication.New(c.Auth, "mysql_old_password"); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(err)
		}
		if called {
			t.Fatal("factory bypassed weak-algorithm policy")
		}
	}
}

func TestAuthPluginLegacySwitch(t *testing.T) {
	for _, allow := range []bool{false, true} {
		_, err := runAuthTest(t, config.Config{User: testUser, Password: testPassword, Auth: config.AuthOptions{AllowOldPassword: allow}}, func(ctx context.Context, p *wire.PacketIO) {
			if e := p.WriteMessage(ctx, authGreeting(authentication.Native, false)); e != nil {
				t.Error(e)
				return
			}
			if _, e := p.ReadMessage(ctx); e != nil {
				t.Error(e)
				return
			}
			if e := p.WriteMessage(ctx, []byte{0xfe}); e != nil {
				t.Error(e)
				return
			}
			got, e := p.ReadMessage(ctx)
			if !allow {
				if e == nil || len(got) != 0 {
					t.Error("legacy response sent without opt-in")
				}
				return
			}
			want, _ := auth.OldPassword(testPassword, []byte("12345678"))
			if e != nil || !bytes.Equal(got, want) {
				t.Errorf("legacy switch: %x %v", got, e)
				return
			}
			if e = p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
				t.Error(e)
			}
		})
		if allow && err != nil {
			t.Fatal(err)
		}
		if !allow && !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("legacy policy: ", err)
		}
	}
}
