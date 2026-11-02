package valuecodec

import (
	"errors"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func textObjectDescriptor() *Descriptor {
	child := &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "CHILD"}, Fields: []Field{{Name: "N", Type: valueformat.TypeNameNumber}, {Name: "S", Type: valueformat.TypeNameNVarchar2}}}
	return &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "ROOT", Version: 7}, Fields: []Field{{Name: "C", Type: "CHILD", Child: child}, {Name: "TAG", Type: valueformat.TypeNameNumber}}}
}

func TestComplexTextObjects(t *testing.T) {
	d := textObjectDescriptor()
	for _, text := range []string{"ROOT(CHILD(7, 'a,b (c) ''q'''), 9)", " ROOT ( CHILD(7, 'a,b (c) 'q''), 9 ) "} {
		got, err := DecodeComplexText(d, []byte(text), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		child := got.Fields[0].Value.(types.Object)
		if got.Version != 7 || got.Fields[1].Value != types.Number("9") || child.Fields[0].Value != types.Number("7") || child.Fields[1].Value != "a,b (c) 'q'" {
			t.Fatal(got)
		}
	}
	for _, text := range []string{"ROOT(NULL,NULL)", "ROOT(CHILD(NULL, ''), NULL)", "ROOT(CHILD(1e-2, '患者😀\\'), -3.20)"} {
		if _, err := DecodeComplexText(d, []byte(text), 1<<20); err != nil {
			t.Fatal(text, err)
		}
	}
	for _, text := range []string{"ROOT(NULL,NULL) trailing", "ROOT(NULL)", "ROOT(NULL,NULL,1)", "OTHER(NULL,NULL)", "ROOT(CHILD(1,'bad),2)", "ROOT(CHILD(abc,'x'),2)", "ROOT(CHILD(1,NULLX),2)", "ROOT(CHILD(1,'x') 2)"} {
		if _, err := DecodeComplexText(d, []byte(text), 1<<20); err == nil {
			t.Fatal("malformed text accepted", text)
		}
	}
	if _, err := DecodeComplexText(d, []byte{'R', 255}, 1<<20); !errors.Is(err, charset.ErrEncoding) {
		t.Fatal(err)
	}
	if _, err := DecodeComplexText(d, []byte("ROOT(NULL,NULL)"), 8); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
}

func TestComplexTextDescriptorBoundaries(t *testing.T) {
	for _, typ := range []string{"RAW", "DATE", "TIMESTAMP", "BLOB", "CLOB", "NCLOB", "REF CURSOR", "UNKNOWN"} {
		d := textObjectDescriptor()
		d.Fields[0].Child.Fields[1].Type = typ
		if _, err := DecodeComplexText(d, []byte("ROOT(NULL,NULL)"), 1<<20); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(typ, err)
		}
	}
	d := textObjectDescriptor()
	d.Fields[0].Child.Array = true
	if _, err := DecodeComplexText(d, []byte("ROOT(NULL,NULL)"), 1<<20); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	d = textObjectDescriptor()
	d.Fields[0].Child = d
	if _, err := DecodeComplexText(d, []byte("ROOT(NULL,NULL)"), 1<<20); err == nil {
		t.Fatal("cyclic descriptor accepted")
	}
	d = &Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T"}}
	for i := 0; i <= valueformat.MaxComplexNodes; i++ {
		d.Fields = append(d.Fields, Field{Name: "F", Type: valueformat.TypeNameNumber})
	}
	if _, err := DecodeComplexText(d, []byte("T("+strings.Repeat("NULL,", len(d.Fields)-1)+"NULL)"), 1<<20); err == nil {
		t.Fatal("unbounded descriptor accepted")
	}
}
