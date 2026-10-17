package oceanbase

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"
)

func ExampleNewConnector_pool() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With("pool", "orders")
	connector, err := NewConnector(Config{
		User: "pool_app", Mode: ModeMySQL, Logger: logger,
		// Set deployment credentials, address and TLSConfig before connecting.
	})
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(10 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Optional one-time readiness; this does not maintain a minimum pool size.
	if err = poolExampleWarmup(ctx, db, 2); err != nil {
		panic(err)
	}
	stats := db.Stats()
	logger.InfoContext(ctx, "database pool ready", "open", stats.OpenConnections, "in_use", stats.InUse, "idle", stats.Idle)
	// No Output: compiled only; the application owns DB lifetime and metrics collection.
}

// Application helper: hold distinct leases until all readiness checks complete.
func poolExampleWarmup(ctx context.Context, db *sql.DB, count int) error {
	if limit := db.Stats().MaxOpenConnections; count < 0 || (limit > 0 && count > limit) {
		return fmt.Errorf("warmup count %d exceeds pool capacity or is negative", count)
	}
	var held []*sql.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for range count {
		c, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		held = append(held, c)
		if err = c.PingContext(ctx); err != nil {
			return err
		}
	}
	return nil
}
