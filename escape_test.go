package oceanbase

import "testing"

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
		{ModeMySQL, `SELECT {fn timestampadd(SQL_TSI_DAY,1,?)}`, `SELECT timestampadd(DAY,1,?)`},
		{ModeMySQL, `SELECT {fn convert(1.25, SQL_REAL)}`, `SELECT (0e0+1.25)`},
		{ModeMySQL, `SELECT {fn convert(1.25, SQL_DOUBLE)}`, `SELECT (0e0+1.25)`},
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
