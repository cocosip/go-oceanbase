package connect_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// A rejected authentication or initialization must not send later session commands or retry.
func TestInitializationErrorBoundary(t *testing.T) {
	for _, status := range []byte{2, 6} {
		assignment := "600"
		variableQuery := testReadSessionVariable
		if status == 6 {
			variableQuery += sessionReadDualSuffix
		}
		queries := []string{transactionUTF8Names, testAutocommitOn, transactionUTCZone}
		queries = append(queries, variableQuery)
		queries = append(queries, "SET SESSION wait_timeout = "+assignment, readySQL)
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for failed := -1; failed < len(queries); failed++ {
				t.Run(fmt.Sprintf("%d/%s/%d", status, framing, failed), func(t *testing.T) {
					peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
						ctx := context.Background()
						if failed < 0 {
							if !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolGreeting(framing == testCompression)) || !testpeer.ReadAuthResponse(ctx, t, p) {
								return
							}
						} else {
							if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) {
								return
							}
							for _, query := range queries[:failed] {
								if query == variableQuery {
									if !sessionScalarTyped(ctx, t, p, query, status, 8, "28800") {
										return
									}
									continue
								}
								if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, query...), status) {
									return
								}
							}
							if !testpeer.ReadQuery(ctx, t, p, queries[failed]) {
								return
							}
						}
						if !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'e'}) {
							return
						}
						p.ResetSequence()
						if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
							t.Errorf("command sent after rejected initialization: %x %v", data, err)
						}
					})
					var dials atomic.Int32
					cfg := config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: true, Variables: map[string]string{testSessionVariable: "600"}, InitSQL: []string{readySQL}}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						return peer(ctx, network, addr)
					}}
					cfg.Routing = config.RoutingOptions{Endpoints: []config.Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Nanosecond}
					if framing == testCompression {
						cfg.Protocol.Compression = config.FeatureRequired
					}
					if framing == testOB20 {
						cfg.Protocol.OB20 = config.FeatureRequired
					}
					created, err := oceanbase.NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(created)
					defer func() { _ = db.Close() }()
					err = db.PingContext(context.Background())
					var server *oceanbase.Error
					if !errors.As(err, &server) || server.Code != 1064 || server.SQLState != "42000" || dials.Load() != 1 || db.Stats().OpenConnections != 0 {
						t.Fatal("failed initialization lost rejection or entered the pool", err, db.Stats(), dials.Load())
					}
				})
			}
		}
	}
}
