//go:build integration

package lob_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestTableBlobProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.Limits.MaxLOBBytes = 1024
	if p.Mode == oceanbase.ModeMySQL {
		cfg.RowsAffectedMode = oceanbase.ChangedRows
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid table BLOB configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	defer func() { _ = db.Close() }()
	table := fmt.Sprintf("OBGTB_%d", time.Now().UnixNano())
	typ, empty, suffix := integrationIntegrationLongBlobType, "X''", integrationIntegrationInnoDBSuffix
	if p.Mode == oceanbase.ModeOracle {
		typ, empty, suffix = integrationIntegrationBlobType, "EMPTY_BLOB()", ""
	}
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER,PART VARCHAR(20),B "+typ+",PRIMARY KEY(ID,PART))"+suffix); err != nil {
		integrationtest.FailOperation(t, "create table BLOB fixture", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop table BLOB fixture", err)
		}
	}()
	for _, row := range []struct {
		id    int
		value string
	}{{1, empty}, {2, "NULL"}} {
		if _, err = db.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES (?,'a',"+row.value+")", row.id); err != nil {
			integrationtest.FailOperation(t, "insert table BLOB fixture", err)
		}
	}
	c, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin table BLOB session", err)
	}
	defer func() { _ = c.Close() }()
	target := oceanbase.BlobTarget{Table: table, Column: "B", Key: map[string]any{"ID": 1, "PART": "a"}}
	integrationTestTableBlobConn(t, c, target, p.Mode)
	tr, err := c.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin table BLOB transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	b, err := oceanbase.OpenTableBlob(integrationtest.TestContext(t), tr, target)
	if err != nil {
		integrationtest.FailOperation(t, "open transactional table BLOB", err)
	}
	integrationTestTableBlobContent(t, b)
	if p.Mode == oceanbase.ModeMySQL {
		integrationTestTableBlobLock(t, db, table)
	}
	if err = b.Truncate(integrationtest.TestContext(t), 3); err != nil {
		integrationtest.FailOperation(t, "truncate table BLOB", err)
	}
	if size, err := b.Size(integrationtest.TestContext(t)); err != nil || size != 3 {
		t.Fatal(size, err)
	}
	if err = tr.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback table BLOB", err)
	}
	if _, err = b.Size(integrationtest.TestContext(t)); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("table BLOB survived transaction", err)
	}
	if err = b.Close(); err != nil {
		integrationtest.FailOperation(t, "close expired table BLOB", err)
	}
	b, err = oceanbase.OpenTableBlob(integrationtest.TestContext(t), c, target)
	if err != nil {
		integrationtest.FailOperation(t, "reopen rolled back BLOB", err)
	}
	if size, err := b.Size(integrationtest.TestContext(t)); err != nil || size != 0 {
		t.Fatal("BLOB rollback", size, err)
	}
	if err = b.Close(); err != nil {
		integrationtest.FailOperation(t, "close rolled back BLOB", err)
	}
	if err = c.Close(); err != nil {
		integrationtest.FailOperation(t, "return table BLOB session", err)
	}
}

const integrationIntegrationBlobType = "BLOB"

const integrationIntegrationInnoDBSuffix = " ENGINE=InnoDB"

const integrationIntegrationLongBlobType = "LONGBLOB"

func integrationTestTableBlobConn(t *testing.T, c *sql.Conn, target oceanbase.BlobTarget, mode oceanbase.Mode) {
	t.Helper()
	b, err := oceanbase.OpenTableBlob(integrationtest.TestContext(t), c, target)
	if err != nil || b == nil {
		integrationtest.FailOperation(t, "open empty table BLOB", err)
	}
	if size, err := b.Size(integrationtest.TestContext(t)); err != nil || size != 0 {
		t.Fatal(size, err)
	}
	if mode == oceanbase.ModeMySQL {
		if _, err = b.WriteAtContext(integrationtest.TestContext(t), []byte{1}, 0); !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("table BLOB write outside transaction", err)
		}
	}
	if err = b.Close(); err != nil {
		integrationtest.FailOperation(t, "close table BLOB", err)
	}
	target.Key["ID"] = 2
	if b, err = oceanbase.OpenTableBlob(integrationtest.TestContext(t), c, target); b != nil || err != nil {
		t.Fatal("NULL table BLOB", b, err)
	}
	target.Key["ID"] = 99
	if _, err = oceanbase.OpenTableBlob(integrationtest.TestContext(t), c, target); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing table BLOB", err)
	}
	target.Key["ID"] = 1
}

