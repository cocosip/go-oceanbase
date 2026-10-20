package valuecodec

import (
	"testing"

	"github.com/cocosip/go-oceanbase/config"
)

func TestSessionLiteralTypeSelection(t *testing.T) {
	for _, mode := range []struct {
		name   string
		mode   config.Mode
		status uint16
		quoted string
	}{
		{"mysql", config.ModeMySQL, 0, `'a\\b''c'`},
		{"mysql-no-backslash", config.ModeMySQL, 512, `'a\b''c'`},
		{"oracle", config.ModeOracle, 0, `'a\b''c'`},
	} {
		t.Run(mode.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, value string
				columnType  byte
				want        string
			}{
				{"integer", "00042", 8, "42"},
				{"decimal", "+0042", 246, "42"},
				{"signed-boundary", "-9223372036854775808", 8, "-9223372036854775808"},
				{"unsigned-boundary", maxUint64Literal, 246, maxUint64Literal},
				{"string-column", "00042", 253, "'00042'"},
				{"null-column", "42", 6, "'42'"},
				{"unknown-column", "42", 255, "'42'"},
				{"fraction", "1.5", 5, "'1.5'"},
				{"exponent", "1e3", 5, "'1e3'"},
				{"overflow", "18446744073709551616", 8, "'18446744073709551616'"},
				{"text", "a'b", 8, "'a''b'"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if got := SessionVariableLiteral(mode.mode, mode.status, tc.value, tc.columnType); got != tc.want {
						t.Fatalf("type=%d value=%q: got %q want %q", tc.columnType, tc.value, got, tc.want)
					}
				})
			}
			if got := SessionVariableLiteral(mode.mode, mode.status, `a\b'c`, 8); got != mode.quoted {
				t.Fatalf("session string escaping: got %q want %q", got, mode.quoted)
			}
		})
	}
}
