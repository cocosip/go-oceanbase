//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const integrationBlobType = "BLOB"
const integrationInnoDBSuffix = " ENGINE=InnoDB"
const integrationLongBlobType = "LONGBLOB"

func TestProcedureResources(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testProcedureResources(t, p) })
	}
}

func testProcedureResources(t *testing.T, p profile) {
	cfg := profileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	cfg.Limits.MaxLOBBytes = 1024
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid procedure resource configuration")
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	s, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin procedure session", err)
	}
	defer func() { _ = s.Close() }()
	if p.mode == oceanbase.ModeMySQL {
		var cur *oceanbase.Cursor
		var blob *oceanbase.Blob
		for _, out := range []any{oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &blob, Type: integrationBlobType}} {
			if _, err = oceanbase.Execute(testContext(t), s, "CALL missing_resource_procedure(?)", out); !errors.Is(err, oceanbase.ErrUnsupported) {
				t.Fatal("MySQL resource output must reject before business send")
			}
		}
		if err = s.PingContext(testContext(t)); err != nil {
			failOperation(t, "session after rejected output", err)
		}
		return
	}
	name := fmt.Sprintf("OBGRO_%d", time.Now().UnixNano())
	definition := "CREATE PROCEDURE " + name + "(a OUT SYS_REFCURSOR,b OUT SYS_REFCURSOR,v OUT BLOB,c OUT CLOB,n OUT NUMBER) AS BEGIN OPEN a FOR SELECT 42 AS N FROM dual; OPEN b FOR SELECT 43 AS N FROM dual; DBMS_LOB.CREATETEMPORARY(v,TRUE); DBMS_LOB.WRITEAPPEND(v,32767,UTL_RAW.CAST_TO_RAW(RPAD('x',32767,'x'))); DBMS_LOB.CREATETEMPORARY(c,TRUE); DBMS_LOB.WRITEAPPEND(c,2,'你好'); n:=7; END;"
	if _, err = s.ExecContext(testContext(t), definition); err != nil {
		failOperation(t, "create resource procedure", err)
	}
	defer func() {
		if _, e := db.ExecContext(testContext(t), "DROP PROCEDURE "+name); e != nil {
			failOperation(t, "drop resource procedure", e)
		}
	}()
	for range 2 {
		testProcedureResourceOutputs(t, s, name)
	}
	testProcedureResourceFailureAndColumn(t, s)
	t.Run("child-cursor-column", func(t *testing.T) {
		fresh, err := db.Conn(testContext(t))
		if err != nil {
			failOperation(t, "pin child cursor session", err)
		}
		defer func() { _ = fresh.Close() }()
		testChildCursorColumn(t, fresh)
	})
}

func testProcedureResourceOutputs(t *testing.T, s *sql.Conn, name string) {
	callCtx, cancel := context.WithCancel(testContext(t))
	var a, b *oceanbase.Cursor
	var blob *oceanbase.Blob
	var clob *oceanbase.Clob
	var n int64
	e, err := oceanbase.Execute(callCtx, s, "BEGIN "+name+"(?,?,?,?,?); END;", oceanbase.CursorOut{Dest: &a}, oceanbase.CursorOut{Dest: &b}, oceanbase.Out{Dest: &blob, Type: integrationBlobType}, oceanbase.Out{Dest: &clob, Type: "CLOB"}, sql.Out{Dest: &n})
	if err != nil {
		cancel()
		failOperation(t, "execute resource procedure", err)
	}
	if err = e.Close(); err != nil {
		cancel()
		failOperation(t, "finalize resource procedure", err)
	}
	cancel()
	if a == nil || b == nil || blob == nil || clob == nil || n != 7 || !e.Snapshot().Done {
		t.Fatal("missing resource outputs")
	}
	defer func() { _ = a.Close(); _ = b.Close(); _ = blob.Close(); _ = clob.Close() }()
	for i, cur := range []*oceanbase.Cursor{a, b} {
		rows, err := cur.Fetch(testContext(t), oceanbase.CursorPosition{}, 1)
		if err != nil {
			failOperation(t, "fetch output cursor with fresh context", err)
		}
		var got int64
		if !rows.Next() || rows.Scan(&got) != nil || got != int64(42+i) {
			t.Fatal("output cursor row mismatch")
		}
		if err = rows.Close(); err != nil {
			failOperation(t, "close cursor batch", err)
		}
		if err = cur.Close(); err != nil {
			failOperation(t, "close output cursor", err)
		}
	}
	data := make([]byte, 4096)
	if count, err := blob.ReadAtContext(testContext(t), data, 30000); count != 2767 || !errors.Is(err, io.EOF) || string(data[:count]) != strings.Repeat("x", 2767) {
		t.Fatal("streaming output BLOB mismatch")
	}
	if text, err := clob.ReadChars(testContext(t), 0, 2); err != nil || text != "你好" {
		t.Fatal("output CLOB mismatch")
	}
	if err = blob.Close(); err != nil {
		failOperation(t, "close output BLOB", err)
	}
	if err = clob.Close(); err != nil {
		failOperation(t, "close last output lease", err)
	}
}

func testProcedureResourceFailureAndColumn(t *testing.T, s *sql.Conn) {
	var failed *oceanbase.Cursor
	_, err := oceanbase.Execute(testContext(t), s, "BEGIN OPEN ? FOR SELECT 1 FROM dual; RAISE_APPLICATION_ERROR(-20001,'resource failure'); END;", oceanbase.CursorOut{Dest: &failed})
	if err == nil || failed != nil {
		t.Fatal("failed procedure published a cursor")
	}
}

func testChildCursorColumn(t *testing.T, s *sql.Conn) {
	statement, err := s.PrepareContext(testContext(t), "SELECT CURSOR(SELECT 42 AS N FROM dual) AS C FROM dual")
	if err != nil {
		failOperation(t, "prepare child cursor column", err)
	}
	defer func() { _ = statement.Close() }()
	rows, err := statement.QueryContext(testContext(t))
	if err != nil {
		if onlyServerErrorCode(err, 902) {
			t.Skip("server rejects CURSOR expression with OBE-00902; OUT cursors tested separately")
		}
		failOperation(t, "query child cursor column", err)
	}
	defer func() { _ = rows.Close() }()
	var child sql.Rows
	var n int64
	if !rows.Next() || rows.Scan(&child) != nil || !child.Next() || child.Scan(&n) != nil || n != 42 {
		t.Fatal("standard child Rows mismatch")
	}
	if err = rows.Close(); err != nil {
		failOperation(t, "close child cursor parent", err)
	}
	if child.Next() {
		t.Fatal("child survived parent Rows.Close")
	}
	if err = s.PingContext(testContext(t)); err != nil {
		failOperation(t, "session after procedure resources", err)
	}
}
