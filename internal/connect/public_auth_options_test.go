package connect_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAuthNativeRSAPaths(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, plugin := range []string{authentication.SHA256, authentication.CachingSHA2} {
			for _, path := range []string{"initial", "switch", authRSAChangePath} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, plugin, path), func(t *testing.T) {
					testAuthNativeRSAPath(t, key, mode, plugin, path)
				})
			}
		}
	}
}

func TestAuthNativeCredentialRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			t.Setenv("OB_USER", testUser)
			t.Setenv("OB_PASSWORD", testPassword)
			t.Setenv("OB_PROXY_USER", "")
			status := byte(2)
			if mode == config.ModeOracle {
				status = 6
			}
			var calls, dials atomic.Int32
			configured, err := oceanbase.NewConnector(config.Config{User: "fallback", Password: "fallback", Mode: mode,
				Auth: config.AuthOptions{RSAKey: &key.PublicKey}, Credentials: func(ctx context.Context) (config.Credentials, error) {
					calls.Add(1)
					return authExampleCredentials(ctx)
				}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					index := dials.Add(1)
					user, password := testUser, testPassword
					if index == 2 {
						password = changeUserPassword
					} else if index != 1 {
						t.Error("unexpected replacement connection")
						return
					}
					if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.SHA256, false)) {
						return
					}
					response, e := p.ReadMessage(ctx)
					offset := 32 + len(user) + 1
					if e != nil || len(response) < offset+3+key.Size() || string(response[32:offset]) != user+"\x00" ||
						!bytes.Equal(response[offset:offset+3], []byte{252, 0, 1}) ||
						!checkPinnedCipher(t, key, response[offset+3:offset+3+key.Size()], "12345678901234567890", password) {
						t.Error("credential rotation handshake mismatch", e)
						return
					}
					if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) {
						return
					}
					ctx = context.Background()
					for range 2 {
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})})
			if err != nil || calls.Load() != 0 {
				t.Fatal("constructor obtained credentials", err)
			}
			db := sql.OpenDB(configured)
			db.SetMaxIdleConns(0)
			defer func() { _ = db.Close() }()
			for i := range 2 {
				pinned, err := db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if err = pinned.PingContext(context.Background()); err != nil {
					_ = pinned.Close()
					t.Fatal(err)
				}
				t.Setenv("OB_USER", testUser)
				t.Setenv("OB_PASSWORD", changeUserPassword)
				if err = pinned.PingContext(context.Background()); err != nil || calls.Load() != int32(i+1) {
					_ = pinned.Close()
					t.Fatal("ordinary use refreshed credentials", err, calls.Load())
				}
				if err = pinned.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 2 || dials.Load() != 2 {
				t.Fatal("connector did not refresh per physical authentication", calls.Load(), dials.Load())
			}
		})
	}
}

func TestAuthNativeCredentialFailure(t *testing.T) {
	t.Setenv("OB_USER", "")
	t.Setenv("OB_PASSWORD", "auth-input-secret")
	result, err := runAuthTest(t, config.Config{User: testUser, Password: testPassword, Credentials: authExampleCredentials}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, false)) {
			return
		}
		if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
			t.Error("failed credential source fell back to static identity")
		}
	})
	if result != nil || err == nil || strings.Contains(err.Error(), "auth-input-secret") {
		t.Fatal("credential failure accepted or disclosed", err)
	}
}
