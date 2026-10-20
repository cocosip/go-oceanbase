//go:build integration

package call_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	io "io"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestProcedureResources(t *testing.T, p integrationtest.Profile) {
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.Limits.MaxLOBBytes = 1024
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid procedure resource configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	s, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin procedure session", err)
	}
	defer func() { _ = s.Close() }()
	if p.Mode == oceanbase.ModeMySQL {
		var cur *oceanbase.Cursor
		var blob *oceanbase.Blob
		for _, out := range []any{oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &blob, Type: integrationIntegrationBlobType}} {
			if _, err = oceanbase.Execute(integrationtest.TestContext(t), s, "CALL missing_resource_procedure(?)", out); !errors.Is(err, oceanbase.ErrUnsupported) {
				t.Fatal("MySQL resource output must reject before business send")
			}
		}
		if err = s.PingContext(integrationtest.TestContext(t)); err != nil {
			integrationtest.FailOperation(t, "session after rejected output", err)
		}
		return
	}
	name := fmt.Sprintf("OBGRO_%d", time.Now().UnixNano())
	definition := "CREATE PROCEDURE " + name + "(a OUT SYS_REFCURSOR,b OUT SYS_REFCURSOR,v OUT BLOB,c OUT CLOB,n OUT NUMBER) AS BEGIN OPEN a FOR SELECT 42 AS N FROM dual; OPEN b FOR SELECT 43 AS N FROM dual; DBMS_LOB.CREATETEMPORARY(v,TRUE); DBMS_LOB.WRITEAPPEND(v,32767,UTL_RAW.CAST_TO_RAW(RPAD('x',32767,'x'))); DBMS_LOB.CREATETEMPORARY(c,TRUE); DBMS_LOB.WRITEAPPEND(c,2,'你好'); n:=7; END;"
	if _, err = s.ExecContext(integrationtest.TestContext(t), definition); err != nil {
		integrationtest.FailOperation(t, "create resource procedure", err)
	}
	defer func() {
		if _, e := db.ExecContext(integrationtest.TestContext(t), "DROP PROCEDURE "+name); e != nil {
			integrationtest.FailOperation(t, "drop resource procedure", e)
		}
	}()
	for range 2 {
		integrationTestProcedureResourceOutputs(t, s, name)
	}
	integrationTestProcedureResourceFailureAndColumn(t, s)
	t.Run("child-cursor-column", func(t *testing.T) {
		fresh, err := db.Conn(integrationtest.TestContext(t))
		if err != nil {
			integrationtest.FailOperation(t, "pin child cursor session", err)
		}
		defer func() { _ = fresh.Close() }()
		integrationTestChildCursorColumn(t, fresh)
	})
}

func integrationTestProcedureResourceOutputs(t *testing.T, s *sql.Conn, name string) {
	callCtx, cancel := context.WithCancel(integrationtest.TestContext(t))
	var a, b *oceanbase.Cursor
	var blob *oceanbase.Blob
	var clob *oceanbase.Clob
	var n int64
	e, err := oceanbase.Execute(callCtx, s, "BEGIN "+name+"(?,?,?,?,?); END;", oceanbase.CursorOut{Dest: &a}, oceanbase.CursorOut{Dest: &b}, oceanbase.Out{Dest: &blob, Type: integrationIntegrationBlobType}, oceanbase.Out{Dest: &clob, Type: "CLOB"}, sql.Out{Dest: &n})
	if err != nil {
		cancel()
		integrationtest.FailOperation(t, "execute resource procedure", err)
	}
	if err = e.Close(); err != nil {
		cancel()
		integrationtest.FailOperation(t, "finalize resource procedure", err)
	}
	cancel()
	if a == nil || b == nil || blob == nil || clob == nil || n != 7 || !e.Snapshot().Done {
		t.Fatal("missing resource outputs")
	}
	defer func() { _ = a.Close(); _ = b.Close(); _ = blob.Close(); _ = clob.Close() }()
	for i, cur := range []*oceanbase.Cursor{a, b} {
		rows, err := cur.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1)
		if err != nil {
			integrationtest.FailOperation(t, "fetch output cursor with fresh context", err)
		}
		var got int64
		if !rows.Next() || rows.Scan(&got) != nil || got != int64(42+i) {
			t.Fatal("output cursor row mismatch")
		}
		if err = rows.Close(); err != nil {
			integrationtest.FailOperation(t, "close cursor batch", err)
		}
		if err = cur.Close(); err != nil {
			integrationtest.FailOperation(t, "close output cursor", err)
		}
	}
	data := make([]byte, 4096)
	if count, err := blob.ReadAtContext(integrationtest.TestContext(t), data, 30000); count != 2767 || !errors.Is(err, io.EOF) || string(data[:count]) != strings.Repeat("x", 2767) {
		t.Fatal("streaming output BLOB mismatch")
	}
	if text, err := clob.ReadChars(integrationtest.TestContext(t), 0, 2); err != nil || text != "你好" {
		t.Fatal("output CLOB mismatch")
	}
	if err = blob.Close(); err != nil {
		integrationtest.FailOperation(t, "close output BLOB", err)
	}
	if err = clob.Close(); err != nil {
		integrationtest.FailOperation(t, "close last output lease", err)
	}
}

const integrationIntegrationBlobType = "BLOB"

