package oceanbase_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const benchmarkPayloadBytes = 16 << 10
const benchmarkFetchRows = 32
const benchmarkBatchRows = 8
const benchmarkPreparedSQL = "INSERT INTO benchmark_fixture VALUES (?)"
const benchmarkCursorSQL = "SELECT id FROM benchmark_fixture"

type driverBenchmarkCase struct {
	name  string
	bytes int64
	rows  int
	run   func(testing.TB, oceanbase.Mode, int, func(), func())
}

func driverBenchmarkCases(mode oceanbase.Mode) []driverBenchmarkCase {
	cases := []driverBenchmarkCase{
		{name: "crud", run: runDriverCRUD},
		{name: "prepare-per-call", run: func(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, false, false)
		}},
		{name: "prepare-reuse", run: func(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, true, false)
		}},
		{name: "cache-hit", run: func(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, false, true)
		}},
		{name: "batch-sequential", rows: benchmarkBatchRows, run: func(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
			runDriverBatch(t, mode, n, start, stop, oceanbase.BatchSequential)
		}},
		{name: "batch-multisend", rows: benchmarkBatchRows, run: func(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
			runDriverBatch(t, mode, n, start, stop, oceanbase.BatchMultiSend)
		}},
		{name: "lob", bytes: benchmarkPayloadBytes, run: runDriverLOB},
		{name: "fetch", bytes: benchmarkFetchRows * 8, rows: benchmarkFetchRows, run: runDriverFetch},
	}
	if mode == oceanbase.ModeMySQL {
		cases = append(cases, driverBenchmarkCase{name: "batch-rewrite", rows: benchmarkBatchRows, run: runDriverBatchRewrite})
	} else {
		cases = append(cases, driverBenchmarkCase{name: "batch-array-binding", rows: benchmarkBatchRows, run: runDriverBatchArray})
	}
	return cases
}

// These benchmarks measure database/sql and the driver against an in-process protocol peer.
// They include peer/scheduler allocations and do not measure real database throughput.
func BenchmarkDriver(b *testing.B) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, scenario := range driverBenchmarkCases(mode) {
			b.Run(map[oceanbase.Mode]string{oceanbase.ModeMySQL: "mysql-compatible", oceanbase.ModeOracle: "oracle-compatible"}[mode]+"/"+scenario.name, func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(scenario.bytes)
				scenario.run(b, mode, b.N, b.ResetTimer, b.StopTimer)
				if scenario.rows > 0 {
					b.ReportMetric(float64(scenario.rows), "rows/op")
				}
			})
		}
	}
}

// Run the same workload and result checks in ordinary CI, where benchmarks are not selected.
func TestDriverBenchmarkFixtures(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, scenario := range driverBenchmarkCases(mode) {
			t.Run(map[oceanbase.Mode]string{oceanbase.ModeMySQL: "mysql-compatible", oceanbase.ModeOracle: "oracle-compatible"}[mode]+"/"+scenario.name, func(t *testing.T) {
				scenario.run(t, mode, 2, func() {}, func() {})
			})
		}
	}
}

