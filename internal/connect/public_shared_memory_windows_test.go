//go:build windows

package connect_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPlatformTransportSharedMemoryVerifiedTLS(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprint(status, "/", trusted), func(t *testing.T) {
				client, server := testpeer.TLSConfig(t)
				if !trusted {
					client.ServerName = "wrong.invalid"
				}
				db := sharedMemoryDBConn(t, config.Config{Password: testPassword, TLSConfig: client}, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
					if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
						return
					}
					secure := tls.Server(raw, server)
					if err := secure.HandshakeContext(ctx); err != nil {
						if trusted {
							t.Error(err)
						}
						return
					}
					if err := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); err != nil {
						t.Error(err)
						return
					}
					data, err := p.ReadMessage(ctx)
					if !trusted {
						if err == nil || len(data) != 0 {
							t.Error("untrusted IPC target received DB credentials")
						}
						return
					}
					if err != nil || !bytes.Contains(data, []byte(testPassword+"\x00")) {
						t.Error("verified IPC authentication missing", err)
						return
					}
					if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, nil)) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				if err := db.PingContext(t.Context()); (err == nil) != trusted {
					t.Fatal("IPC TLS identity check", err)
				}
			})
		}
	}
}

func TestPlatformTransportSharedMemorySessions(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprint(status, "/", framing), func(t *testing.T) {
				cfg := config.Config{}
				if framing == testCompression {
					cfg.Protocol.Compression = config.FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = config.FeatureRequired
				}
				db := sharedMemoryDB(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				if err := db.PingContext(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestPlatformTransportSharedMemoryTLSGate(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			cfg := config.Config{Password: testPassword}
			if configured {
				cfg.TLSConfig = &tls.Config{ServerName: "database.example", MinVersion: tls.VersionTLS12}
			}
			db := sharedMemoryDB(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Clear, false)) {
					return
				}
				if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
					t.Error("shared memory sent unprotected credentials")
				}
			})
			if err := db.PingContext(t.Context()); !errors.Is(err, dberrors.ErrTLSRequired) {
				t.Fatal(err)
			}
		})
	}
}
