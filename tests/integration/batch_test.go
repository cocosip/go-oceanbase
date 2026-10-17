//go:build integration

package integration

import (
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
	db := openProfile(t, p)
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
			var be *oceanbase.BatchError
			if !errors.As(err, &be) || be.Index != 1 || len(r.Items) != 3 || r.Items[0].Status != oceanbase.BatchSucceeded || r.Items[1].Status != oceanbase.BatchFailed {
				t.Fatal("batch results differ from sequential contract")
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
	if _, err = oceanbase.ExecBatch(ctx, tx, query, [][]any{{3, "三"}, {4, "四"}}, oceanbase.BatchOptions{ContinueOnError: true}); err != nil {
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
