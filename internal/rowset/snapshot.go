package rowset

import (
	"context"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ValuesSize and MetadataSize share the buffered-response budget that valuecodec
// owns; keeping one formula prevents the two accounting paths from drifting.
func ValuesSize(values []driver.Value) int64 { return valuecodec.SnapshotValuesSize(values) }

// MetadataSize counts a result's column metadata using the original snapshot budget.
func MetadataSize(columns []wire.Column) int64 { return valuecodec.SnapshotMetadataSize(columns) }

// SnapshotSize includes row storage, values, and nested materialized child results.
func SnapshotSize(rows [][]driver.Value) int64 {
	var n int64
	for _, row := range rows {
		n += 24 + int64(len(row))*16 + ValuesSize(row)
		for _, value := range row {
			if child, ok := value.(*Rows); ok {
				for _, set := range child.sets {
					n += MetadataSize(set.Columns) + SnapshotSize(set.Rows)
				}
			}
		}
	}
	return n
}

// CloneSnapshot gives each static cursor fetch independent byte values and child readers.
func CloneSnapshot(ctx context.Context, rows [][]driver.Value) [][]driver.Value {
	cloned := make([][]driver.Value, len(rows))
	for i, row := range rows {
		cloned[i] = append([]driver.Value(nil), row...)
		for j, value := range row {
			switch v := value.(type) {
			case []byte:
				cloned[i][j] = append([]byte{}, v...)
			case *Rows:
				sets := make([]Set, len(v.sets))
				for k, set := range v.sets {
					sets[k] = Set{Columns: set.Columns, Rows: CloneSnapshot(ctx, set.Rows)}
				}
				cloned[i][j] = &Rows{ctx: ctx, mode: v.mode, options: v.options, columns: v.columns, sets: sets}
			}
		}
	}
	return cloned
}
