package oceanbase_test

import (
	"database/sql"
	driver "database/sql/driver"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
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
			for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
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
func benchmarkIdleError(c driver.Validator, stats sql.DBStats) error {
	if stats.OpenConnections != 1 || stats.InUse != 0 || stats.Idle != 1 {
		return fmt.Errorf("benchmark leaked a pool connection lease")
	}
	return benchmarkSessionError(c)
}

func benchmarkSessionError(c driver.Validator) error {
	if !c.IsValid() {
		return fmt.Errorf("benchmark physical session is invalid")
	}
	return nil
}
func checkBenchmarkIdle(t testing.TB, physical driver.Validator, stats sql.DBStats) {
	t.Helper()
	if err := benchmarkIdleError(physical, stats); err != nil {
		t.Error(err)
	}
}
