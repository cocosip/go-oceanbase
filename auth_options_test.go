package oceanbase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const authRSAChangePath = "change"
const authRSAOversizedPassword = "oversized-password"
const authRSAUnsolicitedKey = "unsolicited-key"

func TestAuthExampleCredentials(t *testing.T) {
	t.Setenv("OB_USER", "rotating@tenant")
	t.Setenv("OB_PASSWORD", "auth-input-secret")
	t.Setenv("OB_PROXY_USER", "")
	got, err := authExampleCredentials(context.Background())
	if err != nil || got.User != "rotating@tenant" || got.Password != "auth-input-secret" || got.ProxyUser != "" {
		t.Fatal("environment credentials unavailable", err)
	}
	t.Setenv("OB_PASSWORD", "")
	t.Setenv("OB_PROXY_USER", "business")
	got, err = authExampleCredentials(context.Background())
	if err != nil || got.Password != "" || got.ProxyUser != "business" {
		t.Fatal("empty password or proxy identity lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = authExampleCredentials(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("credential source ignored cancellation", err)
	}
	for _, missing := range []string{"OB_USER", "OB_PASSWORD"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("OB_USER", "rotating@tenant")
			t.Setenv("OB_PASSWORD", "auth-input-secret")
			if err := os.Unsetenv(missing); err != nil {
				t.Fatal(err)
			}
			_, err := authExampleCredentials(context.Background())
			if err == nil || strings.Contains(err.Error(), "auth-input-secret") {
				t.Fatal("missing input accepted or disclosed", err)
			}
		})
	}
	t.Setenv("OB_USER", "")
	if _, err = authExampleCredentials(context.Background()); err == nil {
		t.Fatal("empty user accepted")
	}
}

func TestAuthExampleRSAFile(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherDER, err := x509.MarshalPKIXPublicKey(&other.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	valid := pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: publicDER})
	for _, tc := range []struct {
		name string
		data []byte
		ok   bool
	}{
		{"pkix", valid, true},
		{"whitespace", append(bytes.Clone(valid), '\n', '\t'), true},
		{"no-pem", []byte("auth-input-secret"), false},
		{"trailing", append(bytes.Clone(valid), []byte("auth-input-secret")...), false},
		{"leading", append([]byte("auth-input-secret\n"), valid...), false},
		{"multiple", append(bytes.Clone(valid), valid...), false},
		{"skipped-block", append([]byte("-----BEGIN PUBLIC KEY-----\ninvalid!\n-----END PUBLIC KEY-----\n"), valid...), false},
		{"headers", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Headers: map[string]string{"Comment": "test"}, Bytes: publicDER}), false},
		{"malformed", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: []byte{1, 2}}), false},
		{"wrong-type", pem.EncodeToMemory(&pem.Block{Type: authExamplePublicKeyPEM, Bytes: otherDER}), false},
		{"private-key", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "server-public.pem")
			if err := os.WriteFile(file, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := authExampleRSAKey(file)
			if !tc.ok {
				if err == nil || got != nil || strings.Contains(err.Error(), "auth-input-secret") {
					t.Fatal("bad key input accepted or disclosed", err)
				}
				return
			}
			if err != nil || got == nil || got.N.Cmp(key.N) != 0 || got.E != key.E {
				t.Fatal("public key changed", err)
			}
			if _, err = NewConnector(Config{User: testUser, Auth: AuthOptions{RSAKey: got}}); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err = authExampleRSAKey(filepath.Join(t.TempDir(), "missing.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file error lost", err)
	}
}

func TestAuthNativeRSAPaths(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, plugin := range []string{authSHA256, authCachingSHA2} {
			for _, path := range []string{"initial", "switch", authRSAChangePath} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, plugin, path), func(t *testing.T) {
					testAuthNativeRSAPath(t, key, mode, plugin, path)
				})
			}
		}
	}
}

