package publictest

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// RawConn exposes only the standard driver contracts used by protocol tests.
type RawConn interface {
	driver.Conn
	driver.ConnPrepareContext
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
	driver.Pinger
	driver.SessionResetter
	driver.Validator
	driver.NamedValueChecker
}

// OutputTestConn establishes the small Oracle session used by direct driver tests.
func OutputTestConn(t *testing.T, caps uint64, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) RawConn {
	return WrappedOutputTestConn(t, caps, serve, nil, configure...)
}

// WrappedOutputTestConn injects a transport fault while retaining the public handshake path.
func WrappedOutputTestConn(t *testing.T, caps uint64, serve func(context.Context, *wire.PacketIO), wrap func(net.Conn) net.Conn, configure ...func(*config.Config)) RawConn {
	return outputTestConn(t, "4.3.5.1", caps, serve, wrap, configure...)
}

// VersionedOutputTestConn exercises version-dependent behavior through the version probe.
func VersionedOutputTestConn(t *testing.T, version string, caps uint64, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) RawConn {
	return outputTestConn(t, version, caps, serve, nil, configure...)
}

func outputTestConn(t *testing.T, version string, caps uint64, serve func(context.Context, *wire.PacketIO), wrap func(net.Conn) net.Conn, configure ...func(*config.Config)) RawConn {
	t.Helper()
	cfg := config.Config{Mode: config.ModeOracle, Limits: config.Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
	for _, apply := range configure {
		apply(&cfg)
	}
	c := wrappedConnector(t, cfg, version, caps, serve, wrap)
	raw, err := c.Connect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return CheckedConn(raw.(RawConn))
}

// ConfiguredDB runs a public API workload with its configuration fixed before dialing.
func ConfiguredDB(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	return sql.OpenDB(configuredConnector(t, cfg, "4.3.5.1", 0, serve))
}

// QueryVersionDB supplies a real version-probe response, or skips the probe for an unknown version.
func QueryVersionDB(t *testing.T, version string, serve func(context.Context, *wire.PacketIO), configure ...func(*config.Config)) *sql.DB {
	t.Helper()
	cfg := config.Config{}
	for _, apply := range configure {
		apply(&cfg)
	}
	cfg.Session.SkipVersionProbe = version == ""
	return sql.OpenDB(configuredConnector(t, cfg, version, 0, serve))
}

func configuredConnector(t *testing.T, cfg config.Config, version string, caps uint64, serve func(context.Context, *wire.PacketIO)) driver.Connector {
	return wrappedConnector(t, cfg, version, caps, serve, nil)
}

func wrappedConnector(t *testing.T, cfg config.Config, version string, caps uint64, serve func(context.Context, *wire.PacketIO), wrap func(net.Conn) net.Conn) driver.Connector {
	t.Helper()
	cfg.User = testpeer.User
	if cfg.Mode == 0 {
		cfg.Mode = config.ModeMySQL
	}
	status := byte(2)
	if cfg.Mode == config.ModeOracle {
		status = 6
	}
	cfg.DialContext = testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, status, caps) {
			return
		}
		charset := cfg.Session.Charset
		if charset == "" {
			charset = "utf8mb4"
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES "+charset), status) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET autocommit=1"), status) {
			return
		}
		if !cfg.Session.SkipVersionProbe {
			query := "SELECT ob_version()"
			if cfg.Mode == config.ModeOracle {
				query += " FROM dual"
			}
			if !testpeer.ReadQuery(ctx, t, p, query) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("version", 253, 45, 0)) {
				return
			}
			if caps&wire.ClientDeprecateEOF == 0 && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
				return
			}
			end := []byte{254, 0, 0, status, 0}
			if caps&wire.ClientDeprecateEOF != 0 {
				end = append(end, 0, 0)
			}
			if !testpeer.SendPackets(ctx, t, p, append([]byte{byte(len(version))}, []byte(version)...), end) {
				return
			}
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		serve(context.Background(), p)
	})
	if wrap != nil {
		dial := cfg.DialContext
		cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			raw, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return wrap(raw), nil
		}
	}
	c, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
