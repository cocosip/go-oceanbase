package rowset

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// MetadataRows preserves the fixed dictionary schema independently of server mode.
type MetadataRows struct {
	driver.Rows
	columns []wire.Column
}

// NewMetadata defers scalar conversion until Next, preserving dictionary error timing.
func NewMetadata(ctx context.Context, sets []Set) *MetadataRows {
	r := New(ctx, config.ModeMySQL, false, sets)
	return WrapMetadata(r, r.columns)
}

// WrapMetadata projects a live dictionary response without buffering its rows.
func WrapMetadata(rows driver.Rows, columns []wire.Column) *MetadataRows {
	return &MetadataRows{Rows: rows, columns: columns}
}

// Columns returns the public dictionary names independently of server labels.
func (r *MetadataRows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, column := range r.columns {
		names[i] = column.Name
	}
	return names
}

// ColumnTypeDatabaseTypeName returns the fixed public dictionary type.
func (r *MetadataRows) ColumnTypeDatabaseTypeName(i int) string {
	if r.columns[i].Type == 8 {
		return "BIGINT"
	}
	return "VARCHAR"
}

// ColumnTypeScanType returns the fixed dictionary scalar destination.
func (r *MetadataRows) ColumnTypeScanType(i int) reflect.Type {
	if r.columns[i].Type == 8 {
		return reflect.TypeFor[int64]()
	}
	return reflect.TypeFor[string]()
}

// ColumnTypeNullable preserves NULL for every dictionary column.
func (*MetadataRows) ColumnTypeNullable(int) (bool, bool) { return true, true }

// Next converts dictionary text and integer values into the fixed public schema.
func (r *MetadataRows) Next(values []driver.Value) error {
	if err := r.Rows.Next(values); err != nil {
		return err
	}
	for i, column := range r.columns {
		if values[i] == nil {
			continue
		}
		if column.Type == 8 {
			switch v := values[i].(type) {
			case int64:
			case string:
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					return fmt.Errorf("oceanbase: invalid numeric metadata column %s", column.Name)
				}
				values[i] = n
			default:
				return fmt.Errorf("oceanbase: invalid numeric metadata column %s", column.Name)
			}
		} else {
			switch v := values[i].(type) {
			case string:
			case []byte:
				values[i] = string(v)
			default:
				return fmt.Errorf("oceanbase: invalid text metadata column %s", column.Name)
			}
		}
	}
	return nil
}
