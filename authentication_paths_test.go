package oceanbase

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const oracleCountsPath = "oracle-counts"
const serverErrorPath = "server-error"
const unknownPluginPath = "unknown-plugin"
const badMorePath = "bad-more"
const malformedSwitchPath = "malformed-switch"

const derivedNamePath = "derived-name"
const invalidCredentialsPath = "invalid-credentials"
const callbackErrorPath = "callback-error"

func testTLSConfig(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: testServerName}, DNSNames: []string{testServerName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{ServerName: testServerName, RootCAs: roots}, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func TestTLSVerifiedAuthentication(t *testing.T) {
	clientCfg, serverCfg := testTLSConfig(t)
	for _, path := range []string{"trusted", derivedNamePath, "bad-name", "bad-chain"} {
		t.Run(path, func(t *testing.T) {
			cfg := cloneTLSConfig(clientCfg)
			good := path == "trusted" || path == derivedNamePath
			if path == "bad-name" {
				cfg.ServerName = "other.example"
			}
			if path == "bad-chain" {
				cfg.RootCAs = x509.NewCertPool()
			}
			if path == derivedNamePath {
				cfg.ServerName = ""
			}
			result, err := runAuthConnTest(t, Config{Addr: "db.example:2881", User: testUser, Password: testPassword, TLSConfig: cfg}, nil, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, authGreeting(authClear, true)); err != nil {
					t.Error(err)
					return
				}
				request, err := p.ReadMessage(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if len(request) != 32 || binary.LittleEndian.Uint32(request)&0x800 == 0 {
					t.Errorf("invalid SSLRequest: %x", request)
					return
				}
				secure := tls.Server(raw, serverCfg)
				if err = secure.HandshakeContext(ctx); err != nil {
					if good {
						t.Error(err)
					}
					return
				}
				if !good {
					t.Error("untrusted handshake succeeded")
					return
				}
				if err = p.ReplaceConn(authPeerConn{secure}); err != nil {
					t.Error(err)
					return
				}
				response, err := p.ReadMessage(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if len(response) < 45 || !bytes.Contains(response[32:], []byte("secret\x00")) {
					t.Errorf("missing encrypted credentials: %x", response)
					return
				}
				if err = p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
					t.Error(err)
				}
			})
			if good {
				if err != nil || !result.tlsVerified {
					t.Fatalf("verified TLS rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("verification failure silently downgraded")
			}
		})
	}
}

type fakeSecureConn struct{ net.Conn }

func (fakeSecureConn) ConnectionState() tls.ConnectionState {
	return tls.ConnectionState{HandshakeComplete: true, VerifiedChains: [][]*x509.Certificate{{{}}}}
}

func TestAuthRejectsForgedTLS(t *testing.T) {
	_, err := runAuthConnTest(t, Config{User: testUser, Password: testPassword}, func(raw net.Conn) net.Conn { return fakeSecureConn{raw} }, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authClear, false)); err != nil {
			t.Error(err)
			return
		}
		if b, err := p.ReadMessage(ctx); err == nil || len(b) > 0 {
			t.Error("custom transport forged verified TLS")
		}
	})
	if !errors.Is(err, ErrTLSRequired) {
		t.Fatal(err)
	}
}

