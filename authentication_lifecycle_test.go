package oceanbase

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const authCloseErrorPath = "close-error"
const authInitialErrorPath = "initial-error"
const authSwitchPath = "switch"
const authTLSDeniedPath = "tls-denied"

type lifecycleAuth struct {
	closed   *atomic.Int32
	fail     error
	closeErr error
	security AuthSecurity
}

func (p *lifecycleAuth) Security(AuthChallenge) AuthSecurity { return p.security }
func (p *lifecycleAuth) Respond(context.Context, Credentials, AuthChallenge) ([]byte, error) {
	return []byte("response"), p.fail
}
func (p *lifecycleAuth) Close() error { p.closed.Add(1); return p.closeErr }

func TestAuthPluginLifecycle(t *testing.T) {
	for _, path := range []string{"authenticated", serverErrorPath, authSwitchPath, authInitialErrorPath, authTLSDeniedPath, authCloseErrorPath} {
		t.Run(path, func(t *testing.T) {
			var closed, created atomic.Int32
			cfg := Config{User: testUser, Auth: AuthOptions{Plugins: map[string]AuthFactory{
				testCustomPlugin: func() AuthPlugin {
					created.Add(1)
					p := &lifecycleAuth{closed: &closed}
					if path == authCloseErrorPath {
						p.closeErr = context.DeadlineExceeded
					}
					if path == authInitialErrorPath {
						p.fail = context.Canceled
					}
					if path == authTLSDeniedPath {
						p.security = AuthRequireTLS
					}
					return p
				},
			}}}
			for range 2 {
				_, err := runAuthTest(t, cfg, func(ctx context.Context, packets *wire.PacketIO) {
					if e := packets.WriteMessage(ctx, authGreeting(testCustomPlugin, false)); e != nil {
						t.Error(e)
						return
					}
					if _, e := packets.ReadMessage(ctx); e != nil {
						if path != authInitialErrorPath && path != authTLSDeniedPath {
							t.Error(e)
						}
						return
					}
					if path == authSwitchPath {
						data := append([]byte{0xfe}, testCustomPlugin...)
						if e := packets.WriteMessage(ctx, append(data, 0)); e != nil {
							t.Error(e)
							return
						}
						if _, e := packets.ReadMessage(ctx); e != nil {
							t.Error(e)
							return
						}
					}
					data := []byte{0, 0, 0, 2, 0, 0, 0}
					if path == serverErrorPath {
						data = []byte{0xff, 0x15, 0x04, '#', '2', '8', '0', '0', '0', 'x'}
					}
					if e := packets.WriteMessage(ctx, data); e != nil {
						t.Error(e)
					}
				})
				if (err == nil) != (path == "authenticated" || path == authSwitchPath) {
					t.Fatalf("path %s: %v", path, err)
				}
				if path == authInitialErrorPath && !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				if path == authCloseErrorPath && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("close error lost", err)
				}
				if closed.Load() != created.Load() {
					t.Fatalf("created %d; closed %d", created.Load(), closed.Load())
				}
			}
		})
	}
}

func TestAuthPluginTypedNil(t *testing.T) {
	c := connector{cfg: Config{Auth: AuthOptions{Plugins: map[string]AuthFactory{testCustomPlugin: func() AuthPlugin { return (*lifecycleAuth)(nil) }}}}}
	if _, err := c.newAuthPlugin(testCustomPlugin); !errors.Is(err, ErrUnsupported) {
		t.Fatal("typed nil accepted", err)
	}
}
