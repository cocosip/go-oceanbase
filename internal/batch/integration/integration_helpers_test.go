//go:build integration

package integration

import (
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestBatchMultiValues(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGVALUES_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+integrationIntegrationInnoDBSuffix); err != nil {
		integrationtest.FailOperation(t, "create values table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop values table", err)
		}
	}()
	query := "INSERT INTO " + table + " VALUES (?,?)"
	opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkRows: 2, ChunkParameters: 4}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin values transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := oceanbase.ExecBatch(ctx, tx, query, [][]any{{1, "一;\\'"}, {2, "二🙂"}, {3, nil}}, opts)
	if err != nil {
		integrationtest.FailOperation(t, "multi-values insert", err)
	}
	if result.Strategy != oceanbase.BatchMultiValues || len(result.Chunks) != 2 {
		t.Fatal("native multi-values did not execute", result)
	}
	for i, want := range []int64{2, 1} {
		chunk := result.Chunks[i]
		if chunk.Start != i*2 || chunk.End != min(i*2+2, 3) || chunk.Status != oceanbase.BatchSucceeded || !chunk.RowsAffectedKnown || chunk.RowsAffected != want {
			t.Fatal("aggregate values result differs", chunk)
		}
	}
	for i, item := range result.Items {
		if item.Status != oceanbase.BatchSucceeded || i < 2 && (item.RowsAffectedKnown || item.InsertIDKnown) {
			t.Fatal("invented per-input result", item)
		}
		var value sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", i+1).Scan(&value); err != nil {
			integrationtest.FailOperation(t, "read values content", err)
		}
		want := []string{"一;\\'", "二🙂", ""}[i]
		if value.Valid != (i != 2) || value.String != want {
			t.Fatal("multi-values changed input content", value)
		}
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback values transaction", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatal("multi-values committed caller transaction", count, err)
	}
	integrationTestBatchMultiValuesFailures(t, p, db, table)
}

const integrationIntegrationInnoDBSuffix = " ENGINE=InnoDB"

func integrationTestBatchMultiValuesFailures(t *testing.T, p integrationtest.Profile, db *sql.DB, table string) {
	t.Helper()
	ctx := integrationtest.TestContext(t)
	opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkRows: 2, ChunkParameters: 4}
	for _, keepGoing := range []bool{false, true} {
		opts.ContinueOnError = keepGoing
		want := 0
		if keepGoing {
			want = 1
		}
		result, err := oceanbase.ExecBatch(ctx, db, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{1, "values-first"}, {1, "values-duplicate"}, {2, "values-last"}}, opts)
		var batch *oceanbase.BatchError
		if !errors.As(err, &batch) || batch.Index != 0 || !integrationtest.DuplicateKey(err, p.Mode) || len(result.Chunks) != 1+want {
			t.Fatal("failed chunk attribution differs", result, err)
		}
		for _, item := range result.Items[:2] {
			if item.Status != oceanbase.BatchFailed || item.RowsAffectedKnown || item.InsertIDKnown {
				t.Fatal("failed aggregate fabricated input outcome", item)
			}
		}
		wantStatus := oceanbase.BatchNotExecuted
		if keepGoing {
			wantStatus = oceanbase.BatchSucceeded
		}
		if result.Items[2].Status != wantStatus || batch.Completed != want {
			t.Fatal("multi-values stop/continue differs", result, err)
		}
		var count int
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatal("multi-values effects differ", count, err)
		}
		if _, err = db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			integrationtest.FailOperation(t, "clear values table", err)
		}
	}
}

func integrationTestBatchArrayBindingProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	table := fmt.Sprintf("OBGARRAY_%d", time.Now().UnixNano())
	suffix := integrationIntegrationInnoDBSuffix
	if p.Mode == oceanbase.ModeOracle {
		suffix = ""
	}
	if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+suffix); err != nil {
		integrationtest.FailOperation(t, "create array binding table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop array binding table", err)
		}
	}()
	tx, err := db.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin array binding transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := oceanbase.ExecBatch(integrationtest.TestContext(t), tx, "INSERT INTO "+table+" VALUES (?,?)",
		[][]any{{1, "一"}, {1, "duplicate"}, {2, "二🙂"}},
		oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkRows: 3, ChunkParameters: 6})
	want := oceanbase.BatchSequential
	if p.Mode == oceanbase.ModeOracle {
		// This native-path fixture requires a known OceanBase version >= 4.2.1.
		want = oceanbase.BatchArrayBinding
	}
	var batch *oceanbase.BatchError
	var server *oceanbase.Error
	if r.Strategy != want || !errors.As(err, &batch) || batch.Index != 1 || batch.Completed != 2 || len(r.Items) != 3 || !errors.As(err, &server) || !integrationtest.DuplicateKey(err, p.Mode) || !integrationtest.DuplicateKey(r.Items[1].Err, p.Mode) {
		t.Fatal("array binding strategy or error attribution differs")
	}
	if r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchFailed || r.Items[2].Status != oceanbase.BatchSucceeded {
		t.Fatal("array binding omitted a successful input")
	}
	if p.Mode == oceanbase.ModeOracle && (r.Items[0].RowsAffectedKnown || r.Items[2].RowsAffectedKnown || server.SQLState != "") {
		t.Fatal("array binding fabricated per-input counts or SQLSTATE")
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback array binding transaction", err)
	}
	var remaining int
	if err = db.QueryRowContext(integrationtest.TestContext(t), "SELECT COUNT(*) FROM "+table).Scan(&remaining); err != nil {
		integrationtest.FailOperation(t, "count rolled back array binding rows", err)
	}
	if remaining != 0 {
		t.Fatal("array binding changed the caller's transaction boundary")
	}
}

