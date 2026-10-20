package connect_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTLSNativePolicy(t *testing.T) {
	_, server := testpeer.TLSConfig(t)
	caFile, certFile, keyFile := tlsClientFiles(t, server)
	for _, version := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
			for _, callback := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/callback=%t", tls.VersionName(version), map[config.Mode]string{config.ModeMySQL: "mysql-mode", config.ModeOracle: "oracle-mode"}[mode], callback)
				t.Run(name, func(t *testing.T) {
					client, err := tlsExampleConfig(caFile, certFile, keyFile, testServerName)
					if err != nil {
						t.Fatal(err)
					}
					client.MinVersion, client.MaxVersion = version, version
					client.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
					var certificateCalls atomic.Int32
					if callback {
						certificate := client.Certificates[0]
						client.Certificates = nil
						client.GetClientCertificate = func(request *tls.CertificateRequestInfo) (*tls.Certificate, error) {
							certificateCalls.Add(1)
							return &certificate, request.SupportsCertificate(&certificate)
						}
					}
					var verifyCalls atomic.Int32
					client.VerifyConnection = func(state tls.ConnectionState) error {
						verifyCalls.Add(1)
						if state.Version != version || len(state.VerifiedChains) == 0 || state.ServerName != testServerName {
							return errors.New("TLS policy was not preserved")
						}
						return nil
					}
					peer := configspec.CloneTLSConfig(server)
					peer.MinVersion, peer.MaxVersion = version, version
					peer.ClientAuth, peer.ClientCAs = tls.RequireAndVerifyClientCert, client.RootCAs
					peer.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
					var observed atomic.Bool
					result, err := runAuthConnTest(t, config.Config{User: testUser, Password: testPassword, Mode: mode, TLSConfig: client}, nil,
						func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
							if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
								return
							}
							secure := tls.Server(raw, peer)
							if e := secure.HandshakeContext(ctx); e != nil {
								t.Error(e)
								return
							}
							state := secure.ConnectionState()
							if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) != 2 || state.PeerCertificates[0].Subject.CommonName != tlsClientName || state.Version != version || version == tls.VersionTLS12 && state.CipherSuite != tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
								t.Error("driver did not preserve client identity, chain or TLS policy")
								return
							}
							if e := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); e != nil {
								t.Error(e)
								return
							}
							data, e := p.ReadMessage(ctx)
							if e != nil || !bytes.Contains(data, []byte(testPassword+"\x00")) {
								t.Error("verified channel did not carry authentication", e)
								return
							}
							observed.Store(true)
							status := byte(2)
							if mode == config.ModeOracle {
								status = 6
							}
							testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, nil))
						})
					if err != nil || result == nil || !result.TLSVerified || result.Mode != mode || !observed.Load() || verifyCalls.Load() != 1 || callback && certificateCalls.Load() != 1 {
						t.Fatal("TLS configuration did not reach authentication", err, verifyCalls.Load(), certificateCalls.Load())
					}
				})
			}
		}
	}
}

func TestTLSNativeRejection(t *testing.T) {
	_, server := testpeer.TLSConfig(t)
	caFile, certFile, keyFile := tlsClientFiles(t, server)
	policyError := errors.New("application TLS policy rejected peer")
	for _, path := range []string{tlsVerifyCallbackPath, tlsCertificateCallbackPath, "protocol", "cipher"} {
		t.Run(path, func(t *testing.T) {
			client, err := tlsExampleConfig(caFile, certFile, keyFile, testServerName)
			if err != nil {
				t.Fatal(err)
			}
			peer := configspec.CloneTLSConfig(server)
			client.MinVersion, client.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
			peer.MinVersion, peer.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
			peer.ClientAuth, peer.ClientCAs = tls.RequireAndVerifyClientCert, client.RootCAs
			switch path {
			case tlsVerifyCallbackPath:
				client.VerifyConnection = func(tls.ConnectionState) error { return policyError }
			case tlsCertificateCallbackPath:
				client.Certificates = nil
				client.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return nil, policyError }
			case "protocol":
				peer.MinVersion, peer.MaxVersion = tls.VersionTLS13, tls.VersionTLS13
			case "cipher":
				client.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384}
				peer.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
			}
			var credentials atomic.Int32
			result, err := runAuthConnTest(t, config.Config{User: testUser, TLSConfig: client,
				Credentials: func(context.Context) (config.Credentials, error) {
					credentials.Add(1)
					return config.Credentials{User: testUser, Password: testPassword}, nil
				}}, nil, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
				if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
					return
				}
				if e := tls.Server(raw, peer).HandshakeContext(ctx); e == nil {
					t.Error("TLS rejection silently negotiated a different policy")
				}
			})
			if result != nil || err == nil || credentials.Load() != 0 {
				t.Fatal("TLS rejection obtained credentials or succeeded", err, credentials.Load())
			}
			if (path == tlsVerifyCallbackPath || path == tlsCertificateCallbackPath) && !errors.Is(err, policyError) {
				t.Fatal("TLS policy callback error was lost", err)
			}
		})
	}
}