func integrationTestTableBlobLock(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := db.ExecContext(ctx, "UPDATE "+table+" SET B=X'01' WHERE ID=1 AND PART='a'")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("concurrent table BLOB update was not blocked by the caller transaction", err)
	}
}

func integrationTestTableBlobContent(t *testing.T, b *oceanbase.Blob) {
	t.Helper()
	payload := bytes.Repeat([]byte{0, 1, 255, 2}, 1280)
	if n, err := b.WriteAtContext(integrationtest.TestContext(t), payload, 0); err != nil || n != len(payload) {
		t.Fatal(n, err)
	}
	if n, err := b.WriteAtContext(integrationtest.TestContext(t), payload, 0); err != nil || n != len(payload) {
		t.Fatal("unchanged BLOB update", n, err)
	}
	got := make([]byte, len(payload))
	if n, err := b.ReadAtContext(integrationtest.TestContext(t), got, 0); err != nil || n != len(payload) || !bytes.Equal(got, payload) {
		t.Fatal("BLOB exceeds materialization limit", n, err)
	}
}

func integrationTestNativeLOBProfile(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.ResetMode = oceanbase.ResetFull
	if p.Mode == oceanbase.ModeOracle {
		cfg.Session.NCharset = "utf16"
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid LOB configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if p.Mode != oceanbase.ModeOracle {
		c, err := db.Conn(integrationtest.TestContext(t))
		if err != nil {
			integrationtest.FailOperation(t, "pin LOB session", err)
		}
		defer func() { _ = c.Close() }()
		if _, err = oceanbase.OpenBlob(integrationtest.TestContext(t), c, "SELECT NULL"); !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("MySQL native locator accepted")
		}
		return
	}
	table := fmt.Sprintf("OBGL_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (id NUMBER PRIMARY KEY,b BLOB,c CLOB)"); err != nil {
		integrationtest.FailOperation(t, "create LOB table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop LOB table", err)
		}
	}()
	c, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin LOB session", err)
	}
	defer func() { _ = c.Close() }()
	tx, err := c.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin LOB transaction", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES (1,EMPTY_BLOB(),TO_CLOB('a中😀z'))"); err != nil {
		integrationtest.FailOperation(t, "insert uncommitted LOB", err)
	}
	b := integrationTestNativeBlob(t, tx, table)
	defer func() { _ = b.Close() }()
	integrationTestNativeClob(t, tx, table)
	var binary []byte
	var text string
	if err = tx.QueryRowContext(integrationtest.TestContext(t), "SELECT b,c FROM "+table+" WHERE id=1").Scan(&binary, &text); err != nil {
		integrationtest.FailOperation(t, "materialize uncommitted LOBs", err)
	}
	if string(binary) != string([]byte{0, 1}) || text != "a国😀" {
		t.Fatal("ordinary LOB values differ from same-transaction modifications")
	}
	if err = b.Close(); err != nil {
		integrationtest.FailOperation(t, "close BLOB", err)
	}
	old, err := oceanbase.OpenBlob(integrationtest.TestContext(t), tx, "SELECT b FROM "+table+" WHERE id=1")
	if err != nil {
		integrationtest.FailOperation(t, "open expiring BLOB", err)
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback LOB transaction", err)
	}
	if _, err = old.Size(integrationtest.TestContext(t)); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("LOB survived its transaction")
	}
	if err = old.Close(); err != nil {
		integrationtest.FailOperation(t, "close expired BLOB", err)
	}
	temporary, err := oceanbase.OpenBlob(integrationtest.TestContext(t), c, "SELECT TO_BLOB(HEXTORAW('0102')) FROM DUAL")
	if err != nil || temporary == nil {
		integrationtest.FailOperation(t, "open temporary BLOB", err)
	}
	if err = temporary.Close(); err != nil {
		integrationtest.FailOperation(t, "release temporary BLOB", err)
	}
	if err = c.Close(); err != nil {
		integrationtest.FailOperation(t, "return LOB session", err)
	}
	if err = db.PingContext(integrationtest.TestContext(t)); err != nil {
		integrationtest.FailOperation(t, "reset LOB session", err)
	}
}

