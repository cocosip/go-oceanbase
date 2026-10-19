package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

func runLongData(ctx context.Context, s *scenario) (err error) {
	binaryType, textType := "LONGBLOB", "LONGTEXT"
	if s.mode == oceanbase.ModeOracle {
		binaryType, textType = blobType, "CLOB"
	}
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,B "+binaryType+",C "+textType+")")
	if err != nil {
		return err
	}
	payload, text := bytes.Repeat([]byte{0, 1, 127, 128, 255}, 30000), strings.Repeat(unicodeValue, 3000)
	for _, commit := range []bool{false, true} {
		if err = longDataTransaction(ctx, s, table, payload, text, commit); err != nil {
			return err
		}
	}
	return nil
}

func longDataTransaction(ctx context.Context, s *scenario, table string, payload []byte, text string, commit bool) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	binary := &oceanbase.LongData{Reader: bytes.NewReader(payload), Length: int64(len(payload))}
	chars := &oceanbase.LongData{Reader: strings.NewReader(text), Length: -1, Text: true}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,?,?)", binary, chars); err != nil {
		return err
	}
	var got []byte
	var gotText string
	if err = tx.QueryRowContext(ctx, "SELECT B,C FROM "+table+" WHERE ID=1").Scan(&got, &gotText); err != nil {
		return err
	}
	if err = expect(bytes.Equal(got, payload) && gotText == text, "long parameter content mismatch"); err != nil {
		return err
	}
	want := 0
	if commit {
		err, want = tx.Commit(), 1
	} else {
		err = tx.Rollback()
	}
	if err != nil {
		return err
	}
	return countRows(ctx, s.db, table, want)
}

func runTableBlob(ctx context.Context, s *scenario) (err error) {
	typ, empty := "LONGBLOB", "X''"
	if s.mode == oceanbase.ModeOracle {
		typ, empty = blobType, "EMPTY_BLOB()"
	}
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,B "+typ+")")
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,"+empty+")"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	b, err := oceanbase.OpenTableBlob(ctx, tx, oceanbase.BlobTarget{Table: table, Column: "B", Key: map[string]any{"ID": 1}})
	if err != nil {
		return err
	}
	if b == nil {
		return checkFailure("non-NULL table BLOB returned no handle")
	}
	defer closeInto(b, &err)
	data := bytes.Repeat([]byte{0, 1, 255, 2}, 1280)
	n, err := b.WriteAtContext(ctx, data, 0)
	if err != nil {
		return err
	}
	if err = expect(n == len(data), "BLOB write was short"); err != nil {
		return err
	}
	got := make([]byte, len(data))
	n, err = b.ReadAtContext(ctx, got, 0)
	if err != nil {
		return err
	}
	if err = expect(n == len(data) && bytes.Equal(got, data), "BLOB content mismatch"); err != nil {
		return err
	}
	if err = b.Truncate(ctx, 3); err != nil {
		return err
	}
	size, err := b.Size(ctx)
	if err != nil {
		return err
	}
	if err = expect(size == 3, "BLOB truncate size mismatch"); err != nil {
		return err
	}
	if err = b.Close(); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	var raw []byte
	if err = s.db.QueryRowContext(ctx, "SELECT B FROM "+table+" WHERE ID=1").Scan(&raw); err != nil {
		return err
	}
	return expect(len(raw) == 0, "BLOB changes survived rollback")
}

func runNativeLOB(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,B BLOB,C CLOB)")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,EMPTY_BLOB(),TO_CLOB('a中😀z'))"); err != nil {
		return err
	}
	b, err := oceanbase.OpenBlob(ctx, tx, "SELECT B FROM "+table+" WHERE ID=1 FOR UPDATE")
	if err != nil {
		return err
	}
	if b == nil {
		return checkFailure("native BLOB handle missing")
	}
	defer closeInto(b, &err)
	if n, e := b.WriteAtContext(ctx, []byte{0, 1, 255}, 0); e != nil {
		return e
	} else if n != 3 {
		return checkFailure("native BLOB write was short")
	}
	got := make([]byte, 3)
	if n, e := b.ReadAtContext(ctx, got, 0); e != nil {
		return e
	} else if n != 3 || !bytes.Equal(got, []byte{0, 1, 255}) {
		return checkFailure("native BLOB content mismatch")
	}
	if err = b.Truncate(ctx, 2); err != nil {
		return err
	}
	if err = nativeClobs(ctx, tx, table); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	_, e := b.Size(ctx)
	if err = expect(errors.Is(e, sql.ErrTxDone) || errors.Is(e, oceanbase.ErrResource), "native BLOB survived its transaction"); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 0)
}

func nativeClobs(ctx context.Context, tx *sql.Tx, table string) (err error) {
	c, err := oceanbase.OpenClob(ctx, tx, "SELECT C FROM "+table+" WHERE ID=1 FOR UPDATE")
	if err != nil {
		return err
	}
	if c == nil {
		return checkFailure("native CLOB handle missing")
	}
	defer closeInto(c, &err)
	text, err := c.ReadChars(ctx, 1, 2)
	if err != nil {
		return err
	}
	if err = expect(text == "中😀", "CLOB character offsets mismatch"); err != nil {
		return err
	}
	if n, e := c.WriteChars(ctx, 1, "国"); e != nil {
		return e
	} else if n != 1 {
		return checkFailure("CLOB character write was short")
	}
	if err = c.TruncateChars(ctx, 3); err != nil {
		return err
	}
	text, err = c.ReadChars(ctx, 0, 3)
	if err != nil {
		return err
	}
	if err = expect(text == "a国😀", "CLOB mutation mismatch"); err != nil {
		return err
	}
	return nil
}

func runNativeNCLOB(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(N NCLOB)")
	if err != nil {
		if onlyServerErrorCode(err, 902) {
			return skipped("server rejects NCLOB with OBE-00902; BLOB/CLOB tested separately")
		}
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES(TO_NCLOB('a中😀z'))"); err != nil {
		return err
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer closeInto(c, &err)
	n, err := oceanbase.OpenClob(ctx, c, "SELECT N FROM "+table)
	if err != nil {
		return err
	}
	if n == nil {
		return checkFailure("native NCLOB handle missing")
	}
	defer closeInto(n, &err)
	text, err := n.ReadChars(ctx, 1, 2)
	if err != nil {
		return err
	}
	return expect(text == "中😀", "NCLOB Unicode content mismatch")
}
