package main

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/cocosip/go-oceanbase"
)

func runRoutines(ctx context.Context, s *scenario) error {
	proc := "(IN x BIGINT,INOUT y BIGINT,OUT z VARCHAR(40)) BEGIN SET y=x+y; SET z='ok'; END"
	fn := "(x BIGINT) RETURNS BIGINT DETERMINISTIC RETURN x*2"
	failing := "(OUT y BIGINT) BEGIN SET y=100; SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='obtest'; END"
	failureCode := uint32(1644)
	if s.mode == oceanbase.ModeOracle {
		proc = "(x IN NUMBER,y IN OUT NUMBER,z OUT VARCHAR2) AS BEGIN y:=x+y; z:='ok'; END;"
		fn = "(x IN NUMBER) RETURN NUMBER AS BEGIN RETURN x*2; END;"
		failing = "(y OUT NUMBER) AS BEGIN y:=100; RAISE_APPLICATION_ERROR(-20001,'obtest'); END;"
		failureCode = 20001
	}
	p, err := s.create(ctx, "PROCEDURE", "P", proc)
	if err != nil {
		return err
	}
	f, err := s.create(ctx, "FUNCTION", "F", fn)
	if err != nil {
		return err
	}
	e, err := s.create(ctx, "PROCEDURE", "E", failing)
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		y, z := int64(40), "before"
		call := "CALL " + p + "(?,?,?)"
		if s.mode == oceanbase.ModeOracle {
			call = "BEGIN " + p + "(?,?,?); END;"
		}
		for i := range 2 {
			if _, err := c.ExecContext(ctx, call, 2, sql.Out{Dest: &y, In: true}, oceanbase.Out{Dest: &z, Type: "VARCHAR", Length: 40}); err != nil {
				return err
			}
			if err := expect(y == 42+int64(i*2) && z == "ok", "routine INOUT/OUT mismatch"); err != nil {
				return err
			}
		}
		var output oceanbase.Number
		call = "SELECT " + f + "(?) INTO ?"
		args := []any{21, sql.Out{Dest: &output}}
		if s.mode == oceanbase.ModeOracle {
			call, args = "BEGIN ? := "+f+"(?); END;", []any{sql.Out{Dest: &output}, 21}
		}
		if _, err := c.ExecContext(ctx, call, args...); err != nil {
			return err
		}
		if err := expect(output == "42", "function output mismatch"); err != nil {
			return err
		}
		y = 7
		call = "CALL " + e + "(?)"
		if s.mode == oceanbase.ModeOracle {
			call = "BEGIN " + e + "(?); END;"
		}
		_, callErr := c.ExecContext(ctx, call, sql.Out{Dest: &y})
		var server *oceanbase.Error
		if err := expect(errors.As(callErr, &server) && server.Code == failureCode && y == 7, "routine failure code or output mismatch"); err != nil {
			return err
		}
		return c.PingContext(ctx)
	})
}

func runReturning(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID NUMBER PRIMARY KEY,V VARCHAR2(40))")
	if err != nil {
		return err
	}
	var id int64
	var value string
	r, err := s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(:x,'ok') RETURNING ID,V INTO :id,:v", sql.Named("x", 42), sql.Named("id", sql.Out{Dest: &id}), sql.Named("v", sql.Out{Dest: &value}))
	if err = affected(r, err, 1); err != nil {
		return err
	}
	if err = expect(id == 42 && value == "ok", "RETURNING output mismatch"); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 1)
}

func runResourceOutputs(ctx context.Context, s *scenario) error {
	definition := "(r OUT SYS_REFCURSOR,b OUT BLOB,c OUT CLOB) AS BEGIN OPEN r FOR SELECT 42 AS N FROM dual; DBMS_LOB.CREATETEMPORARY(b,TRUE); DBMS_LOB.WRITEAPPEND(b,3,HEXTORAW('0001FF')); DBMS_LOB.CREATETEMPORARY(c,TRUE); DBMS_LOB.WRITEAPPEND(c,2,'你好'); END;"
	name, err := s.create(ctx, "PROCEDURE", "P", definition)
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		var cur *oceanbase.Cursor
		var blob *oceanbase.Blob
		var clob *oceanbase.Clob
		e, err := oceanbase.Execute(ctx, c, "BEGIN "+name+"(?,?,?); END;", oceanbase.CursorOut{Dest: &cur}, oceanbase.Out{Dest: &blob, Type: blobType}, oceanbase.Out{Dest: &clob, Type: "CLOB"})
		if err != nil {
			return err
		}
		defer closeInto(e, &err)
		if err = e.Close(); err != nil {
			return err
		}
		if cur != nil {
			defer closeInto(cur, &err)
		}
		if blob != nil {
			defer closeInto(blob, &err)
		}
		if clob != nil {
			defer closeInto(clob, &err)
		}
		if cur == nil || blob == nil || clob == nil {
			return checkFailure("procedure resource outputs missing")
		}
		rows, err := cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
		if err != nil {
			return err
		}
		if err = checkCursorBatch(rows, 42, ""); err != nil {
			return err
		}
		data := make([]byte, 3)
		n, err := blob.ReadAtContext(ctx, data, 0)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if err = expect(n == 3 && string(data) == string([]byte{0, 1, 255}), "output BLOB content mismatch"); err != nil {
			return err
		}
		text, err := clob.ReadChars(ctx, 0, 2)
		if err != nil {
			return err
		}
		return expect(text == "你好", "output CLOB content mismatch")
	})
}
