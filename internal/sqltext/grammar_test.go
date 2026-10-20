package sqltext

import (
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"testing"
)

func TestBatchValuesShape(t *testing.T) {
	for _, q := range []string{"INSERT INTO t VALUES (?)", "INSERT INTO `db`.`t` (`a`, b) VALUES (?,?)"} {
		if _, _, ok := BatchValuesPlan(q, false); !ok {
			t.Fatal(q)
		}
	}
	for _, q := range []string{"INSERT IGNORE INTO t VALUES (?)", "INSERT INTO t VALUES (?),(?)", "INSERT INTO t VALUES (?) ON DUPLICATE KEY UPDATE a=1", "INSERT INTO t SELECT ?", "INSERT INTO t VALUES (f(?))", "INSERT INTO t VALUES (?); DELETE FROM t", "INSERT INTO t VALUES (?) RETURNING a"} {
		if _, _, ok := BatchValuesPlan(q, false); ok {
			t.Fatal(q)
		}
	}
}

func TestBatchSQLGate(t *testing.T) {
	for _, q := range []string{"SELECT ?", "CALL p(?)", "UPDATE t SET v=?; DELETE FROM t", "INSERT INTO t VALUES (?) RETURNING id INTO ?", "SET x=?", "INSERT /*! FROM */ INTO t VALUES (?)"} {
		if BatchDML(q, config.ModeMySQL, false) {
			t.Fatal("unsafe pipeline SQL", q)
		}
	}
	for _, q := range []string{"INSERT INTO t VALUES (?)", "UPDATE t SET v=';' WHERE id=?", "/* ordinary */ DELETE FROM t WHERE id=?"} {
		if !BatchDML(q, config.ModeMySQL, false) {
			t.Fatal("ordinary DML rejected", q)
		}
	}
}

func TestSensitiveCursorQuery(t *testing.T) {
	for _, tc := range []struct{ query, want string }{
		{"SELECT id,v FROM t WHERE id=:id ORDER BY id", "SELECT id,v, ROWID FROM t WHERE id=:id ORDER BY id"},
		{"SELECT * FROM s.t x WHERE id=?", "SELECT x.*, ROWID FROM s.t x WHERE id=?"},
		{`SELECT * FROM "S"."T"`, `SELECT "T".*, ROWID FROM "S"."T"`},
		{"/* lead */ SELECT t.* FROM t", "/* lead */ SELECT t.*, ROWID FROM t"},
		{"SELECT /*+ FULL(t) */ id,v FROM t ORDER BY 2 DESC,1", "SELECT /*+ FULL(t) */ id,v, ROWID FROM t ORDER BY 2 DESC,1"},
	} {
		got, err := SensitiveCursorQuery(tc.query)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q, %v", tc.query, got, err)
		}
	}
	for _, query := range []string{"SELECT DISTINCT id FROM t", "SELECT COUNT(*) FROM t", "SELECT t.id FROM t JOIN u ON t.id=u.id", "SELECT id FROM t; DELETE FROM t"} {
		if _, err := SensitiveCursorQuery(query); !errors.Is(err, dberrors.ErrCursor) {
			t.Fatalf("accepted non-refreshable query %q: %v", query, err)
		}
	}
}

func TestMySQLImportStatementSyntax(t *testing.T) {
	for _, tc := range []struct {
		query              string
		noBackslash, valid bool
	}{
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t", false, true},
		{"/* import */ " + "LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + " ; -- done\n", false, true},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + " FIELDS TERMINATED BY ';'", false, true},
		{`LOAD DATA LOCAL INFILE 'input' INTO TABLE ` + "`t;name`", false, true},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + ` FIELDS TERMINATED BY '\\'`, false, true},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + ` FIELDS TERMINATED BY '\'`, true, true},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + ";;", false, false},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + "; SET autocommit=0", false, false},
		{`LOAD DATA LOCAL INFILE 'input' INTO TABLE "t\"; START TRANSACTION; -- "`, false, false},
		{"LOAD DATA LOCAL INFILE 'input' INTO TABLE t" + " /* unterminated", false, false},
		{"SELECT 'LOAD DATA'", false, false},
	} {
		err := LocalImportStatement(tc.query, config.ModeMySQL, tc.noBackslash)
		if (err == nil) != tc.valid || err != nil && !errors.Is(err, dberrors.ErrUnsupported) {
			t.Errorf("query=%q noBackslash=%v: %v", tc.query, tc.noBackslash, err)
		}
	}
}

func TestCursorUpdateShape(t *testing.T) {
	for _, query := range []string{"SELECT t.id FROM t JOIN u ON t.id=u.id", "SELECT COUNT(*) FROM t", "SELECT id FROM t,u", "SELECT DISTINCT id FROM t", "SELECT id FROM t UNION SELECT id FROM t", "SELECT id FROM (SELECT id FROM t) x"} {
		if err := CursorUpdatableQuery(query, config.ModeMySQL, false); !errors.Is(err, dberrors.ErrCursor) {
			t.Fatal(query, err)
		}
	}
}