func integrationTestBatchDatabase(t *testing.T, p integrationtest.Profile, db *sql.DB, rewrite bool) {
	t.Helper()
	table := fmt.Sprintf("OBGBATCH_%d", time.Now().UnixNano())
	suffix := integrationIntegrationInnoDBSuffix
	if p.Mode == oceanbase.ModeOracle {
		suffix = ""
	}
	if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+suffix); err != nil {
		integrationtest.FailOperation(t, "create batch table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop batch table", err)
		}
	}()
	query := "INSERT INTO " + table + " VALUES (?,?)"
	inputs := [][]any{{1, "一"}, {1, "duplicate"}, {2, "二🙂"}}
	nativeRewrite := false
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend, oceanbase.BatchRewrite, oceanbase.BatchBulk, oceanbase.BatchArrayBinding} {
		for _, keepGoing := range []bool{false, true} {
			ctx := integrationtest.TestContext(t)
			r, err := oceanbase.ExecBatch(ctx, db, query, inputs, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: keepGoing, ChunkRows: 2, ChunkParameters: 4})
			if rewrite && strategy == oceanbase.BatchRewrite {
				want := oceanbase.BatchSequential
				if p.Mode == oceanbase.ModeMySQL && !keepGoing {
					want = oceanbase.BatchRewrite
				}
				if r.Strategy == oceanbase.BatchRewrite {
					nativeRewrite = true
				}
				if r.Strategy != want && (want != oceanbase.BatchRewrite || r.Strategy != oceanbase.BatchSequential) {
					t.Fatal("batch rewrite negotiation did not select the expected strategy")
				}
			}
			want := integrationCheckBatchDuplicateResult(t, p.Mode, r, err, keepGoing)
			var n int
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
				integrationtest.FailOperation(t, "count batch rows", err)
			}
			if n != want {
				t.Fatal("batch effects differ from declared policy")
			}
			if _, err = db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				integrationtest.FailOperation(t, "clear batch table", err)
			}
		}
	}
	ctx := integrationtest.TestContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin batch transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	opts := oceanbase.BatchOptions{ContinueOnError: true}
	if rewrite {
		opts = oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite}
	}
	if _, err = oceanbase.ExecBatch(ctx, tx, query, [][]any{{3, "三"}, {4, "四"}}, opts); err != nil {
		integrationtest.FailOperation(t, "transactional batch", err)
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback batch transaction", err)
	}
	var n int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		integrationtest.FailOperation(t, "count rolled back batch", err)
	}
	if n != 0 {
		t.Fatal("batch changed caller transaction boundary")
	}
	if rewrite && p.Mode == oceanbase.ModeMySQL && !nativeRewrite {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("native batch rewrite unavailable; sequential fallback and rollback verified")
		}
		t.Skip("native batch rewrite unavailable; sequential fallback and rollback verified")
	}
}

func integrationCheckBatchDuplicateResult(t *testing.T, mode oceanbase.Mode, result oceanbase.BatchResult, err error, keepGoing bool) int {
	t.Helper()
	var batch *oceanbase.BatchError
	if !errors.As(err, &batch) || batch.Index != 1 || len(result.Items) != 3 || result.Items[0].Status != oceanbase.BatchSucceeded || result.Items[1].Status != oceanbase.BatchFailed {
		t.Fatal("batch results differ from sequential contract")
	}
	if !integrationtest.DuplicateKey(err, mode) || !integrationtest.DuplicateKey(result.Items[1].Err, mode) {
		t.Fatal("batch duplicate key error code mismatch")
	}
	if keepGoing {
		if result.Items[2].Status != oceanbase.BatchSucceeded {
			t.Fatal("continue omitted final input")
		}
		return 2
	}
	if result.Items[2].Status != oceanbase.BatchNotExecuted {
		t.Fatal("stop sent final input")
	}
	return 1
}

func integrationTestBatchProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	integrationTestBatchDatabase(t, p, integrationtest.OpenProfile(t, p), false)
}