func testAuthNativeRSAPath(t *testing.T, key *rsa.PrivateKey, mode Mode, plugin, path string) {
	t.Helper()
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	var calls atomic.Int32
	public := &rsa.PublicKey{N: new(big.Int).Set(key.N), E: key.E}
	configured, err := NewConnector(Config{Mode: mode, Auth: AuthOptions{RSAKey: public}, Credentials: func(context.Context) (Credentials, error) {
		calls.Add(1)
		return Credentials{User: testUser, Password: testPassword}, nil
	}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		serveNativeRSAPeer(ctx, t, p, key, mode, plugin, path, status)
	})})
	if err != nil {
		t.Fatal(err)
	}
	// Mutation after construction must not change the key used on the wire.
	public.N.SetInt64(1)
	public.E = 2
	if calls.Load() != 0 {
		t.Fatal("constructor obtained credentials")
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	pinned, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	if path == authRSAChangePath {
		if err = ChangeUser(context.Background(), pinned, Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err = pinned.PingContext(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatal("RSA session or credential lifecycle", err, calls.Load())
	}
}

func serveNativeRSAPeer(ctx context.Context, t *testing.T, p *wire.PacketIO, key *rsa.PrivateKey, mode Mode, plugin, path string, status byte) {
	t.Helper()
	password, nonce := testPassword, "12345678901234567890"
	if path == "initial" {
		if !sendPackets(ctx, t, p, authGreeting(plugin, false)) {
			return
		}
		response, e := p.ReadMessage(ctx)
		if e != nil || len(response) < 36 || string(response[32:36]) != "app\x00" {
			t.Error("missing RSA handshake", e)
			return
		}
		if plugin == authSHA256 {
			if len(response) < 295 || !bytes.Equal(response[36:39], []byte{252, 0, 1}) || !checkPinnedCipher(t, key, response[39:295], nonce, password) {
				t.Error("missing length-encoded RSA ciphertext")
				return
			}
		}
	} else {
		if !sendPackets(ctx, t, p, authGreeting(authNative, false)) || !readAuthResponse(ctx, t, p) {
			return
		}
		if path == authRSAChangePath {
			if !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) {
				return
			}
			ctx = context.Background()
			if !readCommand(ctx, t, p, changeUserPacket(t, mode, "")) {
				return
			}
			password = changeUserPassword
		}
		nonce = "abcdefghijklmnopqrst"
		switchPacket := append(append(append([]byte{254}, plugin...), 0), nonce...)
		if !sendPackets(ctx, t, p, append(switchPacket, 0)) {
			return
		}
		response, e := p.ReadMessage(ctx)
		if e != nil {
			t.Error(e)
			return
		}
		if plugin == authSHA256 && !checkPinnedCipher(t, key, response, nonce, password) {
			return
		}
	}
	if plugin == authCachingSHA2 {
		if !sendPackets(ctx, t, p, []byte{1, 4}) {
			return
		}
		ciphertext, e := p.ReadMessage(ctx)
		if e != nil || !checkPinnedCipher(t, key, ciphertext, nonce, password) {
			t.Error("missing full-auth RSA response", e)
			return
		}
	}
	if !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) {
		return
	}
	ctx = context.Background()
	if !serveOK(ctx, t, p, []byte{14}, status) {
		return
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func checkPinnedCipher(t *testing.T, key *rsa.PrivateKey, ciphertext []byte, nonce, password string) bool {
	t.Helper()
	plain, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, ciphertext, nil)
	if err != nil {
		t.Error("ciphertext cannot be decrypted by provisioned key", err)
		return false
	}
	for i := range plain {
		plain[i] ^= nonce[i%len(nonce)]
	}
	if !bytes.Equal(plain, append([]byte(password), 0)) {
		t.Error("RSA response used wrong nonce or credentials")
		return false
	}
	return true
}

func TestAuthNativeCredentialRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			t.Setenv("OB_USER", testUser)
			t.Setenv("OB_PASSWORD", testPassword)
			t.Setenv("OB_PROXY_USER", "")
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			var calls, dials atomic.Int32
			configured, err := NewConnector(Config{User: "fallback", Password: "fallback", Mode: mode,
				Auth: AuthOptions{RSAKey: &key.PublicKey}, Credentials: func(ctx context.Context) (Credentials, error) {
					calls.Add(1)
					return authExampleCredentials(ctx)
				}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					index := dials.Add(1)
					user, password := testUser, testPassword
					if index == 2 {
						user, password = changeUserName, changeUserPassword
					} else if index != 1 {
						t.Error("unexpected replacement connection")
						return
					}
					if !sendPackets(ctx, t, p, authGreeting(authSHA256, false)) {
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
					if !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) {
						return
					}
					ctx = context.Background()
					for range 2 {
						if !serveOK(ctx, t, p, []byte{14}, status) {
							return
						}
					}
					p.ResetSequence()
					serveQuit(t, p)
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
				t.Setenv("OB_USER", changeUserName)
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
	result, err := runAuthTest(t, Config{User: testUser, Password: testPassword, Credentials: authExampleCredentials}, func(ctx context.Context, p *wire.PacketIO) {
		if !sendPackets(ctx, t, p, authGreeting(authNative, false)) {
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

func TestAuthJavaDSNRejected(t *testing.T) {
	for _, option := range []string{"credentialType", "userKey", "pwdKey", "serverRsaPublicKeyFile", "allowPublicKeyRetrieval"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=auth-input-secret"); err == nil || strings.Contains(err.Error(), "auth-input-secret") {
			t.Fatal("unsupported Java option accepted or disclosed", option, err)
		}
	}
}

func TestAuthNativeRSARejection(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []string{authSHA256, authCachingSHA2} {
		for _, path := range []string{"no-pinned-key", authRSAUnsolicitedKey, authRSAOversizedPassword} {
			t.Run(plugin+"/"+path, func(t *testing.T) {
				cfg := Config{User: testUser, Password: testPassword, Auth: AuthOptions{RSAKey: &key.PublicKey}}
				if path == "no-pinned-key" {
					cfg.Auth.RSAKey = nil
				}
				if path == authRSAOversizedPassword {
					cfg.Password = strings.Repeat("x", 256)
				}
				result, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, authGreeting(plugin, false)) {
						return
					}
					if plugin == authCachingSHA2 || path == authRSAUnsolicitedKey {
						if !readAuthResponse(ctx, t, p) {
							return
						}
						packet := []byte{1, 4}
						if path == authRSAUnsolicitedKey {
							packet = append([]byte{1}, []byte("-----BEGIN PUBLIC KEY-----\nserver-supplied-key")...)
						}
						if !sendPackets(ctx, t, p, packet) {
							return
						}
					}
					if response, e := p.ReadMessage(ctx); e == nil || len(response) != 0 {
						t.Error("rejected RSA path wrote an auth response or key retrieval request")
					}
				})
				want := ErrTLSRequired
				switch path {
				case authRSAUnsolicitedKey:
					want = wire.ErrMalformed
				case authRSAOversizedPassword:
					want = rsa.ErrMessageTooLong
				}
				if result != nil || !errors.Is(err, want) {
					t.Fatal("RSA rejection lost cause", err)
				}
			})
		}
	}
}
