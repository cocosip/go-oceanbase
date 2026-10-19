package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const complexFixtureSchema = "APP"

const complexFixtureObject = "OBT_OFFLINE_O"
const complexFixtureArray = "OBT_OFFLINE_A"
const complexFixtureWrongSchema = "OTHER"

func TestComplexOutputAssertions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*oceanbase.Object)
		wantOK bool
	}{
		{name: "exact", wantOK: true},
		{name: "equivalent-decimal", change: func(o *oceanbase.Object) { o.Fields[0].Value = oceanbase.Number("42.000") }, wantOK: true},
		{name: "wrong-number", change: func(o *oceanbase.Object) { o.Fields[0].Value = oceanbase.Number("41") }},
		{name: "wrong-raw", change: func(o *oceanbase.Object) { o.Fields[1].Value = oceanbase.Raw{255, 0} }},
		{name: "missing-schema", change: func(o *oceanbase.Object) { o.Schema = "" }},
		{name: "different-schema", change: func(o *oceanbase.Object) { o.Schema = complexFixtureWrongSchema }},
		{name: "wrong-attribute", change: func(o *oceanbase.Object) { o.Fields[0].Name = "WRONG" }},
		{name: "wrong-attribute-order", change: func(o *oceanbase.Object) { o.Fields[0], o.Fields[1] = o.Fields[1], o.Fields[0] }},
		{name: "null-output", change: func(o *oceanbase.Object) { *o = oceanbase.Object{Null: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := complexFixtureValue("42", oceanbase.Raw{0, 255})
			if tc.change != nil {
				tc.change(&out)
			}
			checkComplexScenario(t, complexFixtureCollection(), out, tc.wantOK)
		})
	}
}

func TestComplexCollectionAssertions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*oceanbase.Array)
		wantOK bool
	}{
		{name: "distinct-elements", wantOK: true},
		{name: "server-null-object", wantOK: true, change: func(a *oceanbase.Array) {
			a.Elements[1] = oceanbase.Object{Schema: complexFixtureSchema, Type: complexFixtureObject, Fields: []oceanbase.Attribute{{Name: "N"}, {Name: "B"}}}
		}},
		{name: "null-object-wrong-attributes", change: func(a *oceanbase.Array) {
			a.Elements[1] = oceanbase.Object{Schema: complexFixtureSchema, Type: complexFixtureObject, Fields: []oceanbase.Attribute{{Name: "B"}, {Name: "N"}}}
		}},
		{name: "equivalent-tail-number", wantOK: true, change: func(a *oceanbase.Array) { a.Elements[2] = complexFixtureValue("-42.500", oceanbase.Raw{255, 0}) }},
		{name: "swapped-elements", change: func(a *oceanbase.Array) { a.Elements[0], a.Elements[2] = a.Elements[2], a.Elements[0] }},
		{name: "repeated-head", change: func(a *oceanbase.Array) { a.Elements[2] = a.Elements[0] }},
		{name: "repeated-tail", change: func(a *oceanbase.Array) { a.Elements[0] = a.Elements[2] }},
		{name: "tail-raw-changed", change: func(a *oceanbase.Array) {
			a.Elements[2] = complexFixtureValue(complexSecondNumber, oceanbase.Raw{0, 255})
		}},
		{name: "null-element-changed", change: func(a *oceanbase.Array) { a.Elements[1] = a.Elements[0] }},
		{name: "missing-element", change: func(a *oceanbase.Array) { a.Elements = a.Elements[:2] }},
		{name: "missing-collection-schema", change: func(a *oceanbase.Array) { a.Schema = "" }},
		{name: "wrong-collection-schema", change: func(a *oceanbase.Array) { a.Schema = complexFixtureWrongSchema }},
		{name: "wrong-element-schema", change: func(a *oceanbase.Array) {
			o := a.Elements[2].(oceanbase.Object)
			o.Schema = complexFixtureWrongSchema
			a.Elements[2] = o
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			array := complexFixtureCollection()
			if tc.change != nil {
				tc.change(&array)
			}
			checkComplexScenario(t, array, complexFixtureValue("42", oceanbase.Raw{0, 255}), tc.wantOK)
		})
	}
}

