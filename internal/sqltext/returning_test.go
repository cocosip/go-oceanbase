package sqltext

import "testing"

func TestReturningBindCount(t *testing.T) {
	for _, tc := range []struct {
		query string
		count int
	}{
		{"INSERT INTO t(v) VALUES (?) RETURNING id INTO ?", 1},
		{"update t set v=? returning id,v into ?,?; -- tail", 2},
		{"DELETE FROM t RETURNING id INTO ?", 1},
		{"INSERT INTO t VALUES ('RETURNING x INTO ?')", 0},
		{"UPDATE t SET v=(SELECT 'RETURNING' FROM dual) /* RETURNING */ RETURNING f(v, '?') INTO ?", 1},
		{"BEGIN INSERT INTO t VALUES (?) RETURNING id INTO ?; END;", 0},
		{"SELECT ? FROM dual", 0},
		{"INSERT INTO t(v) VALUES (?)", 0},
		{"INSERT INTO t VALUES (?) RETURNING id INTO ?,", 0},
		{"INSERT INTO t VALUES (?) RETURNING id INTO ?, 1", 0},
		{"INSERT INTO t VALUES (?) RETURNING id INTO ?; DELETE FROM t", 0},
		{"INSERT INTO t VALUES (?); DELETE FROM t RETURNING id INTO ?", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if got := ReturningBindCount(tc.query); got != tc.count {
				t.Fatalf("count=%d want=%d", got, tc.count)
			}
		})
	}
}
