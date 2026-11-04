package valuecodec

import (
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestNamedComplexConstructors(t *testing.T) {
	d := textObjectDescriptor()
	v := types.Object{Schema: "S", Type: testComplexRoot, Fields: []types.Attribute{{Name: "C", Value: types.Object{Type: testComplexChild, Fields: []types.Attribute{{Name: "N", Value: types.Number("1e2")}, {Name: "S", Value: "患者😀'"}}}}, {Name: testComplexTag}}}
	want := `"S"."ROOT"("S"."CHILD"(100,UNISTR('\60A3\8005\D83D\DE00\0027')),NULL)`
	got, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{Charset: charsetLatin1, NCharset: charsetUTF8MB4}, 1024, 4096)
	if err != nil || got != want {
		t.Fatal(got, err)
	}
	a := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: `S"X`, Name: "TA"}, Array: true, Fields: []Field{{Type: valueformat.TypeNameNumber}}}
	for _, tc := range []struct {
		value types.Array
		want  string
	}{
		{types.Array{Type: "TA", Elements: []any{types.Number("7"), nil, int64(-9)}}, `"S""X"."TA"(7,NULL,-9)`},
		{types.Array{Type: "TA"}, `"S""X"."TA"()`},
		{types.Array{Type: "TA", Null: true}, "NULL"},
	} {
		got, err := ClientLiteral(ResolvedComplex{a, tc.value}, config.ModeOracle, config.SessionOptions{Charset: charsetUTF8MB4}, 1024, int64(len(tc.want)))
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
		if _, err := ClientLiteral(ResolvedComplex{a, tc.value}, config.ModeOracle, config.SessionOptions{Charset: charsetUTF8MB4}, 1024, int64(len(tc.want)-1)); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal(err)
		}
	}
	if _, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeMySQL, config.SessionOptions{}, 1024, 4096); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestComplexConstructorEncodedAndShapeBudgets(t *testing.T) {
	d := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T"}, Fields: []Field{{Name: "V", Type: valueformat.TypeNameVarchar2}}}
	v := types.Object{Type: "T", Fields: []types.Attribute{{Name: "V", Value: "中'"}}}
	want := `"S"."T"('中''')`
	encoded, err := charset.Encode("gbk", want)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{int64(len(encoded)), int64(len(encoded) - 1)} {
		got, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{Charset: "gbk"}, 1024, limit)
		if limit == int64(len(encoded)) {
			if err != nil || got != want {
				t.Fatal(got, err)
			}
		} else if !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal(err)
		}
	}
	for _, v := range []types.Object{
		{Type: "T", Fields: []types.Attribute{{Name: "WRONG", Value: "x"}}},
		{Type: "T", Fields: []types.Attribute{{Name: "V", Type: valueformat.TypeNameNumber, Value: "x"}}},
		{Type: "T", Fields: []types.Attribute{{Name: "V", Value: []byte{1}}}},
	} {
		if _, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{Charset: charsetUTF8MB4}, 1024, 4096); err == nil {
			t.Fatal("invalid shape or value accepted")
		}
	}
	e := constructorEncoder{options: config.SessionOptions{Charset: charsetUTF8MB4}, maxLOBBytes: 1024, limit: 4096, nodes: 1}
	if err := e.value(d, v, 0, false, false); !errors.Is(err, dberrors.ErrComplex) {
		t.Fatal(err)
	}
}

func TestComplexConstructorScalarIdentity(t *testing.T) {
	d := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T"}, Fields: []Field{{Name: "V"}}}
	wall := time.Date(2026, 11, 2, 3, 4, 5, 0, time.FixedZone("wall", 3600))
	for _, tc := range []struct {
		typ   string
		value any
		want  string
	}{
		{valueformat.TypeNameVarchar2, "x'\x00\\", "REPLACE('x''01\\','01',CHR(0))"},
		{valueformat.TypeNameRaw, types.Raw{0, 255}, "HEXTORAW('00ff')"},
		{valueformat.TypeNameDate, wall, "TO_DATE('2026-11-02 03:04:05','YYYY-MM-DD HH24:MI:SS')"},
		{valueformat.TypeNameTimestamp, "2026-11-02 03:04:05.123456789", "TIMESTAMP '2026-11-02 03:04:05.123456789'"},
	} {
		d.Fields[0].Type = tc.typ
		v := types.Object{Type: "T", Fields: []types.Attribute{{Name: "V", Value: tc.value}}}
		got, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}, 1024, 4096)
		if err != nil || got != `"S"."T"(`+tc.want+`)` {
			t.Fatal(got, err)
		}
	}
}

func TestComplexConstructorNationalBypassesClientCharset(t *testing.T) {
	d := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T"}, Fields: []Field{{Name: "V", Type: valueformat.TypeNameNVarchar2}}}
	v := types.Object{Type: "T", Fields: []types.Attribute{{Name: "V", Value: "😀"}}}
	got, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{Charset: charsetGBK, NCharset: charsetUTF16}, 1024, 4096)
	if err != nil || got != `"S"."T"(UNISTR('\D83D\DE00'))` {
		t.Fatal("national constructor incorrectly required client encoding", got, err)
	}
}

func TestComplexConstructorRejectsUnsafeShapes(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob, valueformat.TypeNameRefCursor, "UNKNOWN"} {
		d := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T"}, Fields: []Field{{Name: "V", Type: typ}}}
		v := types.Object{Type: "T", Fields: []types.Attribute{{Name: "V"}}}
		if _, err := ClientLiteral(ResolvedComplex{d, v}, config.ModeOracle, config.SessionOptions{}, 1024, 4096); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(typ, err)
		}
	}
	d := textObjectDescriptor()
	a := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "TA"}, Array: true, Fields: []Field{{Type: testComplexRoot, Child: d}}}
	for _, v := range []any{nil, types.Object{Type: testComplexRoot, Null: true}, types.Object{Type: testComplexRoot, Fields: []types.Attribute{{Name: "C"}, {Name: testComplexTag}}}} {
		if _, err := ClientLiteral(ResolvedComplex{a, types.Array{Type: "TA", Elements: []any{v}}}, config.ModeOracle, config.SessionOptions{}, 1024, 4096); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("NULL object subtree in collection accepted", err)
		}
	}
	d.Fields[0].Child = d
	if _, err := ClientLiteral(ResolvedComplex{d, types.Object{Null: true}}, config.ModeOracle, config.SessionOptions{}, 1024, 4096); err == nil {
		t.Fatal("unbounded descriptor accepted")
	}
	a.Key.Name = ""
	if _, err := ClientLiteral(ResolvedComplex{a, types.Array{}}, config.ModeOracle, config.SessionOptions{}, 1024, 4096); err == nil {
		t.Fatal("anonymous constructor accepted")
	}
}
