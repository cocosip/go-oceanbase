package materialize

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestComplexAnonymousArray(t *testing.T) {
	op := decoderOperation(t)
	resolver := new(catalog.Resolver)
	c := New(op, resolver)
	v := types.Array{ElementType: valueformat.TypeNameNumber, Elements: []any{types.Number("1.20"), nil}}
	p, err := parameters.EncodeValue(context.Background(), op, resolver, v, 4096)
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 0}) || !reflect.DeepEqual(p.Data, []byte{2, 2, 4, '1', '.', '2', '0'}) {
		t.Fatal(p, err)
	}
	column := wire.Column{Type: 160, ElementType: 0}
	value, err := c.complexValueLocked(context.Background(), column, []byte{2, 8, 4, '1', '.', '2', '0'})
	var got types.Array
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || !reflect.DeepEqual(got, v) {
		t.Fatal(got, err)
	}
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		protocol := op.Protocol()
		protocol.Mode = mode
		op.SetIdentity(protocol)
		if mode == config.ModeMySQL {
			if _, err = parameters.EncodeValue(context.Background(), op, resolver, v, 4096); !errors.Is(err, dberrors.ErrUnsupported) {
				t.Fatal(err)
			}
		} else if _, err = valuecodec.ClientLiteral(v, op.Protocol().Mode, op.Config().Session, op.Config().Limits.MaxLOBBytes, 4096); err == nil {
			t.Fatal("complex JSON interpolated into SQL")
		}
	}
}

func TestComplexCharset(t *testing.T) {
	op := decoderOperation(t)
	resolver := new(catalog.Resolver)
	c := New(op, resolver)
	cfg := op.Config()
	cfg.Session.Charset = "gb18030"
	op.SetConfig(cfg)
	data, err := charset.Encode("gb18030", "中文")
	if err != nil {
		t.Fatal(err)
	}
	budget := op.Config().Limits.MaxMessageBytes
	v, err := c.decodeComplexFieldLocked(context.Background(), valuecodec.Field{Type: valueformat.TypeNameVarchar2}, wire.ComplexValue{Data: data}, 0, &budget)
	if err != nil || v != "中文" {
		t.Fatal(v, err)
	}
	v, err = c.decodeComplexFieldLocked(context.Background(), valuecodec.Field{Type: valueformat.TypeNameClob}, wire.ComplexValue{Data: inlineComplexLOB(string(data))}, 0, &budget)
	if err != nil || v != "中文" {
		t.Fatal("nested CLOB charset", v, err)
	}
}

func TestComplexAnonymousObjectArray(t *testing.T) {
	op, resolver, _ := materializeTypeOperation(t, 7, []string{"N"}, []string{valueformat.TypeNameNumber})
	c := New(op, resolver)
	header := append(testpeer.TextColumn("v", 160, 45, 0), 0, 0, 0, 160, 1, 'S', 1, 'T', 7)
	column, err := wire.DecodeColumn(header)
	if err != nil {
		t.Fatal(err)
	}
	value, err := c.complexValueLocked(context.Background(), column, []byte{2, 8, 0, 1, '7'})
	var got types.Array
	if err != nil {
		t.Fatal(err)
	}
	if err = got.Scan(value); err != nil || got.Schema != "S" || got.Type != "" || got.ElementType != "T" || got.ElementVersion != 7 || len(got.Elements) != 2 || got.Elements[1] != nil {
		t.Fatal(got, err)
	}
	p, err := parameters.EncodeValue(context.Background(), op, resolver, got, 4096)
	if err != nil || !reflect.DeepEqual(p.TypeMetadata, []byte{0, 0, 0, 160, 1, 'S', 1, 'T', 7}) || !reflect.DeepEqual(p.Data, []byte{2, 2, 0, 1, '7'}) {
		t.Fatal(p, err)
	}
}

func TestComplexMaterializationBudget(t *testing.T) {
	op, resolver, d := materializeTypeOperation(t, 0, []string{"A", "B", "C"}, []string{valueformat.TypeNameBlob, valueformat.TypeNameBlob, valueformat.TypeNameBlob})
	cfg := op.Config()
	cfg.Limits.MaxMessageBytes = 1024
	op.SetConfig(cfg)
	c := New(op, resolver)
	data := []byte{0}
	for range 3 {
		data = wire.AppendComplexLenBytes(data, inlineComplexLOB(strings.Repeat("x", 512)))
	}
	if _, err := c.complexValueLocked(context.Background(), wire.Column{Type: 160, ComplexSchema: "S", ComplexName: "T"}, data); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	decoded, _, err := wire.DecodeComplex(data, d.Shape, valueformat.MaxComplexNodes)
	if err != nil {
		t.Fatal(err)
	}
	budget := op.Config().Limits.MaxMessageBytes
	if _, err = c.decodeComplexValueLocked(context.Background(), d, decoded, 0, &budget); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("tree materialization exceeded its shared budget before JSON encoding", err)
	}
}
func inlineComplexLOB(text string) []byte {
	return append([]byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, text...)
}

func materializeTypeOperation(t *testing.T, version uint64, names, types []string) (*session.Operation, *catalog.Resolver, *valuecodec.Descriptor) {
	t.Helper()
	op := testpeer.DictionaryOperation(t, config.Config{}, func(ctx context.Context, p *wire.PacketIO) { testpeer.ServeComplexType(ctx, t, p, names, types) })
	resolver := new(catalog.Resolver)
	d, err := resolver.Resolve(t.Context(), op, valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "T", Version: version})
	if err != nil {
		t.Fatal(err)
	}
	return op, resolver, d
}