func checkComplexScenario(t *testing.T, array oceanbase.Array, out oceanbase.Object, valid bool) {
	t.Helper()
	s := complexAssertionScenario(t, array, out, valid)
	result := runScenario(context.Background(), s, runComplex)
	want := statusFail
	if valid {
		want = statusPass
	}
	if result.Status != want || len(result.Objects) != 3 || len(result.CleanupErrors) != 0 {
		t.Fatalf("complex workflow: %+v; want %s and successful cleanup", result, want)
	}
	for _, object := range result.Objects {
		if object.State != fixtureDroppedState {
			t.Fatalf("object was not cleaned: %+v", object)
		}
	}
}

func complexFixtureCollection() oceanbase.Array {
	return oceanbase.Array{Schema: complexFixtureSchema, Type: complexFixtureArray, ElementType: complexFixtureObject, Elements: []any{
		complexFixtureValue(exactNumber, oceanbase.Raw{0, 255}), nil, complexFixtureValue(complexSecondNumber, oceanbase.Raw{255, 0}),
	}}
}

func complexFixtureValue(number string, raw oceanbase.Raw) oceanbase.Object {
	return oceanbase.Object{Schema: complexFixtureSchema, Type: complexFixtureObject, Fields: []oceanbase.Attribute{
		{Name: "N", Type: "NUMBER", Value: oceanbase.Number(number)},
		{Name: "B", Type: "RAW", Value: raw},
	}}
}

func complexAssertionScenario(t *testing.T, array oceanbase.Array, out oceanbase.Object, valid bool) *scenario {
	t.Helper()
	value := complexFixtureValue(exactNumber, oceanbase.Raw{0, 255})
	objectJSON, err := value.Value()
	if err != nil {
		t.Fatal(err)
	}
	return fixtureScenario(t, []fixtureStep{
		{query: "CREATE TYPE OBT_OFFLINE_O AS OBJECT(N NUMBER(38,9),B RAW(20))"},
		{query: "CREATE TYPE OBT_OFFLINE_A AS TABLE OF OBT_OFFLINE_O"},
		{query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V OBT_OFFLINE_O)"},
		{query: "BEGIN"},
		{query: "INSERT INTO OBT_OFFLINE_T VALUES (?,?)", affected: 1, inspect: func(args []driver.NamedValue) {
			if len(args) != 2 {
				t.Fatal("missing object input")
			}
		}},
		{query: "SELECT V FROM OBT_OFFLINE_T WHERE ID=?", values: [][]driver.Value{{objectJSON}}},
		{query: "DECLARE a OBT_OFFLINE_A := ?; BEGIN ? := CASE WHEN a(2) IS NULL THEN 1 ELSE 0 END; ? := a; END;", inspect: func(args []driver.NamedValue) {
			if len(args) != 3 {
				t.Fatal("missing collection bindings")
			}
			a := args[0].Value.(oceanbase.Array)
			if len(a.Elements) != 3 || a.Elements[1] != nil {
				t.Fatal("missing collection input elements")
			}
			for i, number := range []string{exactNumber, "-42.5"} {
				o := a.Elements[i*2].(oceanbase.Object)
				raw := []byte{0, 255}
				if i == 1 {
					raw = []byte{255, 0}
				}
				if o.Fields[0].Value != oceanbase.Number(number) || !bytes.Equal(o.Fields[1].Value.(oceanbase.Raw), raw) {
					t.Error("collection seed does not distinguish ordered values")
				}
			}
			nullValue := 1
			if array.Elements[1] != nil {
				nullValue = 0
			}
			*args[1].Value.(sql.Out).Dest.(*int) = nullValue
			*args[2].Value.(sql.Out).Dest.(*oceanbase.Array) = array
		}},
		{query: "BEGIN ? := OBT_OFFLINE_O(42,HEXTORAW('00FF')); END;", optional: !valid, inspect: func(args []driver.NamedValue) {
			if len(args) != 1 {
				t.Fatal("missing object output binding")
			}
			binding, ok := args[0].Value.(sql.Out)
			if !ok {
				t.Fatalf("unexpected output binding: %T", args[0].Value)
			}
			*binding.Dest.(*oceanbase.Object) = out
		}},
		{query: "ROLLBACK"},
		{query: fixtureCountSQL, values: [][]driver.Value{{int64(0)}}, optional: !valid},
		{query: fixtureDropTableSQL},
		{query: "DROP TYPE OBT_OFFLINE_A"},
		{query: "DROP TYPE OBT_OFFLINE_O"},
	})
}
