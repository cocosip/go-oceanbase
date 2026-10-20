package rowset

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMetadataScalarConversion(t *testing.T) {
	columns := []wire.Column{{Name: "name", Type: 253}, {Name: "ordinal", Type: 8}}
	for _, test := range []struct {
		input, want []driver.Value
	}{
		{[]driver.Value{[]byte("n"), "2"}, []driver.Value{"n", int64(2)}},
		{[]driver.Value{"n", int64(2)}, []driver.Value{"n", int64(2)}},
		{[]driver.Value{nil, nil}, []driver.Value{nil, nil}},
	} {
		r := NewMetadata(context.Background(), []Set{{Columns: columns, Rows: [][]driver.Value{test.input}}})
		values := make([]driver.Value, 2)
		if err := r.Next(values); err != nil || !reflect.DeepEqual(values, test.want) {
			t.Fatal(values, err)
		}
	}
	for _, values := range [][]driver.Value{{int64(1), "2"}, {"n", "1.5"}, {"n", []byte("2")}} {
		r := NewMetadata(context.Background(), []Set{{Columns: columns, Rows: [][]driver.Value{values}}})
		if err := r.Next(make([]driver.Value, 2)); err == nil {
			t.Fatal("invalid dictionary representation", values)
		}
	}
}

func TestMetadataOptionalInterfaces(t *testing.T) {
	var rows driver.Rows = NewMetadata(context.Background(), nil)
	if _, ok := rows.(driver.RowsNextResultSet); ok {
		t.Fatal("dictionary rows exposed additional result sets")
	}
	if _, ok := rows.(driver.RowsColumnTypeLength); ok {
		t.Fatal("dictionary rows inferred a length from synthetic columns")
	}
	if _, ok := rows.(driver.RowsColumnTypePrecisionScale); ok {
		t.Fatal("dictionary rows inferred precision from synthetic columns")
	}
}
