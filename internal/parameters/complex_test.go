package parameters

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
	"testing"
)

const parameterBudgetText = "测测"

func TestComplexEncoderNestedNullBudget(t *testing.T) {
	child := &valuecodec.Descriptor{Key: valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "CHILD", Version: 0}, Fields: []valuecodec.Field{{Name: "A", Type: valueformat.TypeNameNumber}, {Name: "B", Type: valueformat.TypeNameNumber}}}
	childValue := types.Object{Schema: "S", Type: "CHILD", Fields: []types.Attribute{{Name: "A"}, {Name: "B"}}}
	for _, tc := range []struct {
		name  string
		d     *valuecodec.Descriptor
		value any
	}{
		{"object", &valuecodec.Descriptor{Key: valuecodec.Key{Mode: config.ModeOracle, Schema: "S", Name: "PARENT", Version: 0}, Fields: []valuecodec.Field{{Name: "C", Child: child}, {Name: "N", Type: valueformat.TypeNameNumber}}}, types.Object{Type: "PARENT", Fields: []types.Attribute{{Name: "C", Value: childValue}, {Name: "N"}}}},
		{"array", &valuecodec.Descriptor{Array: true, Fields: []valuecodec.Field{{Child: child}}}, types.Array{Elements: []any{childValue, nil}}},
	} {
		for _, limit := range []int{4, 5} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, limit), func(t *testing.T) {
				e := complexEncoder{nodes: limit, limit: 1024}
				err := e.value(tc.d, tc.value, 0, false)
				if limit == 4 {
					if !errors.Is(err, dberrors.ErrComplex) || e.nodes < 0 {
						t.Fatal("encoder accepted a trailing NULL beyond the shared budget", e.nodes, err)
					}
				} else if err != nil || e.nodes != 0 {
					t.Fatal("encoder rejected exact shared budget", e.nodes, err)
				}
			})
		}
	}
}

func TestComplexEncoderNullDepth(t *testing.T) {
	d := &valuecodec.Descriptor{Fields: []valuecodec.Field{{Name: "N", Type: valueformat.TypeNameNumber}}}
	for _, value := range []any{nil, types.Object{Null: true}, types.Array{Null: true}} {
		for _, depth := range []int{valueformat.MaxComplexDepth - 1, valueformat.MaxComplexDepth} {
			e := complexEncoder{nodes: 2, limit: 1024}
			err := e.value(d, types.Object{Fields: []types.Attribute{{Name: "N", Value: value}}}, depth, false)
			if depth == valueformat.MaxComplexDepth {
				if !errors.Is(err, dberrors.ErrComplex) {
					t.Fatal("encoder accepted NULL beyond maximum depth", value, err)
				}
			} else if err != nil || e.nodes != 0 {
				t.Fatal("encoder rejected NULL at exact depth limit", value, e.nodes, err)
			}
		}
	}
}

func TestComplexNumberNormalizedBudget(t *testing.T) {
	for _, limit := range []int64{3, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			e := complexEncoder{nodes: valueformat.MaxComplexNodes, limit: limit, data: []byte{42}}
			err := e.scalar(valueformat.TypeNameNumber, types.Number("1e0000000"), 0)
			if limit == 2 {
				if !errors.Is(err, wire.ErrMessageTooLarge) || !bytes.Equal(e.data, []byte{42}) {
					t.Fatal("number field exceeded cumulative prefix budget", err)
				}
			} else if err != nil || !bytes.Equal(e.data, []byte{42, 1, '1'}) {
				t.Fatal("fitting number field rejected or changed", e.data, err)
			}
		})
	}
}

func TestComplexTextEncodedBudget(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameVarchar2, valueformat.TypeNameNVarchar2} {
		for _, charset := range []string{testCharsetGBK, testCharsetUTF16} {
			if typ == valueformat.TypeNameVarchar2 && charset == testCharsetUTF16 {
				continue
			}
			for _, limit := range []int64{6, 5} {
				t.Run(fmt.Sprintf("%s/%s/%d", typ, charset, limit), func(t *testing.T) {
					options := config.SessionOptions{Charset: testCharsetGBK, NCharset: charset}
					e := complexEncoder{options: options, nodes: valueformat.MaxComplexNodes, limit: limit, data: []byte{42}}
					err := e.scalar(typ, parameterBudgetText, 0)
					if limit == 5 {
						if !errors.Is(err, wire.ErrMessageTooLarge) || !bytes.Equal(e.data, []byte{42}) {
							t.Fatal("complex field escaped its cumulative budget", err)
						}
						return
					}
					want := []byte{42, 4, 0xb2, 0xe2, 0xb2, 0xe2}
					if charset == testCharsetUTF16 {
						want = []byte{42, 4, 0x6d, 0x4b, 0x6d, 0x4b}
					}
					if err != nil || !bytes.Equal(e.data, want) {
						t.Fatal("complex field rejected a fitting encoded value", err)
					}
				})
			}
		}
	}
}

const testCharsetUTF16 = "utf16"
const testCharsetGBK = "gbk"