func TestAuthPinnedRSA(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range []string{authSHA256, authCachingSHA2} {
		t.Run(plugin, func(t *testing.T) {
			_, err := runAuthTest(t, Config{User: testUser, Password: testPassword, Auth: AuthOptions{RSAKey: &key.PublicKey}}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, authGreeting(plugin, false)); err != nil {
					t.Error(err)
					return
				}
				response, err := p.ReadMessage(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				var encrypted []byte
				if plugin == authSHA256 {
					// 32-byte header + "app\0" + length-encoded 256-byte RSA ciphertext.
					if len(response) < 295 || !bytes.Equal(response[36:39], []byte{252, 0, 1}) {
						t.Errorf("bad auth length: %x", response)
						return
					}
					encrypted = response[39:295]
				} else {
					if err = p.WriteMessage(ctx, []byte{1, 4}); err != nil {
						t.Error(err)
						return
					}
					encrypted, err = p.ReadMessage(ctx)
					if err != nil {
						t.Error(err)
						return
					}
				}
				plain, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, key, encrypted, nil)
				if err != nil || !bytes.Equal(plain, []byte{0x42, 0x57, 0x50, 0x46, 0x50, 0x42, 0x37}) {
					t.Errorf("wrong RSA encrypted password: %x %v", plain, err)
					return
				}
				if err = p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
					t.Error(err)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuthRefreshCredentialsAndFactory(t *testing.T) {
	var refreshed, created atomic.Int32
	cfg := Config{Credentials: func(context.Context) (Credentials, error) {
		refreshed.Add(1)
		return Credentials{User: "fresh", Password: testPassword}, nil
	}, Auth: AuthOptions{Plugins: map[string]AuthFactory{testCustomPlugin: func() AuthPlugin { created.Add(1); return &freshAuthPlugin{} }}}}
	for range 2 {
		_, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
			if err := p.WriteMessage(ctx, authGreeting(testCustomPlugin, false)); err != nil {
				t.Error(err)
				return
			}
			response, err := p.ReadMessage(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(response[32:40], []byte{'f', 'r', 'e', 's', 'h', 0, 1, 0xaa}) {
				t.Errorf("wrong credentials/response: %x", response)
				return
			}
			if err = p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
				t.Error(err)
			}
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if refreshed.Load() != 2 || created.Load() != 2 {
		t.Fatal("credentials or plugin state reused")
	}
}

type freshAuthPlugin struct{ used bool }

func (*freshAuthPlugin) Security(AuthChallenge) AuthSecurity { return AuthChallengeResponse }
func (p *freshAuthPlugin) Respond(_ context.Context, c Credentials, _ AuthChallenge) ([]byte, error) {
	if p.used || c.Password != testPassword {
		return nil, errors.New("plugin state or credentials invalid")
	}
	p.used = true
	return []byte{0xaa}, nil
}

func TestAuthFailureBoundaries(t *testing.T) {
	for _, path := range []string{unknownPluginPath, malformedSwitchPath, badMorePath, serverErrorPath, oracleCountsPath, invalidCredentialsPath, callbackErrorPath} {
		t.Run(path, func(t *testing.T) {
			cfg := Config{User: testUser, Password: testPassword}
			expected := wire.ErrMalformed
			plugin := authNative
			switch path {
			case unknownPluginPath:
				plugin = "unknown"
				expected = ErrUnsupported
			case oracleCountsPath:
				cfg.RowsAffectedMode = ChangedRows
				expected = nil
			case serverErrorPath:
				expected = nil
			case invalidCredentialsPath:
				cfg.Credentials = func(context.Context) (Credentials, error) { return Credentials{}, nil }
				expected = nil
			case callbackErrorPath:
				expected = errors.New("refresh failed")
				cfg.Credentials = func(context.Context) (Credentials, error) { return Credentials{}, expected }
			}
			_, err := runAuthTest(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, authGreeting(plugin, false)); err != nil {
					t.Error(err)
					return
				}
				if path == unknownPluginPath || path == invalidCredentialsPath || path == callbackErrorPath {
					if _, err := p.ReadMessage(ctx); err == nil {
						t.Error("failure wrote handshake")
					}
					return
				}
				if _, err := p.ReadMessage(ctx); err != nil {
					t.Error(err)
					return
				}
				response := []byte{0xfe, 'b', 'a', 'd'}
				if path == badMorePath {
					response = []byte{1, 4}
				}
				if path == serverErrorPath {
					response = []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}
				}
				if path == oracleCountsPath {
					response = []byte{0, 0, 0, 6, 0, 0, 0}
				}
				if err := p.WriteMessage(ctx, response); err != nil {
					t.Error(err)
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("failed authentication kept transport open")
				}
			})
			if err == nil {
				t.Fatal("failure became success")
			}
			if expected != nil && !errors.Is(err, expected) {
				t.Fatalf("wrong failure: %v", err)
			}
			if path == serverErrorPath {
				var server *Error
				if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "28000" || server.Message != "denied" {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestAuthFastSuccess(t *testing.T) {
	_, err := runAuthTest(t, Config{User: testUser, Password: testPassword}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authCachingSHA2, false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		if err := p.WriteMessage(ctx, []byte{1, 3}); err != nil {
			t.Error(err)
			return
		}
		if err := p.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthFastSuccessRequiresFinalOK(t *testing.T) {
	_, err := runAuthTest(t, Config{User: testUser, Password: testPassword}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authCachingSHA2, false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err != nil {
			t.Error(err)
			return
		}
		for range 2 {
			if err := p.WriteMessage(ctx, []byte{1, 3}); err != nil {
				t.Error(err)
				return
			}
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("repeated fast-auth success accepted")
		}
	})
	if !errors.Is(err, wire.ErrMalformed) {
		t.Fatalf("missing final-OK state: %v", err)
	}
}

func TestAuthUnknownCharset(t *testing.T) {
	_, err := runAuthTest(t, Config{User: testUser, Session: SessionOptions{Charset: "unavailable"}}, func(ctx context.Context, p *wire.PacketIO) {
		if err := p.WriteMessage(ctx, authGreeting(authNative, false)); err != nil {
			t.Error(err)
			return
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("unknown charset silently ignored")
		}
	})
	if !errors.Is(err, ErrUnsupported) || strings.Contains(err.Error(), "unavailable") {
		t.Fatal(err)
	}
}

func TestAuthInitialServerError(t *testing.T) {
	for _, tc := range []struct {
		name           string
		data           []byte
		code           uint32
		state, message string
		malformed      bool
	}{
		{"before-greeting", append([]byte{255, 16, 4}, []byte("Too many connections")...), 1040, "HY000", "Too many connections", false},
		{"protocol41", []byte{255, 21, 4, '#', '2', '8', '0', '0', '0', 'n', 'o'}, 1045, "28000", "no", false},
		{name: "truncated-code", data: []byte{255, 1}, malformed: true},
		{name: "truncated-state", data: []byte{255, 21, 4, '#', '2'}, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runAuthTest(t, Config{User: testUser}, func(ctx context.Context, p *wire.PacketIO) {
				if err := p.WriteMessage(ctx, tc.data); err != nil {
					t.Error(err)
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("initial ERR retained connection")
				}
			})
			if tc.malformed {
				if !errors.Is(err, wire.ErrMalformed) {
					t.Fatal(err)
				}
				return
			}
			var server *Error
			if !errors.As(err, &server) || server.Code != tc.code || server.SQLState != tc.state || server.Message != tc.message {
				t.Fatalf("server error lost: %v", err)
			}
		})
	}
}
