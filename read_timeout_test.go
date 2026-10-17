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

func TestDSNReadTimeout(t *testing.T) {
	for _, value := range []string{"0s", "250ms", "2s"} {
		if _, err := ParseDSN("oceanbase://app@localhost/example?readTimeout=" + value); err != nil {
			t.Fatalf("valid read timeout rejected: %v", err)
		}
	}
	for _, value := range []string{"", "-1ns", "1", "never", "999999999999999999999h", "1s&readTimeout=2s"} {
		_, err := ParseDSN("oceanbase://app@localhost/example?readTimeout=" + value)
		var invalid *ConfigError
		if !errors.As(err, &invalid) {
			t.Fatalf("invalid read timeout accepted: %v", err)
		}
	}
}

func TestReadTimeoutConfig(t *testing.T) {
	_, err := NewConnector(Config{User: testUser, ReadTimeout: -time.Nanosecond})
	var invalid *ConfigError
	if !errors.As(err, &invalid) || invalid.Field != "ReadTimeout" {
		t.Fatal("negative read timeout accepted", err)
	}
	configured, err := NewConnector(Config{User: testUser})
	if err != nil || configured.(*connector).cfg.ReadTimeout != 0 {
		t.Fatal("read timeout is not disabled by default", err)
	}
	cfg, err := ParseDSN("oceanbase://app@localhost/?readTimeout=250ms")
	if err != nil || cfg.ReadTimeout != 250*time.Millisecond {
		t.Fatal("read timeout duration lost", err)
	}
}

func TestReadTimeoutCommands(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			for _, rows := range []bool{false, true} {
				t.Run(fmt.Sprintf("%d/%s/rows=%t", status, framing, rows), func(t *testing.T) {
					var dials atomic.Int32
					cfg := Config{User: testUser, ReadTimeout: 100 * time.Millisecond}
					switch framing {
					case testCompression:
						cfg.Protocol.Compression = FeatureRequired
					case testOB20:
						cfg.Protocol.OB20 = FeatureRequired
					}
					query := "UPDATE once"
					if rows {
						query = "SELECT waiting"
					}
					peer := peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
						if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !readQuery(context.Background(), t, p, query) {
							return
						}
						if rows && !sendPackets(context.Background(), t, p, []byte{1}, textColumn("v", 3, 63, 0), []byte{254, 0, 0, status, 0}) {
							return
						}
						if _, err := p.ReadMessage(context.Background()); err == nil {
							t.Error("timed out command reused the session")
						}
					})
					cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
						dials.Add(1)
						return peer(ctx, network, addr)
					}
					configured, err := NewConnector(cfg)
					if err != nil {
						t.Fatal(err)
					}
					db := sql.OpenDB(configured)
					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
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
					if dials.Load() != 1 || db.Stats().Idle != 0 {
						t.Fatal("timed out execution was replayed or pooled", dials.Load(), db.Stats())
					}
				})
			}
		}
	}
}

func TestReadTimeoutConnect(t *testing.T) {
	for _, stage := range []string{"server-hello", "initialization"} {
		t.Run(stage, func(t *testing.T) {
			cfg := Config{User: testUser, ReadTimeout: 50 * time.Millisecond}
			cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if stage == "initialization" && (!serveConnect(ctx, t, p, 2) || !readQuery(ctx, t, p, "SET NAMES utf8mb4")) {
					return
				}
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("stalled connection sent an extra command")
				}
			})
			configured, err := NewConnector(cfg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = configured.Connect(ctx)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("connection ignored read timeout", err)
			}
		})
	}
}
