package valuecodec

import (
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/types"
)

func TestScalarClientLiteralQuoting(t *testing.T) {
	options := config.SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}
	// A value that would end its literal early must either be rejected by the
	// type's validation or have every embedded quote doubled.
	for _, value := range []any{
		types.RowID("AAAA')||CHR(0)||('"),
		types.IntervalYM("1-2'||'"),
		types.IntervalDS("1 00:00:00'||'"),
		types.TimestampTZ("2026-01-02 03:04:05 +00:00'"),
		types.TimestampLTZ("2026-01-02 03:04:05 +00:00'"),
	} {
		text, err := ClientLiteral(value, config.ModeOracle, options, 1024, 1024)
		if err != nil {
			continue
		}
		if quotes := strings.Count(text, "'"); quotes%2 != 0 {
			t.Fatalf("%T produced an unbalanced literal: %s", value, text)
		}
	}
}

func TestScalarClientLiterals(t *testing.T) {
	options := config.SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC), "TIMESTAMP '2026-01-02 03:04:05.123456789'"},
		{types.NString("中😀'"), "UNISTR('\\4E2D\\D83D\\DE00\\0027')"},
		{types.Raw{0, 255}, "HEXTORAW('00ff')"}, {types.RowID("AAAA"), "CHARTOROWID('AAAA')"},
		{types.TimestampTZ("2026-01-02 03:04:05.123456789 -00:30"), "TIMESTAMP '2026-01-02 03:04:05.123456789 -00:30'"},
		{types.TimestampLTZ("2026-01-02 03:04:05.123456789 +00:00"), "CAST(TIMESTAMP '2026-01-02 03:04:05.123456789 +00:00' AS TIMESTAMP(9) WITH LOCAL TIME ZONE)"},
		{types.IntervalYM("-1-02"), "INTERVAL '-1-02' YEAR(9) TO MONTH"},
		{types.IntervalDS("1 02:03:04.123456789"), "INTERVAL '1 02:03:04.123456789' DAY(9) TO SECOND(9)"},
	} {
		text, err := ClientLiteral(tc.value, config.ModeOracle, options, 1024, 1024)
		if err != nil || text != tc.want {
			t.Fatal(tc.value, text, err)
		}
		if _, err = ClientLiteral(tc.value, config.ModeOracle, options, 1024, 2); err == nil {
			t.Fatal("expanded literal escaped limit", tc.value)
		}
	}
}

func TestGeneratedLiteralTokens(t *testing.T) {
	const variable = "@_obgo_0123456789abcdef0123456789abcdef_0"
	for _, mode := range []struct {
		name                       string
		mode                       config.Mode
		variableData, questionData string
	}{
		{"mysql", config.ModeMySQL, "_utf8mb4 X'405f6f62676f5f30313233343536373839616263646566303132333435363738396162636465665f30'", "_utf8mb4 X'3f'"},
		{"oracle", config.ModeOracle, "'" + variable + "'", "'?'"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			options := config.SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}
			for _, tc := range []struct {
				name  string
				value any
				want  string
			}{
				{"generated-variable", OutputVariable(variable), variable},
				{"ordinary-variable-text", variable, mode.variableData},
				{"generated-placeholder", LongPlaceholder{}, "?"},
				{"ordinary-placeholder-text", "?", mode.questionData},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := ClientLiteral(tc.value, mode.mode, options, 1024, 1024)
					if err != nil || got != tc.want {
						t.Fatalf("%T: got %q want %q: %v", tc.value, got, tc.want, err)
					}
				})
			}
		})
	}
}
