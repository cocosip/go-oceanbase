package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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
	run   func(testing.TB, Mode, int, func(), func())
}

func driverBenchmarkCases() []driverBenchmarkCase {
	return []driverBenchmarkCase{
		{name: "crud", run: runDriverCRUD},
		{name: "prepare-per-call", run: func(t testing.TB, mode Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, false, false)
		}},
		{name: "prepare-reuse", run: func(t testing.TB, mode Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, true, false)
		}},
		{name: "cache-hit", run: func(t testing.TB, mode Mode, n int, start, stop func()) {
			runDriverPrepared(t, mode, n, start, stop, false, true)
		}},
		{name: "batch-sequential", rows: benchmarkBatchRows, run: func(t testing.TB, mode Mode, n int, start, stop func()) {
			runDriverBatch(t, mode, n, start, stop, BatchSequential)
		}},
		{name: "batch-multisend", rows: benchmarkBatchRows, run: func(t testing.TB, mode Mode, n int, start, stop func()) {
			runDriverBatch(t, mode, n, start, stop, BatchMultiSend)
		}},
		{name: "lob", bytes: benchmarkPayloadBytes, run: runDriverLOB},
		{name: "fetch", bytes: benchmarkFetchRows * 8, rows: benchmarkFetchRows, run: runDriverFetch},
	}
}

// These benchmarks measure database/sql and the driver against an in-process protocol peer.
// They include peer/scheduler allocations and do not measure real database throughput.
func BenchmarkDriver(b *testing.B) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, scenario := range driverBenchmarkCases() {
			b.Run(map[Mode]string{ModeMySQL: "mysql-compatible", ModeOracle: "oracle-compatible"}[mode]+"/"+scenario.name, func(b *testing.B) {
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
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, scenario := range driverBenchmarkCases() {
			t.Run(map[Mode]string{ModeMySQL: "mysql-compatible", ModeOracle: "oracle-compatible"}[mode]+"/"+scenario.name, func(t *testing.T) {
				scenario.run(t, mode, 2, func() {}, func() {})
			})
		}
	}
}

func driverBenchmarkDB(t testing.TB, mode Mode, options ExecutionOptions, serve func(context.Context, *wire.PacketIO, byte) bool) (*sql.DB, context.Context) {
	t.Helper()
	// testing cancels TB.Context before Cleanup; the peer must survive DB.Close.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
	t.Cleanup(cancel)
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	var dials atomic.Int32
	configured, err := NewConnector(Config{User: testUser, Mode: mode, Execution: options, DialContext: func(context.Context, string, string) (net.Conn, error) {
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
			p := wire.NewPacketIO(authPeerConn{server}, 64<<20)
			if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) || !serve(ctx, p, status) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}})
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
	var physical *conn
	err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil })
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
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer checkCancel()
		if err := physical.lock(checkCtx); err != nil {
			t.Error(err)
			return
		}
		defer physical.unlock()
		if !physical.closed.Load() || len(physical.resources.records) != 0 || physical.statements.order.Len() != 0 || len(physical.statements.entries) != 0 {
			t.Error("benchmark physical connection retained resources after Close")
		}
	})
	return db, ctx
}

