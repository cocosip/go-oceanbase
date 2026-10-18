package oceanbase

import (
	"errors"
	"testing"
)

func TestJDBCEscapes(t *testing.T) {
	for _, tc := range []struct {
		mode        Mode
		query, want string
	}{
		{ModeMySQL, `SELECT {d '2026-01-02'}, {ts '2026-01-02 03:04:05.123456'}`, `SELECT '2026-01-02', '2026-01-02 03:04:05.123456'`},
		{ModeOracle, `SELECT {d '2026-01-02'}, {ts '2026-01-02 03:04:05.123456'} FROM dual`, `SELECT DATE '2026-01-02', TIMESTAMP '2026-01-02 03:04:05.123456' FROM dual`},
		{ModeMySQL, `SELECT {fn concat({fn ucase('a')},'?')}`, `SELECT concat(UPPER('a'),'?')`},
		{ModeOracle, `SELECT {fn ifnull(:x,0)} FROM dual`, `SELECT NVL(:x,0) FROM dual`},
		{ModeMySQL, `{call p(?)}`, `CALL p(?)`},
		{ModeOracle, `{call p(:x)}`, `BEGIN p(:x); END;`},
		{ModeOracle, `{? = call f(?)}`, `BEGIN ? := f(?); END;`},
		{ModeMySQL, `SELECT '{fn now()}' /* {d 'wrong'} */`, `SELECT '{fn now()}' /* {d 'wrong'} */`},
		{ModeOracle, `SELECT q'[{bad}]' FROM dual`, `SELECT q'[{bad}]' FROM dual`},
		{ModeMySQL, `SELECT {fn convert(?, SQL_INTEGER)}`, `SELECT CAST(? AS SIGNED INTEGER)`},
		{ModeOracle, `SELECT {fn convert(:x, SQL_INTEGER)} FROM dual`, `SELECT CAST(:x AS NUMBER(10)) FROM dual`},
		{ModeOracle, "SELECT {fn convert(:x -- operand\n, SQL_DOUBLE)} FROM dual", "SELECT CAST(:x -- operand\n AS BINARY_DOUBLE) FROM dual"},
		{ModeMySQL, `SELECT {fn timestampadd(SQL_TSI_DAY,1,?)}`, `SELECT timestampadd(DAY,1,?)`},
		{ModeMySQL, `SELECT {fn convert(1.25, SQL_REAL)}`, `SELECT (0e0+(1.25))`},
		{ModeMySQL, `SELECT {fn convert(1.25, SQL_DOUBLE)}`, `SELECT (0e0+(1.25))`},
	} {
		got, err := RewriteJDBCEscapes(tc.query, tc.mode)
		if err != nil || got != tc.want {
			t.Fatalf("%q -> %q, want %q: %v", tc.query, got, tc.want, err)
		}
	}
	for _, tc := range []struct {
		mode  Mode
		query string
	}{
		{ModeAuto, `SELECT {d '2026-01-02'}`},
		{ModeMySQL, `SELECT {d '2026-99-02'}`},
		{ModeMySQL, `SELECT {fn now()`},
		{ModeOracle, `SELECT {unknown :x}`},
		{ModeMySQL, `{? = call f(?)}`},
		{ModeMySQL, `SELECT "a\" {fn now()} "b"`},
	} {
		if _, err := RewriteJDBCEscapes(tc.query, tc.mode); err == nil {
			t.Fatal("invalid escape accepted", tc.query)
		}
	}
}

func TestJDBCConvertOperandBoundaries(t *testing.T) {
	for _, tc := range []struct {
		query, want string
	}{
		{`SELECT {fn convert(2 OR 0, SQL_BOOLEAN)}`, `SELECT (1=(2 OR 0))`},
		{`SELECT {fn convert(1 XOR 1, SQL_BOOLEAN)}`, `SELECT (1=(1 XOR 1))`},
		{`SELECT {fn convert(NULL IS NULL, SQL_BOOLEAN)}`, `SELECT (1=(NULL IS NULL))`},
		{`SELECT {fn convert(1 | 2, SQL_DOUBLE)}`, `SELECT (0e0+(1 | 2))`},
		{`SELECT {fn convert(? & 3, SQL_FLOAT)}`, `SELECT (0e0+(? & 3))`},
		{`SELECT {fn convert(1 <=> NULL, SQL_REAL)}`, `SELECT (0e0+(1 <=> NULL))`},
		{`SELECT {fn convert(COALESCE(?, 2) OR 0, SQL_BOOLEAN)}`, `SELECT (1=(COALESCE(?, 2) OR 0))`},
		{`SELECT {fn convert({fn convert(2 OR 0, SQL_BOOLEAN)}, SQL_DOUBLE)}`, `SELECT (0e0+((1=(2 OR 0))))`},
		{`SELECT {fn convert(NULL, SQL_DOUBLE)}, {fn convert(NULL, SQL_BOOLEAN)}`, `SELECT (0e0+(NULL)), (1=(NULL))`},
		{"SELECT {fn convert(2 OR 0 -- operand\n, SQL_BOOLEAN)}", "SELECT (1=(2 OR 0 -- operand\n))"},
		{"SELECT {fn convert(1 | 2 -- operand\n, SQL_DOUBLE)}", "SELECT (0e0+(1 | 2 -- operand\n))"},
		{"SELECT {fn convert(? -- operand\n, SQL_INTEGER)}", "SELECT CAST(? -- operand\n AS SIGNED INTEGER)"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := RewriteJDBCEscapes(tc.query, ModeMySQL)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, want %q: %v", got, tc.want, err)
			}
			before, err := buildBindPlan(tc.query, ModeMySQL)
			if err != nil {
				t.Fatal(err)
			}
			after, err := buildBindPlan(got, ModeMySQL)
			if err != nil || after.numInput != before.numInput {
				t.Fatal("conversion changed parameter count", err)
			}
		})
	}
}

func TestJDBCConvertMissingOperand(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, typ := range []string{"BOOLEAN", "FLOAT", "DOUBLE", "REAL", "INTEGER"} {
			for _, operand := range []string{" ", "/* missing */", "-- missing\n", "\u3000", "\t\u00a0", "\u3000/* missing */", "/* missing */\u3000", "-- missing\n\u3000"} {
				query := "SELECT {fn convert(" + operand + ", SQL_" + typ + ")}"
				if got, err := RewriteJDBCEscapes(query, mode); !errors.Is(err, ErrBind) || got != "" {
					t.Errorf("mode %v accepted missing operand: %q: %v", mode, got, err)
				}
			}
		}
	}
}
