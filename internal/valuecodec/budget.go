package valuecodec

import (
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// SnapshotValuesSize counts variable-width values in a buffered response.
func SnapshotValuesSize(values []driver.Value) int64 {
	var n int64
	for _, value := range values {
		switch v := value.(type) {
		case []byte:
			n += int64(len(v))
		case string:
			n += int64(len(v))
		}
	}
	return n
}

// SnapshotMetadataSize retains the original buffered column metadata budget.
func SnapshotMetadataSize(columns []wire.Column) int64 {
	n := int64(32)
	for _, col := range columns {
		n += 128 + int64(len(col.Catalog)+len(col.Schema)+len(col.Table)+len(col.OriginalTable)+len(col.Name)+len(col.OriginalName)+len(col.Extra)+len(col.ComplexSchema)+len(col.ComplexName)+len(col.ElementSchema)+len(col.ElementName))
	}
	return n
}