func integrationTestNativeClob(t *testing.T, tx *sql.Tx, table string) {
	t.Helper()
	c, err := oceanbase.OpenClob(integrationtest.TestContext(t), tx, "SELECT c FROM "+table+" WHERE id=1 FOR UPDATE")
	if err != nil || c == nil {
		integrationtest.FailOperation(t, "open CLOB", err)
	}
	defer func() { _ = c.Close() }()
	if text, err := c.ReadChars(integrationtest.TestContext(t), 1, 2); err != nil || text != "中😀" {
		integrationtest.FailOperation(t, "CLOB code point read", err)
	}
	if n, err := c.WriteChars(integrationtest.TestContext(t), 1, "国"); err != nil || n != 1 {
		integrationtest.FailOperation(t, "write CLOB", err)
	}
	if err = c.TruncateChars(integrationtest.TestContext(t), 3); err != nil {
		integrationtest.FailOperation(t, "truncate CLOB", err)
	}
	if size, err := c.CharLength(integrationtest.TestContext(t)); err != nil || size != 3 {
		integrationtest.FailOperation(t, "CLOB character length", err)
	}
}

func integrationTestNativeBlob(t *testing.T, tx *sql.Tx, table string) *oceanbase.Blob {
	t.Helper()
	b, err := oceanbase.OpenBlob(integrationtest.TestContext(t), tx, "SELECT b FROM "+table+" WHERE id=? FOR UPDATE", 1)
	if err != nil || b == nil {
		integrationtest.FailOperation(t, "open BLOB", err)
	}
	if n, err := b.WriteAtContext(integrationtest.TestContext(t), []byte{0, 1, 255}, 0); err != nil || n != 3 {
		integrationtest.FailOperation(t, "write BLOB", err)
	}
	value := make([]byte, 3)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.ReadAtContext(canceled, value, 0); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-canceled BLOB operation accepted")
	}
	if n, err := b.ReadAtContext(integrationtest.TestContext(t), value, 0); err != nil || n != 3 || string(value) != string([]byte{0, 1, 255}) {
		integrationtest.FailOperation(t, "read BLOB in original transaction", err)
	}
	if err = b.Truncate(integrationtest.TestContext(t), 2); err != nil {
		integrationtest.FailOperation(t, "truncate BLOB", err)
	}
	if size, err := b.Size(integrationtest.TestContext(t)); err != nil || size != 2 {
		integrationtest.FailOperation(t, "BLOB size", err)
	}
	return b
}

func integrationTestNativeNCLOB(t *testing.T, p integrationtest.Profile) {
	db := integrationtest.OpenProfile(t, p)
	table := fmt.Sprintf("OBGN_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+"(N NCLOB)"); err != nil {
		if integrationtest.OnlyServerErrorCode(err, 902) {
			t.Skip("server rejects NCLOB with OBE-00902; BLOB/CLOB tested separately")
		}
		integrationtest.FailOperation(t, "create NCLOB table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop NCLOB table", err)
		}
	}()
	if _, err := db.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES(TO_NCLOB('a中😀z'))"); err != nil {
		integrationtest.FailOperation(t, "insert NCLOB", err)
	}
	s, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin NCLOB session", err)
	}
	defer func() { _ = s.Close() }()
	n, err := oceanbase.OpenClob(integrationtest.TestContext(t), s, "SELECT n FROM "+table)
	if err != nil || n == nil {
		integrationtest.FailOperation(t, "open NCLOB", err)
	}
	defer func() { _ = n.Close() }()
	if text, err := n.ReadChars(integrationtest.TestContext(t), 1, 2); err != nil || text != "中😀" {
		integrationtest.FailOperation(t, "NCLOB code point read", err)
	}
}
