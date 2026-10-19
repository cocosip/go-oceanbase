package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// A rejected authentication or initialization must not send later session commands or retry.
func TestInitializationErrorBoundary(t *testing.T) {
	for _, status := range []byte{2, 6} {
		assignment := "600"
		if status == 6 {
			assignment = testQuotedTimeout
		}
		queries := []string{transactionUTF8Names, testAutocommitOn, transactionUTCZone}
		if status == 2 {
			queries = append(queries, testReadSessionVariable)
		}
		queries = append(queries, "SET SESSION wait_timeout = "+assignment, readySQL)
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for failed := -1; failed < len(queries); failed++ {
				t.Run(fmt.Sprintf("%d/%s/%d", status, framing, failed), func(t *testing.T) {
					peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
						ctx := context.Background()
						if failed < 0 {
							if !sendPackets(ctx, t, p, protocolGreeting(framing == testCompression)) || !readAuthResponse(ctx, t, p) {
								return
							}
						} else {
							if !serveCreateDatabaseAuth(ctx, t, p, framing, status) {
								return
							}
							for _, query := range queries[:failed] {
								if query == testReadSessionVariable {
									if !sessionScalarTyped(ctx, t, p, query, status, 8, "28800") {
										return
									}
									continue
								}
								if !serveOK(ctx, t, p, append([]byte{3}, query...), status) {
									return
								}
							}
							if !readQuery(ctx, t, p, queries[failed]) {
								return
							}
						}
						if !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'e'}) {
							return
						}
						p.ResetSequence()
						if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
							t.Errorf("command sent after rejected initialization: %x %v", data, err)
						}
					})
					var dials atomic.Int32
					cfg := Config{User: testUser, Session: SessionOptions{SkipVersionProbe: true, Variables: map[string]string{testSessionVariable: "600"}, InitSQL: []string{readySQL}}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						return peer(ctx, network, addr)
					}}
					cfg.Routing = RoutingOptions{Endpoints: []Endpoint{{Addr: routeFirst}, {Addr: routeSecond}}, RetryDelay: time.Nanosecond}
					if framing == testCompression {
						cfg.Protocol.Compression = FeatureRequired
					}
					if framing == testOB20 {
						cfg.Protocol.OB20 = FeatureRequired
					}
					created, err := NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(created)
					defer func() { _ = db.Close() }()
					err = db.PingContext(context.Background())
					var server *Error
					if !errors.As(err, &server) || server.Code != 1064 || server.SQLState != "42000" || dials.Load() != 1 || db.Stats().OpenConnections != 0 {
						t.Fatal("failed initialization lost rejection or entered the pool", err, db.Stats(), dials.Load())
					}
				})
			}
		}
	}
}

// Removing interpolation spaces can turn a negative parameter into a SQL comment or join an alias.
func TestClientParameterBoundaries(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				query := "SELECT -?, ? e3, ?"
				args := []any{-1, 1, "x';DELETE FROM t;--"}
				want := "SELECT - -1 ,  1  e3,  _utf8mb4 X'78273b44454c4554452046524f4d20743b2d2d' "
				status := byte(2)
				if mode == ModeOracle {
					query = "SELECT -:x, :y e3, :message, :x FROM dual"
					args = []any{sql.Named("message", "x';DELETE FROM t;--"), sql.Named("y", 1), sql.Named("x", -1)}
					want = "SELECT - -1 ,  1  e3,  'x'';DELETE FROM t;--' ,  -1  FROM dual"
					status = 6
				}
				db := batchRewriteDB(t, Config{Mode: mode, PrepareMode: PrepareClient}, framing, func(ctx context.Context, p *wire.PacketIO) {
					for range 2 {
						if !readQuery(ctx, t, p, want) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) {
							return
						}
					}
					if !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
					t.Fatal(err)
				}
				prepared, err := db.PrepareContext(context.Background(), query)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = prepared.Close() }()
				result, err := prepared.ExecContext(context.Background(), args...)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := result.RowsAffected(); err != nil || count != 1 {
					t.Fatal("client binding lost command result", count, err)
				}
				if err = db.PingContext(context.Background()); err != nil {
					t.Fatal("interpolation lost the response boundary", err)
				}
			})
		}
	}
}

// A pre-send deadline must preserve the session; a sent command with no ACK must close it.
func TestStatementContextDeadline(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, framing, prepare), func(t *testing.T) {
					status := byte(2)
					if mode == ModeOracle {
						status = 6
					}
					db := batchRewriteDB(t, Config{Mode: mode, PrepareMode: prepare, ReadTimeout: 2 * time.Second}, framing, func(ctx context.Context, p *wire.PacketIO) {
						if prepare == PrepareServer && !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
							return
						}
						if !serveOK(ctx, t, p, []byte{14}, status) {
							return
						}
						if prepare == PrepareServer {
							if !readCommand(ctx, t, p, batchExecutePacket(t, 1)) {
								return
							}
						} else if !readQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )") {
							return
						}
						p.ResetSequence()
						if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
							t.Errorf("deadline sent another command: %x %v", data, err)
						}
					})
					defer func() { _ = db.Close() }()
					pinned := pinnedSession(t, db)
					defer func() { _ = pinned.Close() }()
					prepared, err := pinned.PrepareContext(context.Background(), testBatchSQL)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = prepared.Close() }()
					var physical *conn
					if err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil }); err != nil {
						t.Fatal(err)
					}
					expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
					defer stop()
					if _, err = prepared.ExecContext(expired, 1); !errors.Is(err, context.DeadlineExceeded) || !physical.IsValid() {
						t.Fatal("pre-send deadline poisoned the session", err)
					}
					if err = pinned.PingContext(context.Background()); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
					defer cancel()
					if _, err = prepared.ExecContext(ctx, 1); !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) || physical.IsValid() {
						t.Fatal("sent deadline lost its cause or allowed replay/pooling", err)
					}
					if err = prepared.Close(); err != nil {
						t.Fatal(err)
					}
					if err = pinned.Close(); err != nil {
						t.Fatal(err)
					}
					if stats := db.Stats(); stats.Idle != 0 || stats.OpenConnections != 0 {
						t.Fatal("unknown command outcome returned to idle pool")
					}
				})
			}
		}
	}
}

func TestLegacyExecutionOptionsRejected(t *testing.T) {
	for _, option := range []string{"usePipelineAuth", "enableQueryTimeouts", "encloseParamInParentheses", "serverAffinityOrder"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=true"); err == nil {
			t.Fatal("Java option silently accepted instead of its Go API", option)
		}
	}
}
