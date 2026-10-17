//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestTableBlob(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testTableBlobProfile(t, p) })
	}
}

func testTableBlobProfile(t *testing.T, p profile) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.Limits.MaxLOBBytes = 1024
	if p.mode == oceanbase.ModeMySQL {
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
	typ, empty, suffix := "LONGBLOB", "X''", " ENGINE=InnoDB"
	if p.mode == oceanbase.ModeOracle {
		typ, empty, suffix = integrationBlobType, "EMPTY_BLOB()", ""
	}
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER,PART VARCHAR(20),B "+typ+",PRIMARY KEY(ID,PART))"+suffix); err != nil {
		failOperation(t, "create table BLOB fixture", err)
	}
	defer func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop table BLOB fixture", err)
		}
	}()
	for _, row := range []struct {
		id    int
		value string
	}{{1, empty}, {2, "NULL"}} {
		if _, err = db.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES (?,'a',"+row.value+")", row.id); err != nil {
			failOperation(t, "insert table BLOB fixture", err)
		}
	}
	c, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin table BLOB session", err)
	}
	defer func() { _ = c.Close() }()
	target := oceanbase.BlobTarget{Table: table, Column: "B", Key: map[string]any{"ID": 1, "PART": "a"}}
	testTableBlobConn(t, c, target, p.mode)
	tr, err := c.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin table BLOB transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	b, err := oceanbase.OpenTableBlob(testContext(t), tr, target)
	if err != nil {
		failOperation(t, "open transactional table BLOB", err)
	}
	testTableBlobContent(t, b)
	if p.mode == oceanbase.ModeMySQL {
		testTableBlobLock(t, db, table)
	}
	if err = b.Truncate(testContext(t), 3); err != nil {
		failOperation(t, "truncate table BLOB", err)
	}
	if size, err := b.Size(testContext(t)); err != nil || size != 3 {
		t.Fatal(size, err)
	}
	if err = tr.Rollback(); err != nil {
		failOperation(t, "rollback table BLOB", err)
	}
	if _, err = b.Size(testContext(t)); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("table BLOB survived transaction", err)
	}
	if err = b.Close(); err != nil {
		failOperation(t, "close expired table BLOB", err)
	}
	b, err = oceanbase.OpenTableBlob(testContext(t), c, target)
	if err != nil {
		failOperation(t, "reopen rolled back BLOB", err)
	}
	if size, err := b.Size(testContext(t)); err != nil || size != 0 {
		t.Fatal("BLOB rollback", size, err)
	}
	if err = b.Close(); err != nil {
		failOperation(t, "close rolled back BLOB", err)
	}
	if err = c.Close(); err != nil {
		failOperation(t, "return table BLOB session", err)
	}
}

func testTableBlobLock(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := db.ExecContext(ctx, "UPDATE "+table+" SET B=X'01' WHERE ID=1 AND PART='a'")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("concurrent table BLOB update was not blocked by the caller transaction", err)
	}
}

func testTableBlobConn(t *testing.T, c *sql.Conn, target oceanbase.BlobTarget, mode oceanbase.Mode) {
	t.Helper()
	b, err := oceanbase.OpenTableBlob(testContext(t), c, target)
	if err != nil || b == nil {
		failOperation(t, "open empty table BLOB", err)
	}
	if size, err := b.Size(testContext(t)); err != nil || size != 0 {
		t.Fatal(size, err)
	}
	if mode == oceanbase.ModeMySQL {
		if _, err = b.WriteAtContext(testContext(t), []byte{1}, 0); !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("table BLOB write outside transaction", err)
		}
	}
	if err = b.Close(); err != nil {
		failOperation(t, "close table BLOB", err)
	}
	target.Key["ID"] = 2
	if b, err = oceanbase.OpenTableBlob(testContext(t), c, target); b != nil || err != nil {
		t.Fatal("NULL table BLOB", b, err)
	}
	target.Key["ID"] = 99
	if _, err = oceanbase.OpenTableBlob(testContext(t), c, target); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing table BLOB", err)
	}
	target.Key["ID"] = 1
}

func testTableBlobContent(t *testing.T, b *oceanbase.Blob) {
	t.Helper()
	payload := bytes.Repeat([]byte{0, 1, 255, 2}, 1280)
	if n, err := b.WriteAtContext(testContext(t), payload, 0); err != nil || n != len(payload) {
		t.Fatal(n, err)
	}
	if n, err := b.WriteAtContext(testContext(t), payload, 0); err != nil || n != len(payload) {
		t.Fatal("unchanged BLOB update", n, err)
	}
	got := make([]byte, len(payload))
	if n, err := b.ReadAtContext(testContext(t), got, 0); err != nil || n != len(payload) || !bytes.Equal(got, payload) {
		t.Fatal("BLOB exceeds materialization limit", n, err)
	}
}