func driverBenchmarkDB(t testing.TB, mode oceanbase.Mode, cfg oceanbase.Config, serve func(context.Context, *wire.PacketIO, byte) bool) (*sql.DB, context.Context) {
	t.Helper()
	// testing cancels TB.Context before Cleanup; the peer must survive DB.Close.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	t.Cleanup(cancel)
	status := byte(2)
	if mode == oceanbase.ModeOracle {
		status = 6
	}
	var dials atomic.Int32
	var transport *driverTrackedConn
	cfg.User, cfg.Mode = driverFixtureUser, mode
	cfg.DialContext = func(context.Context, string, string) (net.Conn, error) {
		if dials.Add(1) != 1 {
			return nil, errors.New("benchmark unexpectedly replaced its physical connection")
		}
		client, server := net.Pipe()
		deadline, _ := ctx.Deadline()
		if e := server.SetDeadline(deadline); e != nil {
			_ = client.Close()
			_ = server.Close()
			return nil, e
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			p := wire.NewPacketIO(testpeer.AuthPeerConn{Conn: server}, 64<<20)
			offer := uint64(0)
			if cfg.AllowMultiStatements {
				offer = wire.ClientMultiStatements
			}
			if !testpeer.ServeConnect(ctx, t, p, status, offer) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) || !serve(ctx, p, status) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		transport = &driverTrackedConn{Conn: client}
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
	pinned, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	var physical driverPhysical
	err = pinned.Raw(func(raw any) error { physical = raw.(driverPhysical); return nil })
	err = errors.Join(err, pinned.Close())
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	// Register after dialing so DB.Close sends QUIT before transport cleanup waits for the peer.
	t.Cleanup(func() {
		checkBenchmarkIdle(t, physical, db.Stats())
		if err := db.Close(); err != nil {
			t.Error(err)
		}
		if stats := db.Stats(); stats.OpenConnections != 0 || stats.InUse != 0 {
			t.Error("benchmark pool retained connections after Close:", stats)
		}
		if physical.IsValid() || !transport.closed.Load() {
			t.Error("benchmark physical connection remained open after Close")
		}
	})
	return db, ctx
}

func runDriverCRUD(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
	t.Helper()
	queries := []string{"INSERT INTO benchmark_fixture VALUES (42)", "UPDATE benchmark_fixture SET id=42", "SELECT id FROM benchmark_fixture", "DELETE FROM benchmark_fixture"}
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			for j, query := range queries {
				if !testpeer.ReadQuery(ctx, t, p, query) {
					return false
				}
				if j == 2 {
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, status, 0}, []byte{2, '4', '2'}, []byte{254, 0, 0, status, 0}) {
						return false
					}
				} else if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) {
					return false
				}
			}
		}
		return true
	})
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		for j, query := range queries {
			if j == 2 {
				var got int64
				if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil || got != 42 {
					t.Fatal("CRUD query result", got, err)
				}
			} else {
				result, err := db.ExecContext(ctx, query)
				if err != nil {
					t.Fatal(err)
				}
				if affected, err := result.RowsAffected(); err != nil || affected != 1 {
					t.Fatal("CRUD affected rows", affected, err)
				}
			}
		}
	}
	stop()
}

func runDriverPrepared(t testing.TB, mode oceanbase.Mode, n int, start, stop func(), explicit, cached bool) {
	t.Helper()
	options := oceanbase.ExecutionOptions{}
	if cached {
		options.StatementCacheSize, options.ReuseParameterTypes = 1, true
	}
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{Execution: options}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			if (i == 0 || !explicit && !cached) && !publictest.ServePrepareStatus(ctx, t, p, benchmarkPreparedSQL, 7, 1, nil, status) {
				return false
			}
			if !testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(7, 42, cached && i > 0)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) {
				return false
			}
			if !explicit && !cached && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
				return false
			}
		}
		return (!explicit && !cached) || testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7))
	})
	exec := func() (sql.Result, error) { return db.ExecContext(ctx, benchmarkPreparedSQL, int64(42)) }
	var prepared *sql.Stmt
	if explicit {
		var err error
		prepared, err = db.PrepareContext(ctx, benchmarkPreparedSQL)
		if err != nil {
			t.Fatal(err)
		}
		exec = func() (sql.Result, error) { return prepared.ExecContext(ctx, int64(42)) }
	}
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		result, err := exec()
		if err != nil {
			t.Fatal(err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			t.Fatal("prepared affected rows", affected, err)
		}
	}
	stop()
	if prepared != nil {
		if err := prepared.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func runDriverBatch(t testing.TB, mode oceanbase.Mode, n int, start, stop func(), strategy oceanbase.BatchStrategy) {
	t.Helper()
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			for j := 0; j < benchmarkBatchRows; j++ {
				if (j == 0 || strategy == oceanbase.BatchSequential) && !publictest.ServePrepareStatus(ctx, t, p, benchmarkPreparedSQL, 7, 1, nil, status) {
					return false
				}
				if !testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(7, 42, false)) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, status)) {
					return false
				}
				if (j == benchmarkBatchRows-1 || strategy == oceanbase.BatchSequential) && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return false
				}
			}
		}
		return true
	})
	rows := make([][]any, benchmarkBatchRows)
	for i := range rows {
		rows[i] = []any{int64(42)}
	}
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		result, err := oceanbase.ExecBatch(ctx, db, benchmarkPreparedSQL, rows, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: true})
		if err != nil || len(result.Items) != benchmarkBatchRows || result.Strategy != strategy {
			t.Fatal("batch shape/strategy", result, err)
		}
		for _, item := range result.Items {
			if item.Status != oceanbase.BatchSucceeded || !item.Started || !item.RowsAffectedKnown || item.RowsAffected != 1 {
				t.Fatal("batch outcome", item)
			}
		}
	}
	stop()
}

