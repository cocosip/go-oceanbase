package connect_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestReadTimeoutTLS(t *testing.T) {
	for _, stage := range []string{"handshake", "query"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			clientTLS, serverTLS := testpeer.TLSConfig(t)
			configured, err := oceanbase.NewConnector(config.Config{User: testUser, ReadTimeout: 100 * time.Millisecond, TLSConfig: clientTLS, DialContext: func(dialCtx context.Context, _, _ string) (net.Conn, error) {
				return testpeer.RoutingTLSPeer(dialCtx, t, func(server net.Conn) {
					p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
					if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, true)) || !testpeer.ReadAuthResponse(ctx, t, p) {
						return
					}
					if stage == "handshake" {
						var data [1024]byte
						for {
							if _, e := server.Read(data[:]); e != nil {
								return
							}
						}
					}
					secure := tls.Server(server, serverTLS)
					if e := secure.HandshakeContext(ctx); e != nil {
						t.Error(e)
						return
					}
					if e := p.ReplaceConn(testpeer.AuthPeerConn{Conn: secure}); e != nil {
						t.Error(e)
						return
					}
					if !testpeer.ReadAuthResponse(ctx, t, p) || !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(2, nil)) || !testpeer.ServeBaseline(ctx, t, p, 2) || !testpeer.ReadQuery(ctx, t, p, "SELECT waiting") {
						return
					}
					if _, e := p.ReadMessage(ctx); e == nil {
						t.Error("TLS timeout did not close the session")
					}
				})
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			_, err = db.ExecContext(ctx, "SELECT waiting")
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil {
				t.Fatal("TLS path ignored independent read timeout", err)
			}
		})
	}
}
