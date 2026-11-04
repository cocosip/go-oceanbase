package valuecodec

import (
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOracleNULLiteral(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"a\x00b", "REPLACE('a01b','01',CHR(0))"},
		{"a01b\x00c", "REPLACE('a01b001c','001',CHR(0))"},
		{"001\x00", "REPLACE('0010001','0001',CHR(0))"},
		{"\x00\x00", "REPLACE('0101','01',CHR(0))"},
		{"000\x001", "REPLACE('000011','01',CHR(0))"},
		{"'\x00'", "REPLACE('''01''','01',CHR(0))"},
	} {
		got, err := quoteClientString(tc.text, len(tc.text), int64(len(tc.want)))
		if err != nil || got != tc.want {
			t.Fatalf("NUL literal %q: %q %v", tc.text, got, err)
		}
		if _, err := quoteClientString(tc.text, len(tc.text), int64(len(tc.want)-1)); !errors.Is(err, wire.ErrMessageTooLarge) {
			t.Fatalf("NUL budget missed: %v", err)
		}
	}
}