func runDriverBatchRewrite(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
	t.Helper()
	query := strings.TrimSuffix(strings.Repeat("INSERT INTO benchmark_fixture VALUES ( 42 )\n;\n", benchmarkBatchRows), "\n;\n")
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{AllowMultiStatements: true}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			if !driverFixtureServeBatchRewriteGuard(ctx, t, p, "0", status) || !testpeer.ReadQuery(ctx, t, p, query) {
				return false
			}
			for j := 0; j < benchmarkBatchRows; j++ {
				flags := status
				if j < benchmarkBatchRows-1 {
					flags |= 8
				}
				if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, flags)) {
					return false
				}
			}
		}
		return true
	})
	rows := make([][]any, benchmarkBatchRows)
	for i := range rows {
		rows[i] = []any{int64(42)}
	}
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		result, err := oceanbase.ExecBatch(ctx, db, benchmarkPreparedSQL, rows, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
		if err != nil || result.Strategy != oceanbase.BatchRewrite || len(result.Items) != benchmarkBatchRows {
			t.Fatal("rewritten batch shape/strategy", result, err)
		}
		for _, item := range result.Items {
			if item.Status != oceanbase.BatchSucceeded || !item.Started || !item.RowsAffectedKnown || item.RowsAffected != 1 {
				t.Fatal("rewritten batch outcome", item)
			}
		}
	}
	stop()
}

