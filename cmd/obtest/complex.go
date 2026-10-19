package main

import (
	"bytes"
	"context"
	"database/sql"
	"math/big"

	"github.com/cocosip/go-oceanbase"
)

const complexSecondNumber = "-42.5"

func runComplex(ctx context.Context, s *scenario) (err error) {
	obj, err := s.create(ctx, "TYPE", "O", "AS OBJECT(N NUMBER(38,9),B RAW(20))")
	if err != nil {
		return err
	}
	arr, err := s.create(ctx, "TYPE", "A", "AS TABLE OF "+obj)
	if err != nil {
		return err
	}
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V "+obj+")")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	value := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number(exactNumber)}, {Name: "B", Value: oceanbase.Raw{0, 255}}}}
	other := oceanbase.Object{Type: obj, Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number(complexSecondNumber)}, {Name: "B", Value: oceanbase.Raw{255, 0}}}}
	array := oceanbase.Array{Type: arr, Elements: []any{value, nil, other}}
	s.observe("complex_stage", "object input")
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", 1, value); err != nil {
		return err
	}
	var got oceanbase.Object
	var items oceanbase.Array
	s.observe("complex_stage", "object query")
	if err = tx.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", 1).Scan(&got); err != nil {
		return err
	}
	var nullElement int
	s.observe("complex_stage", "collection round trip")
	if _, err = tx.ExecContext(ctx, "DECLARE a "+arr+" := ?; BEGIN ? := CASE WHEN a(2) IS NULL THEN 1 ELSE 0 END; ? := a; END;", array, sql.Out{Dest: &nullElement}, sql.Out{Dest: &items}); err != nil {
		return err
	}
	if err = checkObject(got, obj, exactNumber, []byte{0, 255}); err != nil {
		return err
	}
	if err = expect(items.Type == arr && items.Schema == got.Schema && len(items.Elements) == 3, "collection identity or order mismatch"); err != nil {
		return err
	}
	if items.Elements[1] != nil {
		if nullElement != 0 {
			return checkFailure("server NULL object representation inconsistent")
		}
		if err = checkNullObject(items.Elements[1], obj, got.Schema); err != nil {
			return err
		}
		s.observe("null_object_representation", "object with NULL attributes (Connector/J-compatible)")
	} else if nullElement != 1 {
		return checkFailure("server NULL element predicate inconsistent")
	}
	for _, index := range []int{0, 2} {
		v, ok := items.Elements[index].(oceanbase.Object)
		if !ok || v.Schema != got.Schema {
			return checkFailure("collection element lost its descriptor")
		}
		number, raw := exactNumber, []byte{0, 255}
		if index == 2 {
			number, raw = complexSecondNumber, []byte{255, 0}
		}
		if err = checkObject(v, obj, number, raw); err != nil {
			return err
		}
	}
	var out oceanbase.Object
	s.observe("complex_stage", "object output")
	if _, err = tx.ExecContext(ctx, "BEGIN ? := "+obj+"(42,HEXTORAW('00FF')); END;", sql.Out{Dest: &out}); err != nil {
		return err
	}
	if err = checkObject(out, obj, "42", []byte{0, 255}); err != nil {
		return err
	}
	if err = expect(out.Schema == got.Schema, "object output schema mismatch"); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 0)
}

func checkObject(value oceanbase.Object, name, wantNumber string, wantRaw []byte) error {
	if value.Type != name || value.Schema == "" || len(value.Fields) != 2 || value.Fields[0].Name != "N" || value.Fields[1].Name != "B" {
		return checkFailure("object identity or attribute order mismatch")
	}
	number, ok := value.Fields[0].Value.(oceanbase.Number)
	if !ok {
		return checkFailure("object NUMBER lost exact representation")
	}
	got, ok := new(big.Rat).SetString(string(number))
	want, _ := new(big.Rat).SetString(wantNumber)
	if !ok || got.Cmp(want) != 0 {
		return checkFailure("object NUMBER precision mismatch")
	}
	raw, ok := value.Fields[1].Value.(oceanbase.Raw)
	return expect(ok && bytes.Equal(raw, wantRaw), "object RAW content mismatch")
}

func checkNullObject(value any, name, schema string) error {
	n, ok := value.(oceanbase.Object)
	if !ok || n.Type != name || n.Schema != schema || len(n.Fields) != 2 || n.Fields[0].Name != "N" || n.Fields[1].Name != "B" {
		return checkFailure("NULL object identity mismatch")
	}
	for _, f := range n.Fields {
		if f.Value != nil {
			return checkFailure("NULL object attribute mismatch")
		}
	}
	return nil
}
