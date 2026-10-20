package batch_test

import (
	"context"
	"database/sql"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/command"
	"math"
	"testing"
)

func TestBatchGeneratedIDs(t *testing.T) {
	for _, id := range []uint64{0, 7, math.MaxUint64} {
		r, err := oceanbase.ExecBatch(context.Background(), batchExecer(func(context.Context, string, ...any) (sql.Result, error) {
			return command.Result{Known: true, Affected: 1, ID: id}, nil
		}), testBatchSQL, [][]any{{1}}, oceanbase.BatchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := r.Items[0]
		if item.InsertIDKnown != (id != 0) || id == 7 && item.InsertID != "7" || id == math.MaxUint64 && item.InsertID != testMaxUint64Text {
			t.Fatalf("id %d: %+v", id, item)
		}
	}
}

func checkBatchInsertID(t testing.TB, item oceanbase.BatchItem, want string) {
	t.Helper()
	if item.InsertIDKnown != (want != "") || item.InsertID != want {
		t.Fatalf("insert ID: %+v, want %q", item, want)
	}
}
