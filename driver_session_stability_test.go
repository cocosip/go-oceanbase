package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	sessionStabilityUpdate   = "UPDATE stability_fixture SET v=?"
	sessionStabilitySelect   = "SELECT v FROM stability_fixture"
	sessionStabilityMutation = "UPDATE stability_fixture SET v=1"
)

// Keep the original physical session across mixed operations, including recoverable SQL errors.
func TestDriverSessionStability(t *testing.T) {
	iterations := 8
	if value := os.Getenv("OB_TEST_STABILITY_ITERATIONS"); value != "" {
		var err error
		iterations, err = strconv.Atoi(value)
		if err != nil || iterations < 1 || iterations > 10000 {
			t.Fatal("OB_TEST_STABILITY_ITERATIONS must be in [1,10000]")
		}
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				runDriverSessionStability(t, mode, framing, iterations)
			})
		}
	}
}

func runDriverSessionStability(t *testing.T, mode Mode, framing string, iterations int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	t.Cleanup(cancel)
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	peer := peerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
		for i := 0; i < iterations; i++ {
			if !serveDriverSessionCycle(ctx, t, p, mode, i) {
				return
			}
		}
		p.ResetSequence()
		serveQuit(t, p)
	}, 2*time.Minute)
	var dials atomic.Int32
	cfg := Config{User: testUser, Mode: mode, Execution: ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}}
	cfg.Protocol = ProtocolOptions{Compression: FeatureOff, OB20: FeatureOff}
	if framing == testCompression {
		cfg.Protocol.Compression = FeatureRequired
	}
	if framing == testOB20 {
		cfg.Protocol.OB20 = FeatureRequired
	}
	cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) != 1 {
			return nil, errors.New("mixed workload replaced its physical session")
		}
		return peer(ctx, network, addr)
	}
	configured, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var physical *conn
	pinned, err := db.Conn(ctx)
	if err == nil {
		err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil })
		err = errors.Join(err, pinned.Close())
	}
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		checkBenchmarkIdle(t, physical, db.Stats())
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 || !physical.closed.Load() {
			t.Error("mixed workload did not close its pool and physical session", stats)
		}
	})
	started := time.Now()
	sample := func(cycles int) {
		runtime.GC()
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		t.Logf("session_resource_sample cycles=%d iterations=%d elapsed_ms=%d heap_alloc=%d heap_objects=%d goroutines=%d dials=%d",
			cycles, iterations, time.Since(started).Milliseconds(), memory.HeapAlloc, memory.HeapObjects, runtime.NumGoroutine(), dials.Load())
	}
	sample(0)
	sampleStep := max(1, (iterations+stabilitySamples-1)/stabilitySamples)
	for i := 0; i < iterations; i++ {
		runDriverSessionCycle(ctx, t, db, physical, i)
		checkBenchmarkIdle(t, physical, db.Stats())
		if (i+1)%sampleStep == 0 || i+1 == iterations {
			sample(i + 1)
		}
	}
}

func serveDriverSessionCycle(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, i int) bool {
	t.Helper()
	status, active := byte(2), byte(3)
	begin := []txStep{{testMySQLReadWrite, active}}
	if mode == ModeOracle {
		status, active = 6, 5
		begin = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, active}}
	}
	id, value := uint32(2*i+1), int64(i+1)
	if !servePrepareStatus(ctx, t, p, sessionStabilityUpdate, id, 1, nil, status) ||
		!readCommand(ctx, t, p, optimizationExecute(id, value, false)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) ||
		!readCommand(ctx, t, p, optimizationExecute(id, -value, true)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) ||
		!readCommand(ctx, t, p, stmtPacket(25, id)) || !readQuery(ctx, t, p, sessionStabilitySelect) ||
		!sendPackets(ctx, t, p, []byte{1}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, status, 0}) {
		return false
	}
	for row := 0; row < 3; row++ {
		text := strconv.Itoa(i + row + 1)
		if !sendPackets(ctx, t, p, append([]byte{byte(len(text))}, text...)) {
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) || !serveTxSteps(ctx, t, p, begin) ||
		!readQuery(ctx, t, p, sessionStabilityMutation) || !sendPackets(ctx, t, p, commandPacket(1, 0, active)) {
		return false
	}
	finish := testCommitSQL
	if i%2 == 0 {
		finish = testRollbackSQL
	}
	end := []txStep{{finish, status}}
	if mode == ModeOracle {
		end = []txStep{{finish, 4}, {testAutocommitOn, status}}
	}
	if !serveTxSteps(ctx, t, p, end) || !servePrepareStatus(ctx, t, p, sessionStabilityUpdate, id+1, 1, nil, status) ||
		!readCommand(ctx, t, p, optimizationExecute(id+1, value, false)) ||
		!sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}) ||
		!readCommand(ctx, t, p, stmtPacket(25, id+1)) || !serveOK(ctx, t, p, []byte{14}, status) {
		return false
	}
	return true
}

func runDriverSessionCycle(ctx context.Context, t *testing.T, db *sql.DB, physical *conn, i int) {
	t.Helper()
	for _, value := range []int64{int64(i + 1), -int64(i + 1)} {
		result, err := db.ExecContext(ctx, sessionStabilityUpdate, value)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			t.Fatal("cached update count", n, err)
		}
	}
	rows, err := db.QueryContext(ctx, sessionStabilitySelect)
	if err != nil {
		t.Fatal(err)
	}
	if i%2 != 0 {
		var value int64
		if !rows.Next() || rows.Scan(&value) != nil || value != int64(i+1) {
			_ = rows.Close()
			t.Fatal("mixed rows value", value, rows.Err())
		}
	}
	if err = rows.Close(); err != nil || rows.Err() != nil {
		t.Fatal("early Rows.Close", err, rows.Err())
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tx.ExecContext(ctx, sessionStabilityMutation)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if n, countErr := result.RowsAffected(); countErr != nil || n != 1 {
		_ = tx.Rollback()
		t.Fatal("transaction update count", n, countErr)
	}
	if i%2 == 0 {
		err = tx.Rollback()
	} else {
		err = tx.Commit()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = physical.lock(ctx); err != nil {
		t.Fatal(err)
	}
	restored := physical.activeTx == nil && physical.session.ok.Status&3 == 2
	physical.unlock()
	if !restored {
		t.Fatal("transaction returned without restoring the original session")
	}
	_, err = db.ExecContext(ctx, sessionStabilityUpdate, i+1)
	var server *Error
	if !errors.As(err, &server) || server.Code != 1062 || server.SQLState != "23000" {
		t.Fatal("recoverable prepared error", err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("session not reusable after mixed cycle", err)
	}
}
