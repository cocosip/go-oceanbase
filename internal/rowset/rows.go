// Package rowset owns fully materialized result sets and their independent readers.
package rowset

import (
	"context"
	"database/sql/driver"
	"io"
	"reflect"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Set contains decoded columns and values after all network resources are materialized.
type Set struct {
	Columns []wire.Column
	Rows    [][]driver.Value
}

// Rows iterates a materialized snapshot without retaining a connection or operation gate.
type Rows struct {
	ctx     context.Context
	mode    config.Mode
	options config.SessionOptions
	columns []wire.Column
	sets    []Set
	row     int
}

// New takes ownership of the sets and preserves the caller's iteration context.
func New(ctx context.Context, mode config.Mode, parseTime bool, sets []Set) *Rows {
	return NewWithSession(ctx, mode, config.SessionOptions{ParseTime: parseTime}, sets)
}

// NewWithSession takes ownership of the complete immutable session profile.
func NewWithSession(ctx context.Context, mode config.Mode, options config.SessionOptions, sets []Set) *Rows {
	r := &Rows{ctx: ctx, mode: mode, options: options, sets: sets}
	if len(sets) > 0 {
		r.columns = sets[0].Columns
	}
	return r
}

// Columns returns the current result's column names.
func (r *Rows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, column := range r.columns {
		names[i] = column.Name
	}
	return names
}

// Next copies the next decoded row after checking the iteration context.
func (r *Rows) Next(dest []driver.Value) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if len(r.sets) == 0 || r.row >= len(r.sets[0].Rows) {
		return io.EOF
	}
	copy(dest, r.sets[0].Rows[r.row])
	r.row++
	return nil
}

// Close releases the reader's materialized sets.
func (r *Rows) Close() error {
	r.sets = nil
	return nil
}

// HasNextResultSet reports whether another materialized result is available.
func (r *Rows) HasNextResultSet() bool { return len(r.sets) > 1 }

// NextResultSet advances to the next set without network activity.
func (r *Rows) NextResultSet() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if len(r.sets) <= 1 {
		return io.EOF
	}
	r.sets[0] = Set{}
	r.sets = r.sets[1:]
	r.row, r.columns = 0, r.sets[0].Columns
	return nil
}

// ColumnTypeDatabaseTypeName returns the compatibility mode's type name.
func (r *Rows) ColumnTypeDatabaseTypeName(index int) string {
	return valuecodec.TextTypeName(r.columns[index], r.mode, r.options)
}

// ColumnTypeNullable reports the protocol's nullability flag.
func (r *Rows) ColumnTypeNullable(index int) (bool, bool) {
	return r.columns[index].Flags&1 == 0, true
}

// ColumnTypeLength reports the protocol-defined column length.
func (r *Rows) ColumnTypeLength(index int) (int64, bool) {
	return valuecodec.ColumnLength(r.columns[index], r.mode)
}

// ColumnTypePrecisionScale reports decimal precision and scale.
func (r *Rows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	return valuecodec.ColumnPrecisionScale(r.columns[index], r.mode)
}

// ColumnTypeScanType reports the standard destination type for the current column.
func (r *Rows) ColumnTypeScanType(index int) reflect.Type {
	return valuecodec.ColumnScanType(r.columns[index], r.mode, r.options)
}

var (
	_ driver.Rows                           = (*Rows)(nil)
	_ driver.RowsNextResultSet              = (*Rows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*Rows)(nil)
	_ driver.RowsColumnTypeNullable         = (*Rows)(nil)
	_ driver.RowsColumnTypeScanType         = (*Rows)(nil)
	_ driver.RowsColumnTypeLength           = (*Rows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*Rows)(nil)
)