func TestTLSNativeSnapshot(t *testing.T) {
	_, server := testpeer.TLSConfig(t)
	caFile, certFile, keyFile := tlsClientFiles(t, server)
	client, err := tlsExampleConfig(caFile, certFile, keyFile, testServerName)
	if err != nil {
		t.Fatal(err)
	}
	client.MinVersion, client.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	client.CipherSuites = []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}
	peer := configspec.CloneTLSConfig(server)
	peer.ClientAuth, peer.ClientCAs = tls.RequireAndVerifyClientCert, client.RootCAs
	peer.MinVersion, peer.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	configured, err := oceanbase.NewConnector(config.Config{User: testUser, Password: testPassword, TLSConfig: client, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return testpeer.RoutingTLSPeer(ctx, t, func(raw net.Conn) {
			p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: raw}, 64<<20)
			if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
				return
			}
			secure := tls.Server(raw, peer)
			if e := secure.HandshakeContext(ctx); e != nil {
				t.Error(e)
				return
			}
			if secure.ConnectionState().CipherSuite != tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
				t.Error("caller mutation changed the TLS cipher policy")
				return
			}
			if e := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); e != nil {
				t.Error(e)
				return
			}
			if !testpeer.ReadAuthResponse(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, nil)) || !testpeer.ServeBaseline(ctx, t, p, 2) {
				return
			}
			ctx = context.Background()
			p.ResetSequence()
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	client.ServerName = "changed.example"
	client.RootCAs = x509.NewCertPool()
	client.MaxVersion = tls.VersionTLS13
	client.MinVersion = tls.VersionTLS13
	client.CipherSuites[0] = tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384
	client.Certificates[0].Certificate[0][0] ^= 0xff
	raw, err := configured.Connect(context.Background())
	if err != nil {
		t.Fatal("caller mutation corrupted the TLS snapshot", err)
	}
	if err = raw.(driver.Pinger).Ping(context.Background()); err != nil {
		t.Error(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTLSNativeCancellation(t *testing.T) {
	_, server := testpeer.TLSConfig(t)
	caFile, certFile, keyFile := tlsClientFiles(t, server)
	client, err := tlsExampleConfig(caFile, certFile, keyFile, testServerName)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entered := make(chan struct{})
	client.Certificates = nil
	client.GetClientCertificate = func(request *tls.CertificateRequestInfo) (*tls.Certificate, error) {
		close(entered)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	server.ClientAuth, server.ClientCAs = tls.RequireAndVerifyClientCert, client.RootCAs
	var credentials atomic.Int32
	configured, err := oceanbase.NewConnector(config.Config{User: testUser, TLSConfig: client, Credentials: func(context.Context) (config.Credentials, error) {
		credentials.Add(1)
		return config.Credentials{User: testUser, Password: testPassword}, nil
	}, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		return testpeer.RoutingTLSPeer(dialCtx, t, func(raw net.Conn) {
			p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: raw}, 64<<20)
			if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
				return
			}
			if e := tls.Server(raw, server).HandshakeContext(ctx); e == nil {
				t.Error("canceled client certificate selection completed authentication")
			}
		})
	}})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	result, err := configured.Connect(ctx)
	cancel()
	<-stopped
	if result != nil || !errors.Is(err, context.Canceled) || credentials.Load() != 0 {
		t.Fatal("TLS callback lost cancellation or obtained credentials", err, credentials.Load())
	}
	select {
	case <-entered:
	default:
		t.Fatal("certificate selection was never reached")
	}
}

func TestTLSNativeRootSnapshot(t *testing.T) {
	client, _ := testpeer.TLSConfig(t)
	_, alternate := testpeer.TLSConfig(t)
	extraRoot, err := x509.ParseCertificate(alternate.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	var credentials atomic.Int32
	result, err := runAuthConnTest(t, config.Config{User: testUser, TLSConfig: client, Credentials: func(context.Context) (config.Credentials, error) {
		credentials.Add(1)
		return config.Credentials{User: testUser, Password: testPassword}, nil
	}}, func(raw net.Conn) net.Conn {
		// runAuthConnTest has already constructed the Connector before applying this wrapper.
		client.RootCAs.AddCert(extraRoot)
		return raw
	}, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
		if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
			return
		}
		if e := tls.Server(raw, alternate).HandshakeContext(ctx); e == nil {
			t.Error("caller added a new trusted authority after Connector creation")
		}
	})
	var untrusted x509.UnknownAuthorityError
	if result != nil || !errors.As(err, &untrusted) || credentials.Load() != 0 {
		t.Fatal("caller mutation expanded Connector trust", err, credentials.Load())
	}
}
