//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestBatchExecution(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testBatchProfile(t, p) })
	}
}

func testBatchProfile(t *testing.T, p profile) {
	t.Helper()
	testBatchDatabase(t, p, openProfile(t, p), false)
}

func TestBatchRewriteExecution(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.AllowMultiStatements = true
			if p.mode == oceanbase.ModeMySQL {
				if cfg.Session.Variables == nil {
					cfg.Session.Variables = make(map[string]string)
				}
				cfg.Session.Variables["ob_enable_batched_multi_statement"] = "0"
			}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid batch rewrite config")
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			testBatchDatabase(t, p, db, true)
		})
	}
}

func testBatchDatabase(t *testing.T, p profile, db *sql.DB, rewrite bool) {
	t.Helper()
	table := fmt.Sprintf("OBGBATCH_%d", time.Now().UnixNano())
	suffix := integrationInnoDBSuffix
	if p.mode == oceanbase.ModeOracle {
		suffix = ""
	}
	if _, err := db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+suffix); err != nil {
		failOperation(t, "create batch table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop batch table", err)
		}
	}()
	query := "INSERT INTO " + table + " VALUES (?,?)"
	inputs := [][]any{{1, "一"}, {1, "duplicate"}, {2, "二🙂"}}
	for _, strategy := range []oceanbase.BatchStrategy{oceanbase.BatchSequential, oceanbase.BatchMultiSend, oceanbase.BatchRewrite, oceanbase.BatchBulk, oceanbase.BatchArrayBinding} {
		for _, keepGoing := range []bool{false, true} {
			ctx := testContext(t)
			r, err := oceanbase.ExecBatch(ctx, db, query, inputs, oceanbase.BatchOptions{Strategy: strategy, ContinueOnError: keepGoing, ChunkRows: 2, ChunkParameters: 4})
			if rewrite && strategy == oceanbase.BatchRewrite {
				want := oceanbase.BatchSequential
				if p.mode == oceanbase.ModeMySQL && !keepGoing {
					want = oceanbase.BatchRewrite
				}
				if r.Strategy != want {
					t.Fatal("batch rewrite negotiation did not select the expected strategy")
				}
			}
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || be.Index != 1 || len(r.Items) != 3 || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchFailed {
				t.Fatal("batch results differ from sequential contract")
			}
			if !duplicateKey(err, p.mode) || !duplicateKey(r.Items[1].Err, p.mode) {
				t.Fatal("batch duplicate key error code mismatch")
			}
			want := 1
			if keepGoing {
				want = 2
				if r.Items[2].Status != oceanbase.BatchSucceeded {
					t.Fatal("continue omitted final input")
				}
			} else if r.Items[2].Status != oceanbase.BatchNotExecuted {
				t.Fatal("stop sent final input")
			}
			var n int
			if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
				failOperation(t, "count batch rows", err)
			}
			if n != want {
				t.Fatal("batch effects differ from declared policy")
			}
			if _, err = db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
				failOperation(t, "clear batch table", err)
			}
		}
	}
	ctx := testContext(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		failOperation(t, "begin batch transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	opts := oceanbase.BatchOptions{ContinueOnError: true}
	if rewrite {
		opts = oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite}
	}
	if _, err = oceanbase.ExecBatch(ctx, tx, query, [][]any{{3, "三"}, {4, "四"}}, opts); err != nil {
		failOperation(t, "transactional batch", err)
	}
	if err = tx.Rollback(); err != nil {
		failOperation(t, "rollback batch transaction", err)
	}
	var n int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&n); err != nil {
		failOperation(t, "count rolled back batch", err)
	}
	if n != 0 {
		t.Fatal("batch changed caller transaction boundary")
	}
}

func TestBatchArrayBindingExecution(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testBatchArrayBindingProfile(t, p) })
	}
}

func testBatchArrayBindingProfile(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	table := fmt.Sprintf("OBGARRAY_%d", time.Now().UnixNano())
	suffix := integrationInnoDBSuffix
	if p.mode == oceanbase.ModeOracle {
		suffix = ""
	}
	if _, err := db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+suffix); err != nil {
		failOperation(t, "create array binding table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop array binding table", err)
		}
	}()
	tx, err := db.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin array binding transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := oceanbase.ExecBatch(testContext(t), tx, "INSERT INTO "+table+" VALUES (?,?)",
		[][]any{{1, "一"}, {1, "duplicate"}, {2, "二🙂"}},
		oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkRows: 3, ChunkParameters: 6})
	want := oceanbase.BatchSequential
	if p.mode == oceanbase.ModeOracle {
		// This native-path fixture requires a known OceanBase version >= 4.2.1.
		want = oceanbase.BatchArrayBinding
	}
	var batch *oceanbase.BatchError
	var server *oceanbase.Error
	if r.Strategy != want || !errors.As(err, &batch) || batch.Index != 1 || batch.Completed != 2 || len(r.Items) != 3 || !errors.As(err, &server) || !duplicateKey(err, p.mode) || !duplicateKey(r.Items[1].Err, p.mode) {
		t.Fatal("array binding strategy or error attribution differs")
	}
	if r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchFailed || r.Items[2].Status != oceanbase.BatchSucceeded {
		t.Fatal("array binding omitted a successful input")
	}
	if p.mode == oceanbase.ModeOracle && (r.Items[0].RowsAffectedKnown || r.Items[2].RowsAffectedKnown || server.SQLState != "") {
		t.Fatal("array binding fabricated per-input counts or SQLSTATE")
	}
	if err = tx.Rollback(); err != nil {
		failOperation(t, "rollback array binding transaction", err)
	}
	var remaining int
	if err = db.QueryRowContext(testContext(t), "SELECT COUNT(*) FROM "+table).Scan(&remaining); err != nil {
		failOperation(t, "count rolled back array binding rows", err)
	}
	if remaining != 0 {
		t.Fatal("array binding changed the caller's transaction boundary")
	}
}
