package statement

import (
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCacheableSQLQuoteModes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   config.CompatibilityMode
		status uint16
		query  string
		want   bool
	}{
		{"ansi separators", config.CompatibilityModeMySQL, 0, `SELECT 1 AS "a\"; USE db2; SELECT 1 AS "b" -- "`, false},
		{"no backslash separators", config.CompatibilityModeMySQL, 512, `SELECT 1 AS "a\"; USE db2; SELECT 1 AS "b" -- "`, false},
		{"quoted separator", config.CompatibilityModeMySQL, 0, `SELECT "a;b"`, true},
		{"both quote modes safe", config.CompatibilityModeMySQL, 0, `SELECT 'a\b' AS "v"`, true},
		{"ambiguous quote", config.CompatibilityModeMySQL, 0, `SELECT "a\"b"`, false},
		{"oracle quotes", config.CompatibilityModeOracle, 0, `SELECT 'a\b' AS "v" FROM dual`, true},
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

func TestSessionNeutralSQL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mode   config.CompatibilityMode
		status uint16
		query  string
		want   bool
	}{
		{"show", config.CompatibilityModeMySQL, 0, "SHOW VARIABLES LIKE 'sql_mode'", true},
		{"explain", config.CompatibilityModeMySQL, 0, "EXPLAIN SELECT 1", true},
		{"describe", config.CompatibilityModeMySQL, 0, "DESCRIBE t", true},
		{"desc", config.CompatibilityModeMySQL, 0, "DESC t", true},
		{"set mutates", config.CompatibilityModeMySQL, 0, "SET SESSION sql_mode = 'ANSI_QUOTES'", false},
		{"ddl mutates", config.CompatibilityModeMySQL, 0, "TRUNCATE t", false},
		{"call mutates", config.CompatibilityModeMySQL, 0, "CALL p()", false},
		{"with clears", config.CompatibilityModeMySQL, 0, "WITH c AS (SELECT 1) SELECT * FROM c", false},
		{"quoted separator", config.CompatibilityModeMySQL, 0, `SHOW VARIABLES LIKE "a\"; SET x=1; -- "`, false},
		{"multi statement", config.CompatibilityModeMySQL, 0, "SHOW A; SHOW B", false},
		{"ambiguous quote", config.CompatibilityModeMySQL, 0, `SHOW "a\"b"`, false},
		{"empty", config.CompatibilityModeMySQL, 0, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			op := testOperation(t, config.Config{}, tc.mode, "")
			op.SetOK(wire.OK{Status: tc.status})
			if got := SessionNeutralSQL(op, tc.query); got != tc.want {
				t.Fatalf("SessionNeutralSQL(%q)=%v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