func integrationTestChildCursorColumn(t *testing.T, s *sql.Conn) {
	statement, err := s.PrepareContext(integrationtest.TestContext(t), "SELECT CURSOR(SELECT 42 AS N FROM dual) AS C FROM dual")
	if err != nil {
		integrationtest.FailOperation(t, "prepare child cursor column", err)
	}
	defer func() { _ = statement.Close() }()
	rows, err := statement.QueryContext(integrationtest.TestContext(t))
	if err != nil {
		if integrationtest.OnlyServerErrorCode(err, 902) {
			t.Skip("server rejects CURSOR expression with OBE-00902; OUT cursors tested separately")
		}
		integrationtest.FailOperation(t, "query child cursor column", err)
	}
	defer func() { _ = rows.Close() }()
	var child sql.Rows
	var n int64
	if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&n) != nil || n != 42 {
		t.Fatal("standard child Rows mismatch")
	}
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close child cursor parent", err)
	}
	if child.Next() {
		t.Fatal("child survived parent Rows.Close")
	}
	if err = s.PingContext(integrationtest.TestContext(t)); err != nil {
		integrationtest.FailOperation(t, "session after procedure resources", err)
	}
}

func integrationTestProcedureResourceFailureAndColumn(t *testing.T, s *sql.Conn) {
	var failed *oceanbase.Cursor
	_, err := oceanbase.Execute(integrationtest.TestContext(t), s, "BEGIN OPEN ? FOR SELECT 1 FROM dual; RAISE_APPLICATION_ERROR(-20001,'resource failure'); END;", oceanbase.CursorOut{Dest: &failed})
	if err == nil || failed != nil {
		t.Fatal("failed procedure published a cursor")
	}
}

func integrationTestScalarCallProfile(t *testing.T, p integrationtest.Profile, prepare oceanbase.PrepareMode) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = prepare
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid call config")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	c, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin call session", err)
	}
	defer func() { _ = c.Close() }()
	name := fmt.Sprintf("OBGC_%d", time.Now().UnixNano())
	procedure, function, failing := name+"P", name+"F", name+"E"
	definitions := []struct{ kind, name, sql string }{
		{"PROCEDURE", procedure, "CREATE PROCEDURE " + procedure + "(IN x BIGINT,INOUT y BIGINT,OUT z VARCHAR(40)) BEGIN SET y=x+y; SET z='ok'; END"},
		{"FUNCTION", function, "CREATE FUNCTION " + function + "(x BIGINT) RETURNS BIGINT DETERMINISTIC RETURN x*2"},
		{"PROCEDURE", failing, "CREATE PROCEDURE " + failing + "(OUT y BIGINT) BEGIN SET y=100; SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='out test'; END"},
	}
	if p.Mode == oceanbase.ModeOracle {
		definitions[0].sql = "CREATE PROCEDURE " + procedure + "(x IN NUMBER,y IN OUT NUMBER,z OUT VARCHAR2) AS BEGIN y:=x+y; z:='ok'; END;"
		definitions[1].sql = "CREATE FUNCTION " + function + "(x IN NUMBER) RETURN NUMBER AS BEGIN RETURN x*2; END;"
		definitions[2].sql = "CREATE PROCEDURE " + failing + "(y OUT NUMBER) AS BEGIN y:=100; RAISE_APPLICATION_ERROR(-20001,'out test'); END;"
	}
	for _, definition := range definitions {
		if _, err = c.ExecContext(integrationtest.TestContext(t), definition.sql); err != nil {
			integrationtest.FailOperation(t, "create call object", err)
		}
		defer func() {
			if _, err := c.ExecContext(integrationtest.TestContext(t), "DROP "+definition.kind+" "+definition.name); err != nil {
				integrationtest.FailOperation(t, "drop call object", err)
			}
		}()
	}
	y, z := int64(40), "before"
	call := "CALL " + procedure + "(?,?,?)"
	args := []any{2, sql.Out{Dest: &y, In: true}, oceanbase.Out{Dest: &z, Type: "VARCHAR", Length: 40}}
	if p.Mode == oceanbase.ModeOracle {
		call = "BEGIN " + procedure + "(:x,:y,:z); END;"
		args = []any{sql.Named("x", 2), sql.Named("y", args[1]), sql.Named("z", args[2])}
	}
	for i := 0; i < 2; i++ {
		statement, err := c.PrepareContext(integrationtest.TestContext(t), call)
		if err != nil {
			integrationtest.FailOperation(t, "prepare scalar call", err)
		}
		_, execErr := statement.ExecContext(integrationtest.TestContext(t), args...)
		closeErr := statement.Close()
		if execErr != nil {
			integrationtest.FailOperation(t, "execute scalar call", execErr)
		}
		if closeErr != nil || y != 42+int64(i*2) || z != "ok" {
			t.Fatal("INOUT and text output mismatch")
		}
	}
	var result oceanbase.Number
	query := "SELECT " + function + "(?) INTO ?"
	args = []any{21, sql.Out{Dest: &result}}
	if p.Mode == oceanbase.ModeOracle {
		query = "BEGIN :r:=" + function + "(:x); END;"
		args = []any{sql.Named("r", args[1]), sql.Named("x", 21)}
	}
	if _, err = c.ExecContext(integrationtest.TestContext(t), query, args...); err != nil {
		integrationtest.FailOperation(t, "function output", err)
	}
	if result != "42" {
		t.Fatal("function output mismatch")
	}
	y = 7
	query = "CALL " + failing + "(?)"
	if p.Mode == oceanbase.ModeOracle {
		query = "BEGIN " + failing + "(?); END;"
	}
	failureCode := uint32(1644)
	if p.Mode == oceanbase.ModeOracle {
		failureCode = 20001
	}
	_, err = c.ExecContext(integrationtest.TestContext(t), query, sql.Out{Dest: &y})
	if !integrationtest.OnlyServerErrorCode(err, failureCode) || y != 7 {
		t.Fatal("routine failure code or output mismatch")
	}
	if err = c.PingContext(integrationtest.TestContext(t)); err != nil {
		integrationtest.FailOperation(t, "call session reuse", err)
	}
}
