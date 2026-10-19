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

// Multi-VALUES has chunk-level results and must never invent per-input counts.
func TestBatchMultiValues(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.name, func(t *testing.T) { testBatchMultiValues(t, p) })
	}
}

func testBatchMultiValues(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	ctx := testContext(t)
	table := fmt.Sprintf("OBGVALUES_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(100))"+integrationInnoDBSuffix); err != nil {
		failOperation(t, "create values table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop values table", err)
		}
	}()
	query := "INSERT INTO " + table + " VALUES (?,?)"
	opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkRows: 2, ChunkParameters: 4}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		failOperation(t, "begin values transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := oceanbase.ExecBatch(ctx, tx, query, [][]any{{1, "一;\\'"}, {2, "二🙂"}, {3, nil}}, opts)
	if err != nil {
		failOperation(t, "multi-values insert", err)
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
			failOperation(t, "read values content", err)
		}
		want := []string{"一;\\'", "二🙂", ""}[i]
		if value.Valid != (i != 2) || value.String != want {
			t.Fatal("multi-values changed input content", value)
		}
	}
	if err = tx.Rollback(); err != nil {
		failOperation(t, "rollback values transaction", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		t.Fatal("multi-values committed caller transaction", count, err)
	}
	testBatchMultiValuesFailures(t, p, db, table)
}

func testBatchMultiValuesFailures(t *testing.T, p profile, db *sql.DB, table string) {
	t.Helper()
	ctx := testContext(t)
	opts := oceanbase.BatchOptions{Strategy: oceanbase.BatchMultiValues, ChunkRows: 2, ChunkParameters: 4}
	for _, keepGoing := range []bool{false, true} {
		opts.ContinueOnError = keepGoing
		want := 0
		if keepGoing {
			want = 1
		}
		result, err := oceanbase.ExecBatch(ctx, db, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{1, "values-first"}, {1, "values-duplicate"}, {2, "values-last"}}, opts)
		var batch *oceanbase.BatchError
		if !errors.As(err, &batch) || batch.Index != 0 || !duplicateKey(err, p.mode) || len(result.Chunks) != 1+want {
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
			failOperation(t, "clear values table", err)
		}
	}
}
