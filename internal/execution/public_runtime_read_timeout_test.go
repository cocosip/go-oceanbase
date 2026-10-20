package execution_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestReadTimeoutCommands(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for _, rows := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/rows=%t", status, framing, rows), func(t *testing.T) {
					var dials atomic.Int32
					var commandRead atomic.Bool
					var commandReadTimeout atomic.Int64
					cfg := oceanbase.Config{User: testUser, ReadTimeout: 5 * time.Second}
					switch framing {
					case testCompression:
						cfg.Protocol.Compression = oceanbase.FeatureRequired
					case testOB20:
						cfg.Protocol.OB20 = oceanbase.FeatureRequired
					}
					query := "UPDATE once"
					if rows {
						query = "SELECT waiting"
					}
					peer := testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
						if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ReadQuery(context.Background(), t, p, query) {
							return
						}
						commandRead.Store(true)
						if rows && !testpeer.SendPackets(context.Background(), t, p, []byte{1}, testpeer.TextColumn("v", 3, 63, 0), []byte{254, 0, 0, status, 0}) {
							return
						}
						if _, err := p.ReadMessage(context.Background()); err == nil {
							t.Error("timed out command reused the session")
						}
					})
					cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						raw, err := peer(ctx, network, addr)
						if err != nil {
							return nil, err
						}
						return commandReadDeadlineConn{Conn: raw, timeout: &commandReadTimeout}, nil
					}
					configured, err := oceanbase.NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(configured)
					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					// Isolate the stalled command from compression and session initialization.
					pinned, err := db.Conn(ctx)
					if err != nil {
						t.Fatal("initialization failed before the command timeout check", err)
					}
					commandReadTimeout.Store(int64(100 * time.Millisecond))
					if err = pinned.Close(); err != nil {
						t.Fatal(err)
					}
					if rows {
						var result *sql.Rows
						result, err = db.QueryContext(ctx, query)
						if err != nil {
							t.Fatal("metadata did not arrive", err)
						}
						if result.Next() {
							t.Fatal("stalled peer returned a row")
						}
						err = result.Err()
						_ = result.Close()
					} else {
						_, err = db.ExecContext(ctx, query)
					}
					var timeout net.Error
					if !errors.As(err, &timeout) || !timeout.Timeout() || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, driver.ErrBadConn) || ctx.Err() != nil {
						t.Fatal("read timeout lost its network error or caused replay classification", err)
					}
					if !commandRead.Load() || dials.Load() != 1 || db.Stats().Idle != 0 {
						t.Fatal("command was not reached, was replayed or was pooled after timeout", commandRead.Load(), dials.Load(), db.Stats())
					}
				})
			}
		}
	}
}
