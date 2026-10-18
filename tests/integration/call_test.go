//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestScalarCall(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			for _, prepare := range []oceanbase.PrepareMode{oceanbase.PrepareServer, oceanbase.PrepareClient} {
				t.Run(fmt.Sprint(prepare), func(t *testing.T) { testScalarCallProfile(t, p, prepare) })
			}
		})
	}
}

func testScalarCallProfile(t *testing.T, p profile, prepare oceanbase.PrepareMode) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.PrepareMode = prepare
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid call config")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	c, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin call session", err)
	}
	defer func() { _ = c.Close() }()
	name := fmt.Sprintf("OBGC_%d", time.Now().UnixNano())
	procedure, function, failing := name+"P", name+"F", name+"E"
	definitions := []struct{ kind, name, sql string }{
		{"PROCEDURE", procedure, "CREATE PROCEDURE " + procedure + "(IN x BIGINT,INOUT y BIGINT,OUT z VARCHAR(40)) BEGIN SET y=x+y; SET z='ok'; END"},
		{"FUNCTION", function, "CREATE FUNCTION " + function + "(x BIGINT) RETURNS BIGINT DETERMINISTIC RETURN x*2"},
		{"PROCEDURE", failing, "CREATE PROCEDURE " + failing + "(OUT y BIGINT) BEGIN SET y=100; SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='out test'; END"},
	}
	if p.mode == oceanbase.ModeOracle {
		definitions[0].sql = "CREATE PROCEDURE " + procedure + "(x IN NUMBER,y IN OUT NUMBER,z OUT VARCHAR2) AS BEGIN y:=x+y; z:='ok'; END;"
		definitions[1].sql = "CREATE FUNCTION " + function + "(x IN NUMBER) RETURN NUMBER AS BEGIN RETURN x*2; END;"
		definitions[2].sql = "CREATE PROCEDURE " + failing + "(y OUT NUMBER) AS BEGIN y:=100; RAISE_APPLICATION_ERROR(-20001,'out test'); END;"
	}
	for _, definition := range definitions {
		if _, err = c.ExecContext(testContext(t), definition.sql); err != nil {
			failOperation(t, "create call object", err)
		}
		defer func() {
			if _, err := c.ExecContext(testContext(t), "DROP "+definition.kind+" "+definition.name); err != nil {
				failOperation(t, "drop call object", err)
			}
		}()
	}
	y, z := int64(40), "before"
	call := "CALL " + procedure + "(?,?,?)"
	args := []any{2, sql.Out{Dest: &y, In: true}, oceanbase.Out{Dest: &z, Type: "VARCHAR", Length: 40}}
	if p.mode == oceanbase.ModeOracle {
		call = "BEGIN " + procedure + "(:x,:y,:z); END;"
		args = []any{sql.Named("x", 2), sql.Named("y", args[1]), sql.Named("z", args[2])}
	}
	for i := 0; i < 2; i++ {
		statement, err := c.PrepareContext(testContext(t), call)
		if err != nil {
			failOperation(t, "prepare scalar call", err)
		}
		_, execErr := statement.ExecContext(testContext(t), args...)
		closeErr := statement.Close()
		if execErr != nil {
			failOperation(t, "execute scalar call", execErr)
		}
		if closeErr != nil || y != 42+int64(i*2) || z != "ok" {
			t.Fatal("INOUT and text output mismatch")
		}
	}
	var result oceanbase.Number
	query := "SELECT " + function + "(?) INTO ?"
	args = []any{21, sql.Out{Dest: &result}}
	if p.mode == oceanbase.ModeOracle {
		query = "BEGIN :r:=" + function + "(:x); END;"
		args = []any{sql.Named("r", args[1]), sql.Named("x", 21)}
	}
	if _, err = c.ExecContext(testContext(t), query, args...); err != nil {
		failOperation(t, "function output", err)
	}
	if result != "42" {
		t.Fatal("function output mismatch")
	}
	y = 7
	query = "CALL " + failing + "(?)"
	if p.mode == oceanbase.ModeOracle {
		query = "BEGIN " + failing + "(?); END;"
	}
	failureCode := uint32(1644)
	if p.mode == oceanbase.ModeOracle {
		failureCode = 20001
	}
	_, err = c.ExecContext(testContext(t), query, sql.Out{Dest: &y})
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != failureCode || y != 7 {
		t.Fatal("routine failure code or output mismatch")
	}
	if err = c.PingContext(testContext(t)); err != nil {
		failOperation(t, "call session reuse", err)
	}
}

func TestReturning(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			if p.mode != oceanbase.ModeOracle {
				t.Skip("RETURNING INTO is an Oracle compatibility mode capability")
			}
			db := openProfile(t, p)
			c, err := db.Conn(testContext(t))
			if err != nil {
				failOperation(t, "pin returning session", err)
			}
			defer func() { _ = c.Close() }()
			table := fmt.Sprintf("OBGR_%d", time.Now().UnixNano())
			if _, err = c.ExecContext(testContext(t), "CREATE TABLE "+table+"(id NUMBER PRIMARY KEY,v VARCHAR2(40))"); err != nil {
				failOperation(t, "create returning table", err)
			}
			defer func() {
				if _, err := c.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
					failOperation(t, "drop returning table", err)
				}
			}()
			var id int64
			var value string
			result, err := c.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES(:x,'ok') RETURNING id,v INTO :id,:v", sql.Named("x", 42), sql.Named("id", sql.Out{Dest: &id}), sql.Named("v", sql.Out{Dest: &value}))
			if err != nil {
				failOperation(t, "returning outputs", err)
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 || id != 42 || value != "ok" {
				t.Fatal("returning output or count mismatch")
			}
		})
	}
}
