package valuecodec

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestComplexScalarEncodedBudget(t *testing.T) {
	for _, tc := range []struct {
		typ     string
		value   any
		options config.SessionOptions
		want    []byte
	}{
		{valueformat.TypeNameNumber, types.Number("1e0000000"), config.SessionOptions{}, []byte("1")},
		{valueformat.TypeNameRaw, types.Raw{0, 255}, config.SessionOptions{}, []byte{0, 255}},
		{valueformat.TypeNameVarchar2, "测测", config.SessionOptions{Charset: charsetGBK}, []byte{0xb2, 0xe2, 0xb2, 0xe2}},
		{valueformat.TypeNameNVarchar2, "测测", config.SessionOptions{Charset: charsetGBK, NCharset: charsetUTF16}, []byte{0x6d, 0x4b, 0x6d, 0x4b}},
	} {
		limit := int64(len(tc.want) + 1)
		p, err := EncodeComplexScalar(tc.typ, tc.value, tc.options, 1024, limit)
		if err != nil || !p.LengthEncoded || !bytes.Equal(p.Data, tc.want) || p.EncodedSize() != limit {
			t.Fatal("fitting complex scalar changed", tc.typ, p, err)
		}
		if p, err = EncodeComplexScalar(tc.typ, tc.value, tc.options, 1024, limit-1); !errors.Is(err, wire.ErrMessageTooLarge) || len(p.Data) != 0 {
			t.Fatal("length prefix escaped complex field budget", tc.typ, p, err)
		}
	}
	if _, err := EncodeComplexScalar(valueformat.TypeNameRaw, types.Raw{1, 2}, config.SessionOptions{}, 1, 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("RAW escaped LOB limit", err)
	}
}

func TestComplexMetadataBudget(t *testing.T) {
	for _, tc := range []struct {
		d    Descriptor
		want []byte
	}{
		{Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "T", Version: 7}}, []byte{1, 'S', 1, 'T', 7}},
		{Descriptor{Key: Key{Version: 8}, Array: true, Fields: []Field{{Type: valueformat.TypeNameNumber}}}, []byte{0, 0, 8, 0}},
		{Descriptor{Key: Key{Version: 8}, Array: true, Fields: []Field{{Child: &Descriptor{Key: Key{Schema: "S", Name: "T", Version: 9}}}}}, []byte{0, 0, 8, 160, 1, 'S', 1, 'T', 9}},
	} {
		limit := int64(len(tc.want))
		got, err := EncodeComplexMetadata(&tc.d, config.SessionOptions{}, 1024, limit)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Fatal("complex metadata changed", got, err)
		}
		if _, err = EncodeComplexMetadata(&tc.d, config.SessionOptions{}, 1024, limit-1); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("metadata escaped parameter budget", err)
		}
	}
}

func TestComplexScalarDecode(t *testing.T) {
	value, err := DecodeComplexScalar(valueformat.TypeNameNumber, []byte("1.20"), config.SessionOptions{})
	if err != nil || value != types.Number("1.20") {
		t.Fatal("exact number identity changed", value, err)
	}
	if _, err = DecodeComplexScalar(valueformat.TypeNameNumber, []byte("1;select"), config.SessionOptions{}); !errors.Is(err, dberrors.ErrType) {
		t.Fatal("malformed numeric field accepted", err)
	}
	data := []byte{0, 255}
	value, err = DecodeComplexScalar(valueformat.TypeNameRaw, data, config.SessionOptions{})
	data[0] = 1
	if err != nil || !bytes.Equal(value.(types.Raw), []byte{0, 255}) {
		t.Fatal("RAW aliases response data", value, err)
	}
}
