//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestNativeLOB(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			t.Run("BLOB-CLOB", func(t *testing.T) { testNativeLOBProfile(t, p) })
			if p.mode == oceanbase.ModeOracle {
				t.Run("NCLOB", func(t *testing.T) { testNativeNCLOB(t, p) })
			}
		})
	}
}

func testNativeLOBProfile(t *testing.T, p profile) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.ResetMode = oceanbase.ResetFull
	if p.mode == oceanbase.ModeOracle {
		cfg.Session.NCharset = "utf16"
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid LOB configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if p.mode != oceanbase.ModeOracle {
		c, err := db.Conn(testContext(t))
		if err != nil {
			failOperation(t, "pin LOB session", err)
		}
		defer func() { _ = c.Close() }()
		if _, err = oceanbase.OpenBlob(testContext(t), c, "SELECT NULL"); !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("MySQL native locator accepted")
		}
		return
	}
	table := fmt.Sprintf("OBGL_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (id NUMBER PRIMARY KEY,b BLOB,c CLOB)"); err != nil {
		failOperation(t, "create LOB table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop LOB table", err)
		}
	}()
	c, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin LOB session", err)
	}
	defer func() { _ = c.Close() }()
	tx, err := c.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin LOB transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES (1,EMPTY_BLOB(),TO_CLOB('a中😀z'))"); err != nil {
		failOperation(t, "insert uncommitted LOB", err)
	}
	b := testNativeBlob(t, tx, table)
	defer func() { _ = b.Close() }()
	testNativeClob(t, tx, table)
	var binary []byte
	var text string
	if err = tx.QueryRowContext(testContext(t), "SELECT b,c FROM "+table+" WHERE id=1").Scan(&binary, &text); err != nil {
		failOperation(t, "materialize uncommitted LOBs", err)
	}
	if string(binary) != string([]byte{0, 1}) || text != "a国😀" {
		t.Fatal("ordinary LOB values differ from same-transaction modifications")
	}
	if err = b.Close(); err != nil {
		failOperation(t, "close BLOB", err)
	}
	old, err := oceanbase.OpenBlob(testContext(t), tx, "SELECT b FROM "+table+" WHERE id=1")
	if err != nil {
		failOperation(t, "open expiring BLOB", err)
	}
	if err = tx.Rollback(); err != nil {
		failOperation(t, "rollback LOB transaction", err)
	}
	if _, err = old.Size(testContext(t)); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("LOB survived its transaction")
	}
	if err = old.Close(); err != nil {
		failOperation(t, "close expired BLOB", err)
	}
	temporary, err := oceanbase.OpenBlob(testContext(t), c, "SELECT TO_BLOB(HEXTORAW('0102')) FROM DUAL")
	if err != nil || temporary == nil {
		failOperation(t, "open temporary BLOB", err)
	}
	if err = temporary.Close(); err != nil {
		failOperation(t, "release temporary BLOB", err)
	}
	if err = c.Close(); err != nil {
		failOperation(t, "return LOB session", err)
	}
	if err = db.PingContext(testContext(t)); err != nil {
		failOperation(t, "reset LOB session", err)
	}
}

func testNativeClob(t *testing.T, tx *sql.Tx, table string) {
	t.Helper()
	c, err := oceanbase.OpenClob(testContext(t), tx, "SELECT c FROM "+table+" WHERE id=1 FOR UPDATE")
	if err != nil || c == nil {
		failOperation(t, "open CLOB", err)
	}
	defer func() { _ = c.Close() }()
	if text, err := c.ReadChars(testContext(t), 1, 2); err != nil || text != "中😀" {
		failOperation(t, "CLOB code point read", err)
	}
	if n, err := c.WriteChars(testContext(t), 1, "国"); err != nil || n != 1 {
		failOperation(t, "write CLOB", err)
	}
	if err = c.TruncateChars(testContext(t), 3); err != nil {
		failOperation(t, "truncate CLOB", err)
	}
	if size, err := c.CharLength(testContext(t)); err != nil || size != 3 {
		failOperation(t, "CLOB character length", err)
	}
}

func testNativeNCLOB(t *testing.T, p profile) {
	db := openProfile(t, p)
	table := fmt.Sprintf("OBGN_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(testContext(t), "CREATE TABLE "+table+"(N NCLOB)"); err != nil {
		if onlyServerErrorCode(err, 902) {
			t.Skip("server rejects NCLOB with OBE-00902; BLOB/CLOB tested separately")
		}
		failOperation(t, "create NCLOB table", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop NCLOB table", err)
		}
	}()
	if _, err := db.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES(TO_NCLOB('a中😀z'))"); err != nil {
		failOperation(t, "insert NCLOB", err)
	}
	s, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin NCLOB session", err)
	}
	defer func() { _ = s.Close() }()
	n, err := oceanbase.OpenClob(testContext(t), s, "SELECT n FROM "+table)
	if err != nil || n == nil {
		failOperation(t, "open NCLOB", err)
	}
	defer func() { _ = n.Close() }()
	if text, err := n.ReadChars(testContext(t), 1, 2); err != nil || text != "中😀" {
		failOperation(t, "NCLOB code point read", err)
	}
}

func testNativeBlob(t *testing.T, tx *sql.Tx, table string) *oceanbase.Blob {
	t.Helper()
	b, err := oceanbase.OpenBlob(testContext(t), tx, "SELECT b FROM "+table+" WHERE id=? FOR UPDATE", 1)
	if err != nil || b == nil {
		failOperation(t, "open BLOB", err)
	}
	if n, err := b.WriteAtContext(testContext(t), []byte{0, 1, 255}, 0); err != nil || n != 3 {
		failOperation(t, "write BLOB", err)
	}
	value := make([]byte, 3)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.ReadAtContext(canceled, value, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled BLOB operation accepted")
	}
	if n, err := b.ReadAtContext(testContext(t), value, 0); err != nil || n != 3 || string(value) != string([]byte{0, 1, 255}) {
		failOperation(t, "read BLOB in original transaction", err)
	}
	if err = b.Truncate(testContext(t), 2); err != nil {
		failOperation(t, "truncate BLOB", err)
	}
	if size, err := b.Size(testContext(t)); err != nil || size != 2 {
		failOperation(t, "BLOB size", err)
	}
	return b
}
