package valuecodec

import (
	"bytes"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func TestComplexLOBContent(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		value any
		data  []byte
	}{
		{valueformat.TypeNameBlob, types.Raw{0, 128, 255}, []byte{0, 128, 255}},
		{valueformat.TypeNameBlob, []byte{}, []byte{}},
		{valueformat.TypeNameClob, "患者😀", []byte("患者😀")},
		{valueformat.TypeNameClob, "", []byte{}},
	} {
		p, err := EncodeComplexScalar(tc.typ, tc.value, config.SessionOptions{Charset: charsetLatin1}, int64(len(tc.data)), int64(len(tc.data)+1))
		if err != nil || !p.LengthEncoded || !bytes.Equal(p.Data, tc.data) {
			t.Fatal(p, err)
		}
		if _, err := EncodeComplexScalar(tc.typ, tc.value, config.SessionOptions{}, 1024, int64(len(tc.data))); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("framing budget", err)
		}
		if len(tc.data) > 0 {
			if _, err := EncodeComplexScalar(tc.typ, tc.value, config.SessionOptions{}, int64(len(tc.data)-1), 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
				t.Fatal("LOB budget", err)
			}
		}
	}
	for _, tc := range []struct {
		typ   string
		value any
	}{{valueformat.TypeNameBlob, "bytes only"}, {valueformat.TypeNameClob, []byte("text only")}, {valueformat.TypeNameNClob, "not native"}, {valueformat.TypeNameBlob, new(int)}} {
		if _, err := EncodeComplexScalar(tc.typ, tc.value, config.SessionOptions{}, 1024, 1024); err == nil {
			t.Fatal("unsupported LOB kind accepted", tc)
		}
	}
	if _, err := EncodeComplexScalar(valueformat.TypeNameClob, string([]byte{255}), config.SessionOptions{}, 1024, 1024); !errors.Is(err, charset.ErrEncoding) {
		t.Fatal(err)
	}
}

func TestNamedLOBCollectionMetadata(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameBlob, valueformat.TypeNameClob} {
		d := Descriptor{Key: Key{Mode: config.ModeOracle, Schema: "S", Name: "TA", Version: 7}, Array: true, Fields: []Field{{Type: typ}}}
		want := []byte{1, 'S', 2, 'T', 'A', 7}
		got, err := EncodeComplexMetadata(&d, config.SessionOptions{}, 1024, int64(len(want)))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("named %s collection lost identity: %x %v", typ, got, err)
		}
		if _, err := EncodeComplexMetadata(&d, config.SessionOptions{}, 1024, int64(len(want)-1)); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal(err)
		}
		d.Key.Name = ""
		if _, err := EncodeComplexMetadata(&d, config.SessionOptions{}, 1024, 1024); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal("anonymous LOB header enabled", err)
		}
	}
}

func TestLargeComplexLOBContent(t *testing.T) {
	for _, typ := range []string{valueformat.TypeNameBlob, valueformat.TypeNameClob} {
		data := bytes.Repeat([]byte{0, 128, 255}, 30003)
		var value any = data
		if typ == valueformat.TypeNameClob {
			data = bytes.Repeat([]byte("中"), 30003)
			value = string(data)
		}
		p, err := EncodeComplexScalar(typ, value, config.SessionOptions{Charset: charsetLatin1}, 90009, 90013)
		if err != nil || !bytes.Equal(p.Data, data) || p.EncodedSize() != 90013 {
			t.Fatal(len(p.Data), err)
		}
		if _, err := EncodeComplexScalar(typ, value, config.SessionOptions{}, 90009, 90012); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("large field escaped prefix budget", err)
		}
	}
}
