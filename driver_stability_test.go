package oceanbase

import (
	"container/list"
	"context"
	"database/sql"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const stabilitySamples = 8

// Fixed sample count keeps the testing tree bounded even for long workloads.
func TestDriverResourceStability(t *testing.T) {
	iterations := 2
	if value := os.Getenv("OB_TEST_STABILITY_ITERATIONS"); value != "" {
		var err error
		iterations, err = strconv.Atoi(value)
		if err != nil || iterations < 1 || iterations > 10000 {
			t.Fatal("OB_TEST_STABILITY_ITERATIONS must be in [1,10000]")
		}
	}
	started := time.Now()
	for sample := 0; sample <= stabilitySamples; sample++ {
		if sample > 0 {
			for _, mode := range []Mode{ModeMySQL, ModeOracle} {
				for _, scenario := range driverBenchmarkCases(mode) {
					name := fmt.Sprintf("sample-%d/mode-%d/%s", sample, mode, scenario.name)
					if !t.Run(name, func(t *testing.T) {
						scenario.run(t, mode, iterations, func() {}, func() {})
					}) {
						t.FailNow()
					}
				}
			}
		}
		// Compare live heap at quiescent boundaries, never cumulative allocation totals.
		runtime.GC()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		t.Logf("resource_sample sample=%d iterations=%d elapsed_ms=%d heap_alloc=%d heap_objects=%d goroutines=%d",
			sample, iterations, time.Since(started).Milliseconds(), memory.HeapAlloc, memory.HeapObjects, runtime.NumGoroutine())
	}
}

// The counters are hard assertions; process-wide heap/goroutine samples are observations.
func benchmarkIdleError(c *conn, stats sql.DBStats) error {
	if stats.OpenConnections != 1 || stats.InUse != 0 || stats.Idle != 1 {
		return fmt.Errorf("benchmark leaked a pool connection lease")
	}
	return benchmarkSessionError(c)
}

func benchmarkSessionError(c *conn) error {
	if c.closed.Load() || c.activeTx != nil || c.responseActive || len(c.resources.records) != 0 {
		return fmt.Errorf("benchmark leaked a connection lease, transaction, response or resource")
	}
	if len(c.statements.entries) != c.statements.order.Len() || c.statements.order.Len() > c.cfg.Execution.StatementCacheSize {
		return fmt.Errorf("benchmark statement cache exceeds its bound or has inconsistent indexes")
	}
	for entry := c.statements.order.Front(); entry != nil; entry = entry.Next() {
		s := entry.Value.(*stmt)
		if s.closed || s.closeRequested || s.leases != 0 || c.statements.entries[s.plan.query] != entry {
			return fmt.Errorf("benchmark cached an invalid or leased statement")
		}
	}
	if c.descriptorBytes < 0 || c.descriptorBytes > c.cfg.Limits.MaxMessageBytes {
		return fmt.Errorf("benchmark type descriptors exceed their byte budget")
	}
	return nil
}

func checkBenchmarkIdle(t testing.TB, physical *conn, stats sql.DBStats) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Borrowing from database/sql here would reset the session and mask leaked resources.
	if err := physical.lock(ctx); err != nil {
		t.Error(err)
		return
	}
	defer physical.unlock()
	if err := benchmarkIdleError(physical, stats); err != nil {
		t.Error(err)
	}
}

func TestDriverResourceGuard(t *testing.T) {
	for _, name := range []string{"clean", "lease", "transaction", "unread-response", "resource", "cache", "cached-lease", "closed", "descriptors"} {
		t.Run(name, func(t *testing.T) {
			c := &conn{}
			stats := sql.DBStats{OpenConnections: 1, Idle: 1}
			switch name {
			case "lease":
				stats.InUse, stats.Idle = 1, 0
			case "transaction":
				c.activeTx = &tx{}
			case "unread-response":
				c.responseActive = true
			case "resource":
				c.resources.records = map[*resourceEpoch]*resourceRecord{new(resourceEpoch): {}}
			case "cache":
				c.statements.order.PushBack(&stmt{})
			case "cached-lease":
				c.cfg.Execution.StatementCacheSize = 1
				s := &stmt{plan: bindPlan{query: benchmarkPreparedSQL}, leases: 1}
				entry := c.statements.order.PushBack(s)
				c.statements.entries = map[string]*list.Element{benchmarkPreparedSQL: entry}
			case "closed":
				c.closed.Store(true)
			case "descriptors":
				c.descriptorBytes = 1
			}
			if err := benchmarkIdleError(c, stats); (err == nil) != (name == "clean") {
				t.Fatal("resource guard did not detect injected ownership violation:", err)
			}
		})
	}
}
