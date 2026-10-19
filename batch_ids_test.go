package oceanbase

import (
	"context"
	"database/sql"
	"math"
	"testing"
)

func TestBatchGeneratedIDs(t *testing.T) {
	for _, id := range []uint64{0, 7, math.MaxUint64} {
		r, err := ExecBatch(context.Background(), batchExecer(func(context.Context, string, ...any) (sql.Result, error) {
			return result{known: true, affected: 1, id: id}, nil
		}), testBatchSQL, [][]any{{1}}, BatchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		item := r.Items[0]
		if item.InsertIDKnown != (id != 0) || id == 7 && item.InsertID != "7" || id == math.MaxUint64 && item.InsertID != testMaxUint64Text {
			t.Fatalf("id %d: %+v", id, item)
		}
	}
}

func checkBatchInsertID(t testing.TB, item BatchItem, want string) {
	t.Helper()
	if item.InsertIDKnown != (want != "") || item.InsertID != want {
		t.Fatalf("insert ID: %+v, want %q", item, want)
	}
}
