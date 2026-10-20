package connect_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestVersionProbeChangeUser(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			created, err := oceanbase.NewConnector(config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: true}, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				ctx := context.Background()
				if !testpeer.ServeConnect(ctx, t, p, status) || !serveVersionProbeBaseline(ctx, t, p, status, true) || !testpeer.ReadCommand(ctx, t, p, changeUserPacket(t, testpeer.ModeFromStatus(status), "")) || !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveVersionProbeBaseline(ctx, t, p, status, true) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			pinned, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = pinned.Close() }()
			if err = oceanbase.ChangeUser(context.Background(), pinned, config.Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
				t.Fatal(err)
			}
			if err = pinned.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVersionProbeUnknownCapabilities(t *testing.T) {
	const query = "UPDATE t SET v=?"
	execute := binary.LittleEndian.AppendUint64(append(executeHeader(81), 0, 1, 8, 0), 7)
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: true}, Execution: config.ExecutionOptions{PrepareExecute: true, StatementChecksum: true}, DialContext: testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		ctx := context.Background()
		if !testpeer.ServeConnect(ctx, t, p, 6) || !serveVersionProbeBaseline(ctx, t, p, 6, true) || !testpeer.ServeVersion(ctx, t, p, 6, false) || !servePrepareStatus(ctx, t, p, query, 81, 1, nil, 6) || !testpeer.ServeOK(ctx, t, p, execute, 6) || !testpeer.ReadCommand(ctx, t, p, stmtPacket(25, 81)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	var explicitVersion string
	if err = pinned.QueryRowContext(ctx, "SELECT ob_version() FROM dual").Scan(&explicitVersion); err != nil || explicitVersion != testOutVersion {
		t.Fatal("explicit business query was suppressed", err)
	}
	if _, err = oceanbase.OpenCursor(ctx, pinned, "SELECT v FROM t", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("unknown version enabled sensitive cursor", err)
	}
	if err = oceanbase.XAStart(ctx, pinned, oceanbase.XID{FormatID: 1, GlobalID: []byte("x")}, oceanbase.XANoFlags); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("unknown version enabled Oracle XA", err)
	}
	var output int
	if _, err = oceanbase.Execute(ctx, pinned, "CALL p(?)", sql.Out{Dest: &output}); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("unknown version enabled Oracle OUT", err)
	}
	statement, err := pinned.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = statement.Close() }()
	source := strings.NewReader("long-value")
	if _, err = statement.ExecContext(ctx, &oceanbase.LongData{Reader: source, Length: int64(source.Len())}); !errors.Is(err, dberrors.ErrUnsupported) || source.Len() != len("long-value") {
		t.Fatal("unknown version consumed Oracle streamed data", err)
	}
	if _, err = statement.ExecContext(ctx, 7); err != nil {
		t.Fatal("standard prepare/execute fallback failed", err)
	}
	if err = statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestVersionProbeFailure(t *testing.T) {
	for _, skip := range []bool{false, true} {
		for _, status := range []byte{2, 6} {
			t.Run(fmt.Sprintf("%t/%d", skip, status), func(t *testing.T) {
				var dials atomic.Int32
				peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
					ctx := context.Background()
					if !testpeer.ServeConnect(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) {
						return
					}
					query := transactionUTCZone
					if !skip {
						query = "SELECT ob_version()"
						if status == 6 {
							query += sessionReadDualSuffix
						}
					}
					if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
						return
					}
					if data, err := p.ReadMessage(ctx); len(data) != 0 || !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
						t.Error("failed initialization retained connection", err)
					}
				})
				created, err := oceanbase.NewConnector(config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: skip}, Routing: config.RoutingOptions{MaxAttempts: 2}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					dials.Add(1)
					return peer(ctx, network, addr)
				}})
				if err != nil {
					t.Fatal(err)
				}
				connected, err := created.Connect(context.Background())
				var server *oceanbase.Error
				if connected != nil || !errors.As(err, &server) || server.Code != 1064 || dials.Load() != 1 {
					t.Fatal("failed initialization was hidden or retried", dials.Load(), err)
				}
			})
		}
	}
}

func TestVersionProbeCancelQuery(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) { testCancelQueryIdentity(t, status, false, true) })
	}
}

func TestVersionProbeInitialization(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			cfg := config.Config{User: testUser, Session: config.SessionOptions{SkipVersionProbe: true, Variables: map[string]string{testSessionVariable: "600"}, InitSQL: []string{readySQL}}}
			if status == 6 {
				cfg.Session.NCharset = testUTF8MB4
			}
			cfg.DialContext = testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
				ctx := context.Background()
				if !testpeer.ServeConnect(ctx, t, p, status) || !serveVersionProbeBaseline(ctx, t, p, status, true) {
					return
				}
				if status == 6 && !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf8mb4'"), status) {
					return
				}
				assignment := "600"
				variableQuery := testReadSessionVariable
				if status == 6 {
					variableQuery += sessionReadDualSuffix
				}
				if !sessionScalarTyped(ctx, t, p, variableQuery, status, 8, "28800") {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = "+assignment), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+readySQL), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
					return
				}
				if status == 6 && !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf8mb4'"), status) {
					return
				}
				if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			created, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(created)
			defer func() { _ = db.Close() }()
			if err = db.PingContext(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
