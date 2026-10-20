package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type poolPeer struct {
	sessions atomic.Int32
	pings    atomic.Int32
	closed   atomic.Int32
}

// Keep real driver initialization, reset and close in every pool lifecycle check.
func newPoolTestDB(t *testing.T, mode Mode, configure func(*Config)) (*sql.DB, *poolPeer) {
	t.Helper()
	peer := new(poolPeer)
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	cfg := Config{User: testUser, Mode: mode, Session: SessionOptions{InitSQL: []string{readySQL}}}
	cfg.DialContext = peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		peer.sessions.Add(1)
		if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) ||
			!serveOK(ctx, t, p, []byte("\x03SET @ready=1"), status) ||
			!serveOK(ctx, t, p, []byte("\x03SET NAMES utf8mb4"), status) ||
			!serveOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status) {
			return
		}
		ctx = context.Background()
		for {
			p.ResetSequence()
			data, err := p.ReadMessage(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if len(data) == 1 && data[0] == 1 {
				peer.closed.Add(1)
				if _, err = p.ReadMessage(ctx); err == nil {
					t.Error("QUIT did not close transport")
				}
				return
			}
			if len(data) != 1 || data[0] != 14 {
				t.Errorf("unexpected pool command: %x", data)
				return
			}
			peer.pings.Add(1)
			if err = p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
				t.Error(err)
				return
			}
		}
	})
	if configure != nil {
		configure(&cfg)
	}
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	return db, peer
}

func TestPoolExampleWarmup(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			db, peer := newPoolTestDB(t, mode, nil)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(3)
			db.SetMaxIdleConns(3)
			if peer.sessions.Load() != 0 {
				t.Fatal("OpenDB eagerly connected")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := poolExampleWarmup(ctx, db, 0); err != nil || peer.sessions.Load() != 0 {
				t.Fatal("zero warmup should remain lazy", err)
			}
			if err := poolExampleWarmup(ctx, db, -1); err == nil || peer.sessions.Load() != 0 {
				t.Fatal("negative warmup count accepted", err)
			}
			if err := poolExampleWarmup(ctx, db, 3); err != nil {
				t.Fatal(err)
			}
			if got := db.Stats(); got.OpenConnections != 3 || got.InUse != 0 || got.Idle != 3 || peer.sessions.Load() != 3 || peer.pings.Load() != 3 {
				t.Fatalf("warmup reused one connection or leaked a lease: %+v, sessions=%d, pings=%d", got, peer.sessions.Load(), peer.pings.Load())
			}
			if err := db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			if peer.sessions.Load() != 3 {
				t.Fatal("warmed session was not reused")
			}
			db.SetMaxIdleConns(0)
			poolAwait(t, func() bool { return peer.closed.Load() == 3 })
			if got := db.Stats(); got.OpenConnections != 0 || got.MaxIdleClosed != 3 || peer.closed.Load() != 3 {
				t.Fatalf("idle sessions not physically closed: %+v, QUIT=%d", got, peer.closed.Load())
			}
		})
	}
}

func TestPoolExampleWarmupFailure(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			failed := errors.New("pool dial rejected")
			var attempts atomic.Int32
			db, peer := newPoolTestDB(t, mode, func(cfg *Config) {
				dial := cfg.DialContext
				cfg.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) {
					if attempts.Add(1) == 2 {
						return nil, failed
					}
					return dial(ctx, n, a)
				}
			})
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(2)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := poolExampleWarmup(ctx, db, 2); !errors.Is(err, failed) {
				t.Fatal(err)
			}
			if got := db.Stats(); got.InUse != 0 || got.Idle != 1 {
				t.Fatalf("failed warmup leaked acquired session: %+v", got)
			}
			if err := db.PingContext(ctx); err != nil || peer.sessions.Load() != 1 {
				t.Fatal("failed warmup lost reusable connection", err)
			}
			if err := poolExampleWarmup(ctx, db, 3); err == nil || attempts.Load() != 2 {
				t.Fatal("warmup exceeding capacity must fail before dialing", err)
			}
		})
	}
}

func TestPoolExampleWarmupCancellation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			db, peer := newPoolTestDB(t, mode, nil)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(2)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			held, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			warmCtx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- poolExampleWarmup(warmCtx, db, 2) }()
			poolAwait(t, func() bool { return db.Stats().WaitCount == 1 })
			waiting := time.Now()
			poolAwait(t, func() bool { return time.Since(waiting) >= 10*time.Millisecond })
			stop()
			if err = <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if got := db.Stats(); got.InUse != 1 || got.Idle != 1 || got.WaitDuration <= 0 || peer.sessions.Load() != 2 {
				t.Fatalf("cancel did not release only warmup leases: %+v", got)
			}
			if err = held.PingContext(ctx); err != nil {
				t.Fatal("warmup cancellation interrupted another borrower", err)
			}
		})
	}
}

