//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestConcurrentPoolTransactions(t *testing.T) {
	rounds := 16
	if value := os.Getenv("OB_TEST_STABILITY_ROUNDS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 2000 {
			t.Fatal("OB_TEST_STABILITY_ROUNDS must be 1..2000")
		}
		rounds = n
	}
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.Execution.StatementCacheSize = 8
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "configure concurrent pool", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			const workers = 4
			db.SetMaxOpenConns(workers)
			db.SetMaxIdleConns(workers)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
			defer cancel()
			table := fmt.Sprintf("OBGPL_%d", time.Now().UnixNano())
			suffix := ""
			if p.mode == oceanbase.ModeMySQL {
				suffix = integrationInnoDBSuffix
			}
			if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(80))"+suffix); err != nil {
				failOperation(t, "create concurrent fixture", err)
			}
			defer func() {
				cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
				defer done()
				if _, err := db.ExecContext(cleanup, "DROP TABLE "+table); err != nil {
					failOperation(t, "drop concurrent fixture", err)
				}
			}()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var wg sync.WaitGroup
			failures := make(chan error, workers)
			for worker := range workers {
				wg.Go(func() {
					err := func() error {
						c, err := db.Conn(ctx)
						if err != nil {
							return err
						}
						defer func() { _ = c.Close() }()
						for round := range rounds {
							id, value := worker*rounds+round, fmt.Sprintf("worker-%d-round-%d-中文", worker, round)
							tx, err := c.BeginTx(ctx, nil)
							if err != nil {
								return err
							}
							if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES(?,?)", id, value); err != nil {
								_ = tx.Rollback()
								return err
							}
							var got string
							if err = tx.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", id).Scan(&got); err != nil || got != value {
								_ = tx.Rollback()
								return fmt.Errorf("transaction read mismatch: %w", err)
							}
							want := 1
							if round%2 == 0 {
								err = tx.Commit()
							} else {
								want, err = 0, tx.Rollback()
							}
							if err != nil {
								return err
							}
							var count int
							if err = c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=?", id).Scan(&count); err != nil || count != want {
								return fmt.Errorf("transaction outcome mismatch: count=%d want=%d error=%w", count, want, err)
							}
						}
						return c.PingContext(ctx)
					}()
					if err != nil {
						failures <- err
					}
				})
			}
			wg.Wait()
			close(failures)
			for err := range failures {
				failOperation(t, "concurrent transaction worker", err)
			}
			var count int
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != workers*((rounds+1)/2) {
				t.Fatal("final committed row count mismatch", count, err)
			}
			stats := db.Stats()
			if stats.InUse != 0 || stats.OpenConnections > workers {
				t.Fatal("pool retained a lease or exceeded its limit", stats)
			}
			runtime.ReadMemStats(&after)
			t.Logf("workers=%d transactions=%d committed=%d pool_open=%d in_use=%d heap_before=%d heap_after=%d goroutines=%d", workers, workers*rounds, count, stats.OpenConnections, stats.InUse, before.HeapAlloc, after.HeapAlloc, runtime.NumGoroutine())
		})
	}
}