func runDriverCRUD(t testing.TB, mode Mode, n int, start, stop func()) {
	t.Helper()
	queries := []string{"INSERT INTO benchmark_fixture VALUES (42)", "UPDATE benchmark_fixture SET id=42", "SELECT id FROM benchmark_fixture", "DELETE FROM benchmark_fixture"}
	db, ctx := driverBenchmarkDB(t, mode, ExecutionOptions{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			for j, query := range queries {
				if !readQuery(ctx, t, p, query) {
					return false
				}
				if j == 2 {
					if !sendPackets(ctx, t, p, []byte{1}, textColumn("id", 8, 63, 0), []byte{254, 0, 0, status, 0}, []byte{2, '4', '2'}, []byte{254, 0, 0, status, 0}) {
						return false
					}
				} else if !sendPackets(ctx, t, p, commandPacket(1, 0, status)) {
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

func runDriverPrepared(t testing.TB, mode Mode, n int, start, stop func(), explicit, cached bool) {
	t.Helper()
	options := ExecutionOptions{}
	if cached {
		options.StatementCacheSize, options.ReuseParameterTypes = 1, true
	}
	db, ctx := driverBenchmarkDB(t, mode, options, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			if (i == 0 || !explicit && !cached) && !servePrepareStatus(ctx, t, p, benchmarkPreparedSQL, 7, 1, nil, status) {
				return false
			}
			if !readCommand(ctx, t, p, optimizationExecute(7, 42, cached && i > 0)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) {
				return false
			}
			if !explicit && !cached && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
				return false
			}
		}
		return (!explicit && !cached) || readCommand(ctx, t, p, stmtPacket(25, 7))
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

func runDriverBatch(t testing.TB, mode Mode, n int, start, stop func(), strategy BatchStrategy) {
	t.Helper()
	db, ctx := driverBenchmarkDB(t, mode, ExecutionOptions{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			for j := 0; j < benchmarkBatchRows; j++ {
				if (j == 0 || strategy == BatchSequential) && !servePrepareStatus(ctx, t, p, benchmarkPreparedSQL, 7, 1, nil, status) {
					return false
				}
				if !readCommand(ctx, t, p, optimizationExecute(7, 42, false)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) {
					return false
				}
				if (j == benchmarkBatchRows-1 || strategy == BatchSequential) && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
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
		result, err := ExecBatch(ctx, db, benchmarkPreparedSQL, rows, BatchOptions{Strategy: strategy, ContinueOnError: true})
		if err != nil || len(result.Items) != benchmarkBatchRows || result.Strategy != strategy {
			t.Fatal("batch shape/strategy", result, err)
		}
		for _, item := range result.Items {
			if item.Status != BatchSucceeded || !item.Started || !item.RowsAffectedKnown || item.RowsAffected != 1 {
				t.Fatal("batch outcome", item)
			}
		}
	}
	stop()
}

func runDriverLOB(t testing.TB, mode Mode, n int, start, stop func()) {
	t.Helper()
	payload := bytes.Repeat([]byte{'x'}, benchmarkPayloadBytes)
	db, ctx := driverBenchmarkDB(t, mode, ExecutionOptions{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		for i := 0; i <= n; i++ {
			if mode == ModeOracle {
				if !serveLOBQueryStatus(ctx, t, p, "SELECT b FROM benchmark_fixture", 210, status, lobInlineFixture(string(payload))) {
					return false
				}
			} else if !serveTableDictionary(ctx, t, p, status, tableDictionary()...) ||
				!serveTableRows(ctx, t, p, status, []string{testOctetLength}, []byte{8}, []any{strconv.Itoa(len(payload))}) ||
				!serveTableRows(ctx, t, p, status, []string{"SUBSTRING(`b`, 1 , 16384 )", "FROM `s`.`t` WHERE"}, []byte{252}, []any{payload}) {
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
		var blob *Blob
		if mode == ModeOracle {
			blob, err = OpenBlob(ctx, session, "SELECT b FROM benchmark_fixture")
		} else {
			blob, err = OpenTableBlob(ctx, session, tableTarget())
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

func runDriverFetch(t testing.TB, mode Mode, n int, start, stop func()) {
	t.Helper()
	db, ctx := driverBenchmarkDB(t, mode, ExecutionOptions{}, func(ctx context.Context, p *wire.PacketIO, status byte) bool {
		column := textColumn("id", 8, 63, 0)
		if !servePrepareStatus(ctx, t, p, benchmarkCursorSQL, 7, 0, nil, status) || !readCommand(ctx, t, p, cursorExecuteFixture(7)) ||
			!sendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, status | 64, 0}) {
			return false
		}
		fetch := cursorFetchFixture(7, benchmarkFetchRows)
		if mode == ModeOracle {
			fetch = append(fetch, 2, 0, 0, 0, 0, 0, 1, 0, 0, 0)
		}
		packets := make([][]byte, benchmarkFetchRows)
		for i := range packets {
			packets[i] = binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(i))
		}
		for i := 0; i <= n; i++ {
			if !readCommand(ctx, t, p, fetch) {
				return false
			}
			if mode == ModeOracle && !sendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, status, 0}) {
				return false
			}
			if !sendPackets(ctx, t, p, packets...) || !sendPackets(ctx, t, p, []byte{254, 0, 0, status | 64, 0}) {
				return false
			}
			if mode == ModeOracle && !sendPackets(ctx, t, p, protocolOK(status, nil)) {
				return false
			}
		}
		return readCommand(ctx, t, p, stmtPacket(25, 7))
	})
	session, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	cursor, err := OpenCursor(ctx, session, benchmarkCursorSQL, CursorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= n; i++ {
		if i == 1 {
			start()
		}
		rows, err := cursor.Fetch(ctx, CursorPosition{}, benchmarkFetchRows)
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
