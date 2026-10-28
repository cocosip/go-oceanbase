package statement

import (
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCacheableSQLQuoteModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   config.Mode
		status uint16
		query  string
		want   bool
	}{
		{"ansi separators", config.ModeMySQL, 0, `SELECT 1 AS "a\"; USE db2; SELECT 1 AS "b" -- "`, false},
		{"no backslash separators", config.ModeMySQL, 512, `SELECT 1 AS "a\"; USE db2; SELECT 1 AS "b" -- "`, false},
		{"quoted separator", config.ModeMySQL, 0, `SELECT "a;b"`, true},
		{"both quote modes safe", config.ModeMySQL, 0, `SELECT 'a\b' AS "v"`, true},
		{"ambiguous quote", config.ModeMySQL, 0, `SELECT "a\"b"`, false},
		{"oracle quotes", config.ModeOracle, 0, `SELECT 'a\b' AS "v" FROM dual`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := testOperation(t, config.Config{}, tc.mode, "")
			op.SetOK(wire.OK{Status: tc.status})
			if got := CacheableSQL(op, tc.query); got != tc.want {
				t.Fatalf("CacheableSQL(%q)=%v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
