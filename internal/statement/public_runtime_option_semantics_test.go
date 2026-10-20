package statement_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

// A pre-send deadline must preserve the session; a sent command with no ACK must close it.
func TestStatementContextDeadline(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, framing, prepare), func(t *testing.T) {
					status := byte(2)
					if mode == oceanbase.ModeOracle {
						status = 6
					}
					db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode, PrepareMode: prepare, ReadTimeout: 2 * time.Second}, framing, func(ctx context.Context, p *wire.PacketIO) {
						if prepare == oceanbase.PrepareServer && !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, status) {
							return
						}
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
							return
						}
						if prepare == oceanbase.PrepareServer {
							if !testpeer.ReadCommand(ctx, t, p, batchExecutePacket(t, 1)) {
								return
							}
						} else if !testpeer.ReadQuery(ctx, t, p, "INSERT INTO t VALUES ( 1 )") {
							return
						}
						p.ResetSequence()
						if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
							t.Errorf("deadline sent another command: %x %v", data, err)
						}
					})
					defer func() { _ = db.Close() }()
					pinned := publictest.PinnedSession(t, db)
					defer func() { _ = pinned.Close() }()
					prepared, err := pinned.PrepareContext(context.Background(), testBatchSQL)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = prepared.Close() }()
					var physical driver.Validator
					if err = pinned.Raw(func(raw any) error { physical = raw.(driver.Validator); return nil }); err != nil {
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

// Removing interpolation spaces can turn a negative parameter into a SQL comment or join an alias.
func TestClientParameterBoundaries(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				query := "SELECT -?, ? e3, ?"
				args := []any{-1, 1, "x';DELETE FROM t;--"}
				want := "SELECT - -1 ,  1  e3,  _utf8mb4 X'78273b44454c4554452046524f4d20743b2d2d' "
				status := byte(2)
				if mode == oceanbase.ModeOracle {
					query = "SELECT -:x, :y e3, :message, :x FROM dual"
					args = []any{sql.Named("message", "x';DELETE FROM t;--"), sql.Named("y", 1), sql.Named("x", -1)}
					want = "SELECT - -1 ,  1  e3,  'x'';DELETE FROM t;--' ,  -1  FROM dual"
					status = 6
				}
				db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode, PrepareMode: oceanbase.PrepareClient}, framing, func(ctx context.Context, p *wire.PacketIO) {
					for range 2 {
						if !testpeer.ReadQuery(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) {
							return
						}
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
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