func TestPoolExampleWarmupPingFailure(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			status := byte(2)
			if mode == ModeOracle {
				status = 6
			}
			connector, err := NewConnector(Config{User: testUser, Mode: mode, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
					return
				}
				ctx = context.Background()
				if !readCommand(ctx, t, p, []byte{14}) || !sendPackets(ctx, t, p, []byte{255, 0x28, 0x04, '#', 'H', 'Y', '0', '0', '0', 'b', 'u', 's', 'y'}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var serverError *Error
			if err = poolExampleWarmup(ctx, db, 1); !errors.As(err, &serverError) || serverError.Code != 1064 {
				t.Fatal("readiness error lost", err)
			}
			if got := db.Stats(); got.InUse != 0 {
				t.Fatalf("failed readiness ping leaked lease: %+v", got)
			}
		})
	}
}

func TestPoolBoundedWaitAndReuse(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			db, peer := newPoolTestDB(t, mode, nil)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			held, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			waitCtx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() { done <- db.PingContext(waitCtx) }()
			poolAwait(t, func() bool { return db.Stats().WaitCount == 1 })
			stop()
			if err = <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if peer.sessions.Load() != 1 || peer.pings.Load() != 0 {
				t.Fatal("canceled waiter dialed or wrote on held connection")
			}
			if err = held.Close(); err != nil {
				t.Fatal(err)
			}
			if err = db.PingContext(ctx); err != nil || peer.sessions.Load() != 1 {
				t.Fatal("pool did not reuse physical session", err)
			}
		})
	}
}

func TestPoolLifetimeAndIdleExpiry(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode]+"/lifetime", func(t *testing.T) {
			db, peer := newPoolTestDB(t, mode, nil)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			held, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			db.SetConnMaxLifetime(time.Millisecond)
			started := time.Now()
			poolAwait(t, func() bool { return time.Since(started) >= 10*time.Millisecond })
			if err = held.PingContext(ctx); err != nil || peer.closed.Load() != 0 {
				t.Fatal("lifetime expiry interrupted a pinned session", err)
			}
			if err = held.Close(); err != nil {
				t.Fatal(err)
			}
			// Only the original session is under the tiny lifetime; new sessions must be usable.
			db.SetConnMaxLifetime(0)
			if err = db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			poolAwait(t, func() bool { return peer.closed.Load() == 1 })
			if got := db.Stats(); got.MaxLifetimeClosed != 1 || peer.closed.Load() != 1 || peer.sessions.Load() != 2 {
				t.Fatalf("expired session not replaced: %+v, sessions=%d, QUIT=%d", got, peer.sessions.Load(), peer.closed.Load())
			}
		})
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode]+"/idle", func(t *testing.T) {
			db, peer := newPoolTestDB(t, mode, nil)
			defer func() { _ = db.Close() }()
			db.SetConnMaxIdleTime(time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			poolAwait(t, func() bool {
				stats := db.Stats()
				return stats.MaxIdleTimeClosed == 1 && peer.closed.Load() == 1 && stats.OpenConnections == 0 && stats.InUse == 0
			})
			if got := db.Stats(); got.OpenConnections != 0 || got.InUse != 0 {
				t.Fatalf("idle cleaner kept physical session: %+v", got)
			}
		})
	}
}

func TestPoolIdleValidation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, enabled := range []bool{false, true} {
			t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode]+"/"+map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
				db, peer := newPoolTestDB(t, mode, func(cfg *Config) {
					if enabled {
						cfg.Routing.ValidationInterval = time.Millisecond
					}
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := db.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				poolAwait(t, func() bool { return time.Since(started) >= 10*time.Millisecond })
				if err := db.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				want := int32(2)
				if enabled {
					want = 3
				}
				if peer.pings.Load() != want || peer.sessions.Load() != 1 {
					t.Fatalf("checkout validation pings=%d want=%d, sessions=%d", peer.pings.Load(), want, peer.sessions.Load())
				}
			})
		}
	}
}

func TestPoolModeIsolation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		db, peer := newPoolTestDB(t, mode, func(cfg *Config) { cfg.Mode = ModeAuto })
		defer func() { _ = db.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := db.PingContext(ctx); err != nil || peer.sessions.Load() != 1 {
			cancel()
			t.Fatal("independent auto-mode pool inherited another pool's mode", err)
		}
		cancel()
	}
}

var poolAwait = testpeer.Await
