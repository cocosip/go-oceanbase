package oceanbase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const concurrentStabilityConnections = 3

func TestDriverConcurrentSessionStability(t *testing.T) {
	iterations := sessionStabilityIterations(t)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{driverFixtureStandard, driverFixtureCompression, driverFixtureOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				runConcurrentSessionStability(t, mode, framing, iterations)
			})
		}
	}
}

func runConcurrentSessionStability(t *testing.T, mode oceanbase.Mode, framing string, iterations int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	defer cancel()
	step := max(1, (iterations+stabilitySamples-1)/stabilitySamples)
	barriers := concurrentSessionBarriers(ctx, iterations, step)
	db, dials, finished, closeTransports := newConcurrentStabilityDB(ctx, t, mode, framing, iterations, barriers)
	var pinned []*sql.Conn
	var physical []driverPhysical
	defer func() {
		cancel()
		if t.Failed() {
			closeTransports()
		}
		for _, c := range pinned {
			if err := c.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				t.Error(err)
			}
		}
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	for range concurrentStabilityConnections {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		pinned = append(pinned, c)
		if err = c.Raw(func(raw any) error { physical = append(physical, raw.(driverPhysical)); return nil }); err != nil {
			t.Fatal(err)
		}
		if err = c.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	sample := func(cycles int) {
		checkConcurrentStability(t, db, physical, dials.Load(), concurrentStabilityConnections)
		runtime.GC()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		t.Logf("concurrent_session_resource_sample cycles=%d iterations=%d connections=%d elapsed_ms=%d heap_alloc=%d heap_objects=%d goroutines=%d dials=%d",
			cycles, iterations, len(physical), time.Since(started).Milliseconds(), memory.HeapAlloc, memory.HeapObjects, runtime.NumGoroutine(), dials.Load())
	}
	sample(0)
	for start := 0; start < iterations; start += step {
		end := min(start+step, iterations)
		if err := runConcurrentSessionBatch(ctx, cancel, pinned, physical, iterations, start, end); err != nil {
			t.Fatal(err)
		}
		sample(end)
	}
	for _, c := range pinned {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	checkConcurrentStability(t, db, physical, dials.Load(), 0)
	reborrowConcurrentSessions(ctx, t, db, physical)
	checkConcurrentStability(t, db, physical, dials.Load(), 0)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 || stats.Idle != 0 {
		t.Fatal("concurrent workload did not close its pool", stats)
	}
	for _, c := range physical {
		if c.IsValid() {
			t.Fatal("concurrent workload left a physical session open")
		}
	}
	closed, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for range physical {
		select {
		case <-finished:
		case <-closed.Done():
			t.Fatal("concurrent peer did not observe transport close before test cleanup")
		}
	}
}

func concurrentSessionBarriers(ctx context.Context, iterations, step int) map[int]func() bool {
	barriers := make(map[int]func() bool)
	for start := 0; start < iterations; start += step {
		var arrivals atomic.Int32
		ready := make(chan struct{})
		barriers[start] = func() bool {
			if arrivals.Add(1) == concurrentStabilityConnections {
				close(ready)
			}
			// Every physical session must have an execution in flight before any receives its OK.
			select {
			case <-ready:
				return true
			case <-ctx.Done():
				return false
			}
		}
	}
	return barriers
}

func newConcurrentStabilityDB(ctx context.Context, t *testing.T, mode oceanbase.Mode, framing string, iterations int, barriers map[int]func() bool) (*sql.DB, *atomic.Int32, <-chan struct{}, func()) {
	t.Helper()
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	peers := make([]func(context.Context, string, string) (net.Conn, error), concurrentStabilityConnections)
	finished := make(chan struct{}, concurrentStabilityConnections)
	for worker := range peers {
		peers[worker] = testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
			defer func() { finished <- struct{}{} }()
			if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
				return
			}
			for cycle := 0; cycle < iterations; cycle++ {
				// Disjoint IDs and values detect cross-session statement/type-cache contamination.
				i := worker*(iterations+1) + cycle
				if !serveDriverSessionCycle(ctx, t, p, mode, i, barriers[cycle]) {
					return
				}
			}
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) || !testpeer.ReadCommand(ctx, t, p, []byte{1}) {
				return
			}
			// net.Pipe may report ErrClosedPipe when setting a deadline after the peer closes.
			if _, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				t.Error("concurrent peer did not observe transport close after QUIT", err)
			}
		}, 2*time.Minute)
	}
	dials := new(atomic.Int32)
	transports := make([]net.Conn, concurrentStabilityConnections)
	cfg := oceanbase.Config{User: driverFixtureUser, Mode: mode, Execution: oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}}
	cfg.Protocol = sessionStabilityProtocol(framing)
	cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		i := int(dials.Add(1)) - 1
		if i >= len(peers) {
			return nil, errors.New("concurrent workload replaced a physical session")
		}
		raw, err := peers[i](ctx, network, addr)
		if err == nil {
			transports[i] = raw
		}
		return raw, err
	}
	configured, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	db.SetMaxOpenConns(concurrentStabilityConnections)
	db.SetMaxIdleConns(concurrentStabilityConnections)
	return db, dials, finished, func() {
		for _, raw := range transports {
			if raw != nil {
				_ = raw.Close()
			}
		}
	}
}

func runConcurrentSessionBatch(ctx context.Context, cancel context.CancelFunc, pinned []*sql.Conn, physical []driverPhysical, iterations, start, end int) error {
	results := make([]error, len(pinned))
	var workers sync.WaitGroup
	for worker, session := range pinned {
		workers.Go(func() {
			for cycle := start; cycle < end; cycle++ {
				err := runDriverSessionCycle(ctx, session, physical[worker], worker*(iterations+1)+cycle)
				if err == nil {
					err = benchmarkSessionError(physical[worker])
				}
				if err != nil {
					cancel()
					results[worker] = fmt.Errorf("worker %d cycle %d: %w", worker, cycle, err)
					return
				}
			}
		})
	}
	workers.Wait()
	return errors.Join(results...)
}

func checkConcurrentStability(t *testing.T, db *sql.DB, physical []driverPhysical, dials int32, inUse int) {
	t.Helper()
	stats := db.Stats()
	if stats.OpenConnections != concurrentStabilityConnections || stats.InUse != inUse ||
		stats.Idle != concurrentStabilityConnections-inUse || dials != concurrentStabilityConnections {
		t.Fatal("concurrent workload leaked or replaced a pool session", stats, dials)
	}
	for _, c := range physical {
		if err := benchmarkSessionError(c); err != nil {
			t.Fatal(err)
		}
	}
}

func reborrowConcurrentSessions(ctx context.Context, t *testing.T, db *sql.DB, physical []driverPhysical) {
	t.Helper()
	seen := make(map[driverPhysical]bool)
	for range physical {
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// Keep every lease until all originals have been borrowed, rather than reusing one idle slot.
		defer func() {
			if closeErr := c.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}()
		if err = c.Raw(func(raw any) error {
			original := raw.(driverPhysical)
			if !slices.Contains(physical, original) || seen[original] {
				return errors.New("pool reuse did not preserve distinct original physical sessions")
			}
			seen[original] = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err = c.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
