package catalog

import (
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTableBlobDictionaryPlan(t *testing.T) {
	rows := [][]driver.Value{
		{"s", "t", blobBaseTable, blobLockingEngine, "id", metadataLowerBigInt, "", int64(1)},
		{"s", "t", blobBaseTable, blobLockingEngine, "b", testLongBlob, "", nil},
	}
	sets := []Result{{Columns: make([]wire.Column, 8), Rows: rows}}
	plan, err := ResolveTableBlob(config.ModeMySQL, Target{Schema: "s", Table: "t", Column: "B", Key: map[string]any{"ID": int64(7)}}, sets)
	if err != nil || plan.Table != "`s`.`t`" || plan.Column != "`b`" || plan.Predicate != "`id` = ?" || plan.Capacity != 4294967295 || !plan.Locking || len(plan.Args) != 1 || plan.Args[0].Value != int64(7) {
		t.Fatal(plan, err)
	}
	rows[1][6] = "STORED GENERATED"
	if _, err := ResolveTableBlob(config.ModeMySQL, Target{Column: "b", Key: map[string]any{"id": 7}}, sets); !errors.Is(err, dberrors.ErrLOB) {
		t.Fatal(err)
	}
	rows[1][6] = ""
	if _, err := ResolveTableBlob(config.ModeMySQL, Target{Column: "b", Key: map[string]any{"id": 7, "extra": 8}}, sets); !errors.Is(err, dberrors.ErrLOB) {
		t.Fatal(err)
	}
}

func TestCursorDictionaryIdentity(t *testing.T) {
	cols := []wire.Column{{Schema: "s", OriginalTable: "t", OriginalName: "id", Name: "id", Type: 8}, {Schema: "s", OriginalTable: "t", OriginalName: "b", Name: "b", Type: 252}}
	rows := [][]driver.Value{{"s", "t", blobBaseTable, blobLockingEngine, "id", metadataLowerBigInt, "", int64(1)}, {"s", "t", blobBaseTable, blobLockingEngine, "b", testLongBlob, "", nil}}
	sets := []Result{{Columns: make([]wire.Column, 8), Rows: rows}}
	id, err := ResolveCursorIdentity(config.ModeMySQL, cols, sets)
	if err != nil || id.Table != "`s`.`t`" || len(id.Keys) != 1 || id.Keys[0] != "id" || !id.Locking {
		t.Fatal(id, err)
	}
	if _, err := ResolveCursorIdentity(config.ModeMySQL, cols[1:], sets); !errors.Is(err, dberrors.ErrCursor) {
		t.Fatal("missing projected primary key accepted", err)
	}
	rows[0][3] = "MyISAM"
	if _, err := ResolveCursorIdentity(config.ModeMySQL, cols, sets); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestCursorIdentityRequiresProjectedPrimaryKey(t *testing.T) {
	cols := []wire.Column{{Schema: "s", OriginalTable: "t", OriginalName: "b", Name: "b", Type: 252}}
	dict := Result{Columns: make([]wire.Column, 8), Rows: [][]driver.Value{{"s", "t", blobBaseTable, blobLockingEngine, "id", metadataLowerBigInt, "", int64(1)}, {"s", "t", blobBaseTable, blobLockingEngine, "b", testLongBlob, "", nil}}}
	if _, err := ResolveCursorIdentity(config.ModeMySQL, cols, []Result{dict}); !errors.Is(err, dberrors.ErrCursor) {
		t.Fatal("missing projected primary key accepted", err)
	}
}

const testLongBlob = "LONGBLOB"
