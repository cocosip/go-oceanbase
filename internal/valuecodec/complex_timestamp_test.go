package valuecodec

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexTimestampInput(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  []byte
	}{
		{"2026-11-03 01:02:03.123456789", []byte{20, 26, 11, 3, 1, 2, 3, 21, 205, 91, 7, 9}},
		{"2026-11-03 01:02:03.12", []byte{20, 26, 11, 3, 1, 2, 3, 0, 14, 39, 7, 2}},
		{time.Date(2026, 11, 3, 1, 2, 3, 123456789, time.FixedZone("local", 3600)), []byte{20, 26, 11, 3, 1, 2, 3, 21, 205, 91, 7, 9}},
	} {
		p, err := EncodeComplexScalar("timestamp", tc.value, config.SessionOptions{Location: time.UTC}, 1024, 13)
		if err != nil || !p.LengthEncoded || !bytes.Equal(p.Data, tc.want) || p.EncodedSize() != 13 {
			t.Fatalf("TIMESTAMP wall time or precision changed: %x %v", p.Data, err)
		}
		if _, err := EncodeComplexScalar(valueformat.TypeNameTimestamp, tc.value, config.SessionOptions{}, 1024, 12); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatal("TIMESTAMP framing escaped budget", err)
		}
	}
	for _, value := range []any{"2026-11-03 01:02:03.1234567890", "2026-11-03 01:02:03 +01:00", "2026-02-30 01:02:03", 42, time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC)} {
		if _, err := EncodeComplexScalar(valueformat.TypeNameTimestamp, value, config.SessionOptions{}, 1024, 1024); err == nil {
			t.Fatalf("invalid TIMESTAMP accepted: %v", value)
		}
	}
}

func TestComplexTemporalNationalHeaders(t *testing.T) {
	for _, tc := range []struct {
		typ  string
		code byte
	}{
		{valueformat.TypeNameTimestamp, 202},
		{valueformat.TypeNameNVarchar2, 207},
		{valueformat.TypeNameNChar, 208},
	} {
		if got := ComplexElementWireType(tc.typ); got != tc.code {
			t.Errorf("%s header=%d want=%d", tc.typ, got, tc.code)
		}
		d := Descriptor{Array: true, Fields: []Field{{Type: tc.typ}}}
		got, err := EncodeComplexMetadata(&d, config.SessionOptions{}, 1024, 4)
		if err != nil || !bytes.Equal(got, []byte{0, 0, 0, tc.code}) {
			t.Errorf("%s metadata=%x %v", tc.typ, got, err)
		}
	}
	if _, err := EncodeComplexScalar(valueformat.TypeNameNClob, "text", config.SessionOptions{}, 1024, 1024); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("NCLOB boundary changed", err)
	}
}
