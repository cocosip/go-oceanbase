package valuecodec

import (
	"bytes"
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
	"reflect"
	"testing"
	"time"
)

func TestScalarBindings(t *testing.T) {
	for _, tc := range []struct {
		value any
		mode  config.Mode
		typ   uint16
		data  []byte
	}{
		{types.Number("1.234567890123456789e2"), config.ModeOracle, 246, []byte("123.4567890123456789")},
		{types.Number("-1e-3"), config.ModeMySQL, 246, []byte("-0.001")},
		{types.Raw{0, 255}, config.ModeOracle, 203, []byte{0, 255}},
		{types.RowID("AAAABBBB"), config.ModeOracle, 253, []byte("AAAABBBB")},
		{types.NString("中文"), config.ModeOracle, 208, []byte{0x4e, 0x2d, 0x65, 0x87}},
	} {
		options := config.SessionOptions{Charset: charsetUTF8MB4, NCharset: charsetUTF16, Location: time.UTC}
		p, err := EncodeParameter(tc.value, tc.mode, options, 1024)
		if err != nil || p.Type != tc.typ || !p.LengthEncoded || !reflect.DeepEqual(p.Data, tc.data) {
			t.Fatal(tc.value, p, err)
		}
	}
	for _, value := range []any{types.Raw{}, types.NString("x"), types.RowID("AAA")} {
		if _, err := EncodeParameter(value, config.ModeMySQL, config.SessionOptions{}, 1024); !errors.Is(err, dberrors.ErrUnsupported) {
			t.Fatal(value, err)
		}
	}
	if text, err := ClientLiteral(types.Number("1e-20"), config.ModeMySQL, config.SessionOptions{Charset: charsetUTF8MB4}, 1024, 1024); err != nil || text != "0.00000000000000000001" {
		t.Fatal(text, err)
	}
	if _, err := ClientLiteral(types.Number("1e2147483647"), config.ModeMySQL, config.SessionOptions{Charset: charsetUTF8MB4}, 1024, 1024); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("unbounded numeric expansion", err)
	}
}

func TestScalarBinaryNullEmpty(t *testing.T) {
	var raw types.Raw
	if err := raw.Scan([]byte{}); err != nil || raw == nil {
		t.Fatal(raw, err)
	}
	source := []byte{0, 255}
	if err := raw.Scan(source); err != nil {
		t.Fatal(err)
	}
	source[0] = 1
	if !bytes.Equal(raw, []byte{0, 255}) {
		t.Fatal("RAW aliases row buffer")
	}
}

func TestTypeCharsetBoundaries(t *testing.T) {
	for _, binaryRows := range []bool{false, true} {
		mode, options := config.ModeOracle, config.SessionOptions{Charset: charsetLatin1, NCharset: charsetUTF16}
		v, err := DecodeRowValue(wire.Column{Type: 208, Charset: 63}, binaryRows, []byte{0x4e, 0x2d}, mode, options)
		if err != nil || v != "中" {
			t.Fatal("NCHAR treated as binary or connection charset", v, err)
		}
		v, err = DecodeRowValue(wire.Column{Type: 209, Charset: 63}, binaryRows, []byte("AAAA"), mode, options)
		if err != nil || v != "AAAA" {
			t.Fatal("ROWID treated as binary", v, err)
		}
		v, err = DecodeRowValue(wire.Column{Type: 253, Charset: 28}, binaryRows, []byte{0xd6, 0xd0}, mode, options)
		if err != nil || v != "中" {
			t.Fatal(v, err)
		}
	}
	mode, options := config.ModeMySQL, config.SessionOptions{ParseTime: true, Location: time.UTC}
	v, err := DecodeRowValue(wire.Column{Type: 10}, false, []byte("2024-02-29"), mode, options)
	if err != nil || v != time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC) {
		t.Fatal(v, err)
	}
	if _, err = DecodeRowValue(wire.Column{Type: 10}, false, []byte("0000-00-00"), mode, options); !errors.Is(err, dberrors.ErrType) {
		t.Fatal("zero date fabricated as time.Time", err)
	}
	v, err = DecodeRowValue(wire.Column{Type: 11}, false, []byte("-838:59:59.999999"), mode, options)
	if err != nil || v != "-838:59:59.999999" {
		t.Fatal("TIME lost duration semantics", v, err)
	}
}

func TestParityCharsetColumnLength(t *testing.T) {
	for _, tc := range []struct {
		collation uint16
		width     uint32
	}{{152, 2}, {153, 2}, {216, 4}, {217, 4}, {222, 4}} {
		got, known := ColumnLength(wire.Column{Type: 253, Charset: tc.collation, Length: 10 * tc.width}, config.ModeMySQL)
		if !known || got != 10 {
			t.Fatalf("collation %d: %d %v", tc.collation, got, known)
		}
		if _, known = ColumnLength(wire.Column{Type: 253, Charset: tc.collation, Length: 10*tc.width + 1}, config.ModeMySQL); known {
			t.Fatal("nonintegral character length")
		}
	}
}

func TestNewMetadataCharacterUnits(t *testing.T) {
	column, err := DecodeColumn(testpeer.TextColumn("n", 207, 54, 0), config.SessionOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	column.Length = 9
	n, known := ColumnLength(column, config.ModeOracle)
	if !known || n != 9 {
		t.Fatal(n, known)
	}
}

func TestNewMetadataLengths(t *testing.T) {
	for _, tc := range []struct {
		typ       byte
		length    uint32
		precision byte
		want      int64
		known     bool
	}{
		{203, 99, 17, 17, true}, // RAW length comes from precision in new metadata.
		{209, 0, 18, 18, true},
		{210, 100, 0, 0, false},
		{211, 100, 0, 0, false},
		{207, 12, 0, 12, true},
		{208, 12, 0, 12, true},
	} {
		got, known := ColumnLength(wire.Column{Type: tc.typ, Length: tc.length, Precision: tc.precision, NewMetadata: true, Charset: 54}, config.ModeOracle)
		if got != tc.want || known != tc.known {
			t.Fatalf("type %d: length=%d known=%v", tc.typ, got, known)
		}
	}
}
