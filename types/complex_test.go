package types

import (
	"bytes"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

func TestComplexLossless(t *testing.T) {
	want := Object{Schema: "S", Type: "T", Version: 7, Fields: []Attribute{
		{Name: "N", Type: valueformat.TypeNameNumber, Value: Number("12345678901234567890.00100")},
		{Name: "B", Type: valueformat.TypeNameRaw, Value: Raw{0, 255}},
		{Name: "D", Type: valueformat.TypeNameDate, Value: "2025-10-17 12:34:56"},
		{Name: "A", Type: "TA", Value: Array{Schema: "S", Type: "TA", ElementType: valueformat.TypeNameNumber, Elements: []any{nil, Number("1.20")}}},
		{Name: "E", Type: "TA", Value: Array{Schema: "S", Type: "TA", ElementType: valueformat.TypeNameNumber, Elements: []any{}}},
		{Name: "Z", Type: valueformat.TypeNameVarchar2},
	}}
	value, err := want.Value()
	if err != nil || !driver.IsValue(value) || !json.Valid([]byte(value.(string))) || !bytes.Contains([]byte(value.(string)), []byte("AP8=")) {
		t.Fatal(value, err)
	}
	var got Object
	if err = got.Scan(value); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
	old := got
	if err = got.Scan(`{"format":99,"kind":"object"}`); !errors.Is(err, dberrors.ErrComplex) || !reflect.DeepEqual(got, old) {
		t.Fatal("invalid Scan changed destination", err)
	}
	if err = got.Scan(nil); err != nil || !got.Null {
		t.Fatal(got, err)
	}
	if v, err := got.Value(); err != nil || v != nil {
		t.Fatal(v, err)
	}
}

func TestComplexDepth(t *testing.T) {
	var v any = Object{Type: "T", Fields: []Attribute{}}
	for i := 0; i < 34; i++ {
		v = Object{Type: "T", Fields: []Attribute{{Name: "N", Type: "T", Value: v}}}
	}
	if _, err := v.(Object).Value(); !errors.Is(err, dberrors.ErrComplex) {
		t.Fatal(err)
	}
	var o Object
	if err := o.Scan(strings.Repeat("{", 100)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestComplexNullRepresentation(t *testing.T) {
	var object Object
	if err := object.Scan(`{"format":1,"kind":"object","schema":"S","type":"T","version":7,"null":true}`); err != nil {
		t.Fatal(err)
	}
	if !object.Null || object.Schema != "S" || object.Type != "T" || object.Version != 7 || len(object.Fields) != 0 {
		t.Fatal("NULL object lost its description", object)
	}
	if value, err := object.Value(); err != nil || value != nil {
		t.Fatal("NULL object was bound as a document", value, err)
	}
	var array Array
	if err := array.Scan(`{"format":1,"kind":"array","schema":"S","type":"TA","version":8,"null":true,"elementType":"NUMBER","elementVersion":9}`); err != nil {
		t.Fatal(err)
	}
	if !array.Null || array.Schema != "S" || array.Type != "TA" || array.Version != 8 || array.ElementType != "NUMBER" || array.ElementVersion != 9 || len(array.Elements) != 0 {
		t.Fatal("NULL array lost its description", array)
	}
	if value, err := array.Value(); err != nil || value != nil {
		t.Fatal("NULL array was bound as a document", value, err)
	}
}