func runDriverBatchArray(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
	t.Helper()
	values := make([]any, benchmarkBatchRows)
	rows := make([][]any, benchmarkBatchRows)
	for i := range values {
		values[i] = int64(42)
		rows[i] = []any{values[i]}
	}
	expected := driverFixtureBatchArrayPacket(t, values, wire.ExecuteOptions{})
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO, _ byte) bool {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+driverFixtureAutocommitOff), 4) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+driverFixtureOracleReadWrite), 5) {
			return false
		}
		for i := 0; i <= n; i++ {
			if !publictest.ServePrepareStatus(ctx, t, p, benchmarkPreparedSQL, 7, 1, nil, 5) || !testpeer.ReadCommand(ctx, t, p, expected) ||
				!testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(benchmarkBatchRows, 0, 5)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
				return false
			}
		}
		return testpeer.ServeOK(ctx, t, p, []byte("\x03"+driverFixtureRollbackSQL), 4) && testpeer.ServeOK(ctx, t, p, []byte("\x03"+driverFixtureAutocommitOn), 6)
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		result, err := oceanbase.ExecBatch(ctx, tx, benchmarkPreparedSQL, rows, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true})
		if err != nil || result.Strategy != oceanbase.BatchArrayBinding || len(result.Items) != benchmarkBatchRows {
			t.Fatal("array batch shape/strategy", result, err)
		}
		for _, item := range result.Items {
			if item.Status != oceanbase.BatchSucceeded || !item.Started || item.RowsAffectedKnown {
				t.Fatal("array batch outcome", item)
			}
		}
	}
	stop()
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func runDriverLOB(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
	t.Helper()
	payload := bytes.Repeat([]byte{'x'}, benchmarkPayloadBytes)
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			if mode == oceanbase.ModeOracle {
				if !publictest.ServeLOBQueryStatus(ctx, t, p, "SELECT b FROM benchmark_fixture", 210, status, publictest.LobInlineFixture(string(payload))) {
					return false
				}
			} else if !publictest.ServeTableDictionary(ctx, t, p, status, publictest.TableDictionary()...) ||
				!publictest.ServeTableRows(ctx, t, p, status, []string{driverFixtureOctetLength}, []byte{8}, []any{strconv.Itoa(len(payload))}) ||
				!publictest.ServeTableRows(ctx, t, p, status, []string{"SUBSTRING(`b`, 1 , 16384 )", "FROM `s`.`t` WHERE"}, []byte{252}, []any{payload}) {
				return false
			}
		}
		return true
	})
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	buffer := make([]byte, len(payload))
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		var blob *oceanbase.Blob
		if mode == oceanbase.ModeOracle {
			blob, err = oceanbase.OpenBlob(ctx, session, "SELECT b FROM benchmark_fixture")
		} else {
			blob, err = oceanbase.OpenTableBlob(ctx, session, publictest.TableTarget())
		}
		if err != nil || blob == nil {
			t.Fatal("open benchmark LOB", err)
		}
		if count, err := blob.ReadAtContext(ctx, buffer, 0); err != nil || count != len(payload) || !bytes.Equal(buffer, payload) {
			t.Fatal("LOB content", count, err)
		}
		if err = blob.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop()
}

func runDriverFetch(t testing.TB, mode oceanbase.Mode, n int, start, stop func()) {
	t.Helper()
	db, ctx := driverBenchmarkDB(t, mode, oceanbase.Config{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		column := testpeer.TextColumn("id", 8, 63, 0)
		if !publictest.ServePrepareStatus(ctx, t, p, benchmarkCursorSQL, 7, 0, nil, status) || !testpeer.ReadCommand(ctx, t, p, driverFixtureCursorExecuteFixture(7)) ||
			!testpeer.SendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, status | 64, 0}) {
			return false
		}
		fetch := publictest.CursorFetchFixture(7, benchmarkFetchRows)
		if mode == oceanbase.ModeOracle {
			fetch = append(fetch, 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		}
		packets := make([][]byte, benchmarkFetchRows)
		for i := range packets {
			packets[i] = binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(i))
		}
		for i := 0; i <= n; i++ {
			if !testpeer.ReadCommand(ctx, t, p, fetch) {
				return false
			}
			if mode == oceanbase.ModeOracle && !testpeer.SendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, status, 0}) {
				return false
			}
			if !testpeer.SendPackets(ctx, t, p, packets...) || !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, status | 64, 0}) {
				return false
			}
			if mode == oceanbase.ModeOracle && !testpeer.SendPackets(ctx, t, p, testpeer.ProtocolOK(status, nil)) {
				return false
			}
		}
		return testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7))
	})
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	cursor, err := oceanbase.OpenCursor(ctx, session, benchmarkCursorSQL, oceanbase.CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		rows, err := cursor.Fetch(ctx, oceanbase.CursorPosition{}, benchmarkFetchRows)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil || id != int64(count) {
				t.Fatal("fetch row order/value", count, id, err, "close", rows.Close())
			}
			count++
		}
		if err = rows.Err(); err != nil || count != benchmarkFetchRows {
			t.Fatal("fetch row count", count, err)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stop()
	if err = cursor.Close(); err != nil {
		t.Fatal(err)
	}
}
