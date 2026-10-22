package rowset

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestRowsIterationAndMetadata(t *testing.T) {
	columns := []wire.Column{{Name: "amount", Type: 246, Charset: 63, Length: 8, Decimals: 2, Flags: 1}}
	r := New(context.Background(), config.ModeMySQL, false, []Set{
		{Columns: columns, Rows: [][]driver.Value{{"12.34"}}},
		{Columns: []wire.Column{{Name: "created", Type: 12}}},
	})
	if !reflect.DeepEqual(r.Columns(), []string{"amount"}) || r.ColumnTypeDatabaseTypeName(0) != "DECIMAL" {
		t.Fatal("column metadata lost", r.Columns(), r.ColumnTypeDatabaseTypeName(0))
	}
	if nullable, known := r.ColumnTypeNullable(0); nullable || !known {
		t.Fatal(nullable, known)
	}
	if precision, scale, known := r.ColumnTypePrecisionScale(0); precision != 6 || scale != 2 || !known {
		t.Fatal(precision, scale, known)
	}
	if r.ColumnTypeScanType(0) != reflect.TypeFor[string]() {
		t.Fatal(r.ColumnTypeScanType(0))
	}
	dest := make([]driver.Value, 1)
	if err := r.Next(dest); err != nil || dest[0] != "12.34" {
		t.Fatal(dest, err)
	}
	if err := r.Next(dest); err != io.EOF || !r.HasNextResultSet() {
		t.Fatal("result boundary lost", err)
	}
	if err := r.NextResultSet(); err != nil || r.Columns()[0] != "created" || r.HasNextResultSet() {
		t.Fatal(r.Columns(), err)
	}
	if err := r.Next(dest); err != io.EOF {
		t.Fatal("empty result did not end", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Next(dest); err != io.EOF || r.HasNextResultSet() {
		t.Fatal("closed snapshot retained rows", err)
	}
	parsed := New(context.Background(), config.ModeOracle, true, []Set{{Columns: []wire.Column{{Type: 202}}}})
	if parsed.ColumnTypeScanType(0) != reflect.TypeFor[time.Time]() {
		t.Fatal("ParseTime metadata lost")
	}
}

func TestRowsPreserveMySQLCompatibilityProfile(t *testing.T) {
	column := wire.Column{Name: "body", Type: 250, Charset: 45, NewMetadata: true}
	old := NewWithSession(context.Background(), config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 5}, []Set{{Columns: []wire.Column{column}}})
	modern := NewWithSession(context.Background(), config.ModeMySQL, config.SessionOptions{CompatibleMysqlVersion: 8}, []Set{{Columns: []wire.Column{column}}})
	if old.ColumnTypeDatabaseTypeName(0) != "VARCHAR" || modern.ColumnTypeDatabaseTypeName(0) != "TEXT" {
		t.Fatalf("compatibility profile lost: old=%q modern=%q", old.ColumnTypeDatabaseTypeName(0), modern.ColumnTypeDatabaseTypeName(0))
	}
}

func TestSnapshotCloneIsolation(t *testing.T) {
	columns := []wire.Column{{Name: "bytes", Type: 203, Charset: 63}}
	child := New(context.Background(), config.ModeOracle, false, []Set{{Columns: columns, Rows: [][]driver.Value{{[]byte("child")}}}})
	cache := [][]driver.Value{{[]byte("parent"), child}}
	first := CloneSnapshot(context.Background(), cache)
	second := CloneSnapshot(context.Background(), cache)
	first[0][0].([]byte)[0] = 'X'
	firstChild := first[0][1].(*Rows)
	values := make([]driver.Value, 1)
	if err := firstChild.Next(values); err != nil {
		t.Fatal(err)
	}
	values[0].([]byte)[0] = 'Y'
	if err := firstChild.Close(); err != nil {
		t.Fatal(err)
	}
	if string(cache[0][0].([]byte)) != "parent" || string(second[0][0].([]byte)) != "parent" {
		t.Fatal("fetch mutated the static cache")
	}
	if err := second[0][1].(*Rows).Next(values); err != nil || string(values[0].([]byte)) != "child" {
		t.Fatal("nested child reader shared storage or iteration", values, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cloned := CloneSnapshot(ctx, cache)
	cancel()
	if err := cloned[0][1].(*Rows).Next(values); !errors.Is(err, context.Canceled) {
		t.Fatal("nested reader lost fetch context", err)
	}
	if err := child.Next(values); err != nil || string(values[0].([]byte)) != "child" {
		t.Fatal("fetch context escaped into cache", values, err)
	}
}

func TestSnapshotBudgetAndContext(t *testing.T) {
	columns := []wire.Column{{Catalog: "def", Name: "n", Extra: []byte("extra")}}
	child := New(context.Background(), config.ModeOracle, false, []Set{{Columns: columns, Rows: [][]driver.Value{{"abc", []byte("de")}}}})
	rows := [][]driver.Value{{child, int64(7)}}
	want := int64(24+2*16) + 32 + 128 + int64(len("defnextra")) + int64(24+2*16+3+2)
	if got := SnapshotSize(rows); got != want {
		t.Fatalf("nested snapshot budget: got %d, want %d", got, want)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := New(ctx, config.ModeMySQL, false, []Set{{Columns: columns}, {Columns: columns}})
	cancel()
	if err := r.Next(make([]driver.Value, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal("empty snapshot ignored canceled iteration", err)
	}
	if err := r.NextResultSet(); !errors.Is(err, context.Canceled) {
		t.Fatal("result advance ignored canceled iteration", err)
	}
}
