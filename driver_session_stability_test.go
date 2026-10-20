package oceanbase_test

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

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	sessionStabilityUpdate   = "UPDATE stability_fixture SET v=?"
	sessionStabilitySelect   = "SELECT v FROM stability_fixture"
	sessionStabilityMutation = "UPDATE stability_fixture SET v=1"
)

type stabilitySession interface {
	oceanbase.Session
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	PingContext(context.Context) error
}

func sessionStabilityProtocol(framing string) oceanbase.ProtocolOptions {
	protocol := oceanbase.ProtocolOptions{Compression: oceanbase.FeatureOff, OB20: oceanbase.FeatureOff}
	if framing == driverFixtureCompression {
		protocol.Compression = oceanbase.FeatureRequired
	}
	if framing == driverFixtureOB20 {
		protocol.OB20 = oceanbase.FeatureRequired
	}
	return protocol
}

func sessionStabilityIterations(t *testing.T) int {
	t.Helper()
	iterations := 8
	if value := os.Getenv("OB_TEST_STABILITY_ITERATIONS"); value != "" {
		var err error
		iterations, err = strconv.Atoi(value)
		if err != nil || iterations < 1 || iterations > 10000 {
			t.Fatal("OB_TEST_STABILITY_ITERATIONS must be in [1,10000]")
		}
	}
	return iterations
}

// Keep the original physical session across mixed operations, including recoverable SQL errors.
func TestDriverSessionStability(t *testing.T) {
	iterations := sessionStabilityIterations(t)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{driverFixtureStandard, driverFixtureCompression, driverFixtureOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				runDriverSessionStability(t, mode, framing, iterations)
			})
		}
	}
}

func runDriverSessionStability(t *testing.T, mode oceanbase.Mode, framing string, iterations int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	t.Cleanup(cancel)
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	peer := testpeer.PeerDial(t, func(_ context.Context, p *wire.PacketIO) {
		if !testpeer.ServeCreateDatabaseAuth(ctx, t, p, framing, status) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
			return
		}
		for i := 0; i < iterations; i++ {
			if !serveDriverSessionCycle(ctx, t, p, mode, i, nil) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	}, 2*time.Minute)
	var dials atomic.Int32
	var transport *driverTrackedConn
	cfg := oceanbase.Config{User: driverFixtureUser, Mode: mode, Execution: oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true}}
	cfg.Protocol = sessionStabilityProtocol(framing)
	cfg.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if dials.Add(1) != 1 {
			return nil, errors.New("mixed workload replaced its physical session")
		}
		raw, err := peer(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		transport = &driverTrackedConn{Conn: raw}
		return transport, nil
	}
	configured, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(configured)
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var physical driverPhysical
	pinned, err := db.Conn(ctx)
	if err == nil {
		err = pinned.Raw(func(raw any) error { physical = raw.(driverPhysical); return nil })
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
		if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 || physical.IsValid() || !transport.closed.Load() {
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
		if err = runDriverSessionCycle(ctx, db, physical, i); err != nil {
			t.Fatal(err)
		}
		checkBenchmarkIdle(t, physical, db.Stats())
		if (i+1)%sampleStep == 0 || i+1 == iterations {
			sample(i + 1)
		}
	}
}

func serveDriverSessionCycle(ctx context.Context, t *testing.T, p *wire.PacketIO, mode oceanbase.Mode, i int, barrier func() bool) bool {
	t.Helper()
	status, active := byte(2), byte(3)
	begin := []publictest.TxStep{{Query: driverFixtureMySQLReadWrite, Status: active}}
	if mode == oceanbase.ModeOracle {
		status, active = 6, 5
		begin = []publictest.TxStep{{Query: driverFixtureAutocommitOff, Status: 4}, {Query: driverFixtureOracleReadWrite, Status: active}}
	}
	id, value := uint32(2*i+1), int64(i+1)
	if !publictest.ServePrepareStatus(ctx, t, p, sessionStabilityUpdate, id, 1, nil, status) ||
		!testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(id, value, false)) {
		return false
	}
	if barrier != nil && !barrier() {
		return false
	}
	if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) ||
		!testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(id, -value, true)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) ||
		!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, id)) || !testpeer.ReadQuery(ctx, t, p, sessionStabilitySelect) ||
		!testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, status, 0}) {
		return false
	}
	for row := 0; row < 3; row++ {
		text := strconv.Itoa(i + row + 1)
		if !testpeer.SendPackets(ctx, t, p, append([]byte{byte(len(text))}, text...)) {
			return false
		}
	}
	if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) || !publictest.ServeTxSteps(ctx, t, p, begin) ||
		!testpeer.ReadQuery(ctx, t, p, sessionStabilityMutation) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, active)) {
		return false
	}
	finish := driverFixtureCommitSQL
	if i%2 == 0 {
		finish = driverFixtureRollbackSQL
	}
	end := []publictest.TxStep{{Query: finish, Status: status}}
	if mode == oceanbase.ModeOracle {
		end = []publictest.TxStep{{Query: finish, Status: 4}, {Query: driverFixtureAutocommitOn, Status: status}}
	}
	probeEnd := []publictest.TxStep{{Query: driverFixtureRollbackSQL, Status: status}}
	if mode == oceanbase.ModeOracle {
		probeEnd = []publictest.TxStep{{Query: driverFixtureRollbackSQL, Status: 4}, {Query: driverFixtureAutocommitOn, Status: status}}
	}
	if !publictest.ServeTxSteps(ctx, t, p, end) || !publictest.ServeTxSteps(ctx, t, p, begin) || !publictest.ServeTxSteps(ctx, t, p, probeEnd) ||
		!publictest.ServePrepareStatus(ctx, t, p, sessionStabilityUpdate, id+1, 1, nil, status) ||
		!testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(id+1, value, false)) ||
		!testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}) ||
		!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, id+1)) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
		return false
	}
	return true
}

func runDriverSessionCycle(ctx context.Context, db stabilitySession, physical driverPhysical, i int) error {
	for _, value := range []int64{int64(i + 1), -int64(i + 1)} {
		result, err := db.ExecContext(ctx, sessionStabilityUpdate, value)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("cached update count %d: %v", n, err)
		}
	}
	rows, err := db.QueryContext(ctx, sessionStabilitySelect)
	if err != nil {
		return err
	}
	if i%2 != 0 {
		var value int64
		if !rows.Next() || rows.Scan(&value) != nil || value != int64(i+1) {
			_ = rows.Close()
			return fmt.Errorf("mixed rows value %d: %v", value, rows.Err())
		}
	}
	if err = rows.Close(); err != nil || rows.Err() != nil {
		return fmt.Errorf("early Rows.Close: %v, rows: %v", err, rows.Err())
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, sessionStabilityMutation)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if n, countErr := result.RowsAffected(); countErr != nil || n != 1 {
		_ = tx.Rollback()
		return fmt.Errorf("transaction update count %d: %v", n, countErr)
	}
	if i%2 == 0 {
		err = tx.Rollback()
	} else {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}
	probe, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("transaction state was not restored: %w", err)
	}
	if err = probe.Rollback(); err != nil {
		return fmt.Errorf("restored transaction could not roll back: %w", err)
	}
	if !physical.IsValid() {
		return errors.New("transaction invalidated the original session")
	}
	_, err = db.ExecContext(ctx, sessionStabilityUpdate, i+1)
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1062 || server.SQLState != "23000" {
		return fmt.Errorf("recoverable prepared error: %v", err)
	}
	if err = db.PingContext(ctx); err != nil {
		return fmt.Errorf("session not reusable after mixed cycle: %w", err)
	}
	return nil
}
