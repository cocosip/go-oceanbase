package oceanbase

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

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDSNSkipVersionProbe(t *testing.T) {
	for _, value := range []string{dsnTrue, dsnFalse} {
		cfg, err := ParseDSN("oceanbase://app@localhost/?skipVersionProbe=" + value)
		if err != nil || cfg.Session.SkipVersionProbe != (value == dsnTrue) {
			t.Fatal("version probe policy rejected", err)
		}
		if _, err = NewConnector(cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"skipVersionProbe=", "skipVersionProbe=TRUE", "skipVersionProbe=1", "skipVersionProbe=true&skipVersionProbe=false", "connectProxy=true"} {
		_, err := ParseDSN("oceanbase://app@localhost/?" + query)
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatal("invalid version policy accepted", query, err)
		}
	}
	created, err := NewConnector(Config{User: testUser})
	if err != nil || created.(*connector).cfg.Session.SkipVersionProbe {
		t.Fatal("version probing disabled by default", err)
	}
}

func serveVersionProbeBaseline(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, skip bool) bool {
	t.Helper()
	if !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) {
		return false
	}
	if !skip && !serveVersion(ctx, t, p, status, false) {
		return false
	}
	return serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}

func TestVersionProbeSessions(t *testing.T) {
	for _, skip := range []bool{false, true} {
		for _, status := range []byte{2, 6} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%t/%d/%s", skip, status, framing), func(t *testing.T) {
					var dials atomic.Int32
					peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
						ctx := context.Background()
						if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveVersionProbeBaseline(ctx, t, p, status, skip) || !serveOK(ctx, t, p, []byte{14}, status) || !serveOK(ctx, t, p, []byte{14}, status) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					cfg := Config{User: testUser, Session: SessionOptions{SkipVersionProbe: skip}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						return peer(ctx, network, addr)
					}}
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
					cfg.Session.SkipVersionProbe = !skip
					db := sql.OpenDB(created)
					defer func() { _ = db.Close() }()
					for range 2 {
						pinned, err := db.Conn(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						if err = pinned.Raw(func(raw any) error {
							c := raw.(*conn)
							want := testOutVersion
							if skip {
								want = ""
							}
							if c.session.obVersion != want || c.session.mode != modeFromStatus(status) {
								return fmt.Errorf("probe changed actual mode or cached version: %d %q", c.session.mode, c.session.obVersion)
							}
							return nil
						}); err != nil {
							t.Error(err)
						}
						if err = pinned.PingContext(context.Background()); err != nil {
							t.Error(err)
						}
						if err = pinned.Close(); err != nil {
							t.Fatal(err)
						}
					}
					if dials.Load() != 1 {
						t.Fatal("version policy broke pool reuse", dials.Load())
					}
				})
			}
		}
	}
}

func TestVersionProbeChangeUser(t *testing.T) {
	for _, status := range []byte{2, 6} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			created, err := NewConnector(Config{User: testUser, Session: SessionOptions{SkipVersionProbe: true}, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				ctx := context.Background()
				if !serveConnect(ctx, t, p, status) || !serveVersionProbeBaseline(ctx, t, p, status, true) || !readCommand(ctx, t, p, changeUserPacket(t, modeFromStatus(status), "")) || !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveVersionProbeBaseline(ctx, t, p, status, true) || !serveOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
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
			if err = ChangeUser(context.Background(), pinned, Credentials{User: changeUserName, Password: changeUserPassword}, ""); err != nil {
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
	created, err := NewConnector(Config{User: testUser, Session: SessionOptions{SkipVersionProbe: true}, Execution: ExecutionOptions{PrepareExecute: true, StatementChecksum: true}, DialContext: peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		ctx := context.Background()
		if !serveConnect(ctx, t, p, 6) || !serveVersionProbeBaseline(ctx, t, p, 6, true) || !serveVersion(ctx, t, p, 6, false) || !servePrepareStatus(ctx, t, p, query, 81, 1, nil, 6) || !serveOK(ctx, t, p, execute, 6) || !readCommand(ctx, t, p, stmtPacket(25, 81)) || !serveOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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
	if _, err = OpenCursor(ctx, pinned, "SELECT v FROM t", CursorOptions{Mode: CursorSensitive}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unknown version enabled sensitive cursor", err)
	}
	if err = XAStart(ctx, pinned, XID{FormatID: 1, GlobalID: []byte("x")}, XANoFlags); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unknown version enabled Oracle XA", err)
	}
	var output int
	if _, err = Execute(ctx, pinned, "CALL p(?)", sql.Out{Dest: &output}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("unknown version enabled Oracle OUT", err)
	}
	statement, err := pinned.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = statement.Close() }()
	source := strings.NewReader("long-value")
	if _, err = statement.ExecContext(ctx, &LongData{Reader: source, Length: int64(source.Len())}); !errors.Is(err, ErrUnsupported) || source.Len() != len("long-value") {
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
				peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
					ctx := context.Background()
					if !serveConnect(ctx, t, p, status) || !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !serveOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) {
						return
					}
					query := transactionUTCZone
					if !skip {
						query = "SELECT ob_version()"
						if status == 6 {
							query += " FROM dual"
						}
					}
					if !readQuery(ctx, t, p, query) || !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'x'}) {
						return
					}
					if data, err := p.ReadMessage(ctx); len(data) != 0 || !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
						t.Error("failed initialization retained connection", err)
					}
				})
				created, err := NewConnector(Config{User: testUser, Session: SessionOptions{SkipVersionProbe: skip}, Routing: RoutingOptions{MaxAttempts: 2}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					dials.Add(1)
					return peer(ctx, network, addr)
				}})
				if err != nil {
					t.Fatal(err)
				}
				connected, err := created.Connect(context.Background())
				var server *Error
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
			cfg := Config{User: testUser, Session: SessionOptions{SkipVersionProbe: true, Variables: map[string]string{testSessionVariable: "600"}, InitSQL: []string{readySQL}}}
			if status == 6 {
				cfg.Session.NCharset = charsetUTF8MB4
			}
			cfg.DialContext = peerDial(t, func(_ context.Context, p *wire.PacketIO) {
				ctx := context.Background()
				if !serveConnect(ctx, t, p, status) || !serveVersionProbeBaseline(ctx, t, p, status, true) {
					return
				}
				if status == 6 && !serveOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf8mb4'"), status) {
					return
				}
				assignment := "600"
				if status == 6 {
					assignment = testQuotedTimeout
				}
				if status == 2 && !sessionScalarTyped(ctx, t, p, testReadSessionVariable, status, 8, "28800") {
					return
				}
				if !serveOK(ctx, t, p, []byte("\x03SET SESSION wait_timeout = "+assignment), status) || !serveOK(ctx, t, p, []byte("\x03"+readySQL), status) || !serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) || !serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
					return
				}
				if status == 6 && !serveOK(ctx, t, p, []byte("\x03SET NCHARACTER_SET_CONNECTION = 'utf8mb4'"), status) {
					return
				}
				if !serveOK(ctx, t, p, []byte{14}, status) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			created, err := NewConnector(cfg)
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
