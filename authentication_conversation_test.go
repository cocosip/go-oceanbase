package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const authNilOutputPath = "nil-output"
const authEmptyOutputPath = "empty-output"

const testGSSPlugin = "auth_gssapi_client"
const rejectExtraTokenPath = "reject-extra-token"
const requiresFinalOKPath = "requires-final-ok"

type scriptedConversation struct {
	seed, token []byte
	output      []byte
	calls       int
	closed      int
	moreTLS     bool
}

func (p *scriptedConversation) Security(c AuthChallenge) AuthSecurity {
	if c.More && p.moreTLS {
		return AuthRequireTLS
	}
	return AuthChallengeResponse
}

func (p *scriptedConversation) Respond(_ context.Context, _ Credentials, c AuthChallenge) ([]byte, error) {
	p.calls++
	if p.calls == 1 && !c.More && bytes.Equal(c.Nonce, p.seed) {
		return []byte("initial-token"), nil
	}
	if p.calls == 2 && c.More && bytes.Equal(c.Data, p.token) {
		return p.output, nil
	}
	return nil, wire.ErrMalformed
}

func (p *scriptedConversation) Complete() bool { return p.calls == 2 }
func (p *scriptedConversation) Close() error   { p.closed++; return nil }

func TestAuthPluginConversationRawTokens(t *testing.T) {
	for _, prefix := range []byte{0, 1, 0xfe, 0xff, 0x60} {
		for _, initial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%02x", prefix)+map[bool]string{true: "/initial", false: "/switch"}[initial], func(t *testing.T) {
				p := &scriptedConversation{seed: []byte("service/db.example\x00Kerberos\x00"), token: []byte{prefix, 11, 12}, output: []byte("final-token")}
				if prefix == 0 {
					p.token = []byte{0, 0, 0, 2, 0, 0, 0}
				}
				cfg := Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{testGSSPlugin: func() AuthPlugin { return p }}}}
				_, err := runAuthTest(t, cfg, func(ctx context.Context, packets *wire.PacketIO) {
					greeting := authGreeting(authNative, false)
					if initial {
						greeting = pluginGreeting(testGSSPlugin, p.seed)
					}
					if e := packets.WriteMessage(ctx, greeting); e != nil {
						t.Error(e)
						return
					}
					if _, e := packets.ReadMessage(ctx); e != nil {
						t.Error(e)
						return
					}
					if !initial {
						switchData := append(append([]byte{0xfe}, testGSSPlugin...), 0)
						if e := packets.WriteMessage(ctx, append(switchData, p.seed...)); e != nil {
							t.Error(e)
							return
						}
						if got, e := packets.ReadMessage(ctx); e != nil || !bytes.Equal(got, []byte("initial-token")) {
							t.Error("missing initial conversation token", e)
							return
						}
					}
					if e := packets.WriteMessage(ctx, p.token); e != nil {
						t.Error(e)
						return
					}
					if got, e := packets.ReadMessage(ctx); e != nil || !bytes.Equal(got, p.output) {
						t.Error("raw token was interpreted as a protocol marker", e)
						return
					}
					if e := packets.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
						t.Error(e)
					}
				})
				if err != nil || p.calls != 2 || p.closed != 1 {
					t.Fatalf("conversation calls=%d closes=%d: %v", p.calls, p.closed, err)
				}
			})
		}
	}
}

func TestAuthPluginConversationCompletion(t *testing.T) {
	for _, path := range []string{authNilOutputPath, authEmptyOutputPath, requiresFinalOKPath, rejectExtraTokenPath} {
		t.Run(path, func(t *testing.T) {
			p := &scriptedConversation{seed: []byte("service/db.example\x00Kerberos\x00"), token: []byte{0x60, 1, 2}}
			if path == authEmptyOutputPath {
				p.output = []byte{}
			}
			_, err := runAuthConnTest(t, Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{testGSSPlugin: func() AuthPlugin { return p }}}}, nil, func(ctx context.Context, raw net.Conn, packets *wire.PacketIO) {
				if e := packets.WriteMessage(ctx, pluginGreeting(testGSSPlugin, p.seed)); e != nil {
					t.Error(e)
					return
				}
				if _, e := packets.ReadMessage(ctx); e != nil {
					t.Error(e)
					return
				}
				if e := packets.WriteMessage(ctx, p.token); e != nil {
					t.Error(e)
					return
				}
				if path == requiresFinalOKPath {
					_ = raw.Close()
					return
				}
				if path == rejectExtraTokenPath {
					if e := packets.WriteMessage(ctx, []byte{1, 4}); e != nil {
						t.Error(e)
					}
					return
				}
				if p.output != nil {
					if got, e := packets.ReadMessage(ctx); e != nil || len(got) != 0 {
						t.Error("explicit empty token not sent", e)
						return
					}
				}
				if e := packets.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
					t.Error(e)
					return
				}
				if path == authNilOutputPath {
					checkCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
					defer cancel()
					_, e := packets.ReadMessage(checkCtx)
					var timeout net.Error
					if !errors.As(e, &timeout) || !timeout.Timeout() {
						t.Error("nil output sent an extra token", e)
					}
				}
			})
			wantError := path == requiresFinalOKPath || path == rejectExtraTokenPath
			if (err != nil) != wantError || p.closed != 1 || p.calls != 2 {
				t.Fatalf("conversation completion calls=%d closes=%d: %v", p.calls, p.closed, err)
			}
		})
	}
}

func TestAuthPluginConversationTLSGate(t *testing.T) {
	p := &scriptedConversation{seed: []byte("service/db.example\x00Kerberos\x00"), token: []byte{1, 2}, moreTLS: true}
	_, err := runAuthTest(t, Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{testGSSPlugin: func() AuthPlugin { return p }}}}, func(ctx context.Context, packets *wire.PacketIO) {
		if e := packets.WriteMessage(ctx, pluginGreeting(testGSSPlugin, p.seed)); e != nil {
			t.Error(e)
			return
		}
		if _, e := packets.ReadMessage(ctx); e != nil {
			t.Error(e)
			return
		}
		if e := packets.WriteMessage(ctx, p.token); e != nil {
			t.Error(e)
			return
		}
		if _, e := packets.ReadMessage(ctx); e == nil {
			t.Error("conversation bypassed continuation TLS gate")
		}
	})
	if !errors.Is(err, ErrTLSRequired) || p.calls != 1 || p.closed != 1 {
		t.Fatalf("TLS gate calls=%d closes=%d: %v", p.calls, p.closed, err)
	}
}
