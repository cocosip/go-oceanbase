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

func TestCursorModes(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testCursorModes(t, p) })
	}
}

func cursorFixture(t *testing.T, p profile) (*sql.DB, string) {
	t.Helper()
	cfg := profileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	if p.mode == oceanbase.ModeMySQL {
		cfg.RowsAffectedMode = oceanbase.ChangedRows
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid cursor configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(3)
	t.Cleanup(func() { _ = db.Close() })
	table := fmt.Sprintf("OBGCU_%d", time.Now().UnixNano())
	typ, blob, suffix := "LONGBLOB", "X'616263'", " ENGINE=InnoDB"
	if p.mode == oceanbase.ModeOracle {
		typ, blob, suffix = "BLOB", "TO_BLOB(HEXTORAW('616263'))", ""
	}
	if _, err = db.ExecContext(testContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(20),B "+typ+")"+suffix); err != nil {
		failOperation(t, "create cursor fixture", err)
	}
	t.Cleanup(func() {
		if _, e := db.ExecContext(testContext(t), "DROP TABLE "+table); e != nil {
			failOperation(t, "drop cursor fixture", e)
		}
	})
	for i := 1; i <= 2; i++ {
		if _, err = db.ExecContext(testContext(t), "INSERT INTO "+table+" VALUES (?,? ,"+blob+")", i, fmt.Sprintf("v%d", i)); err != nil {
			failOperation(t, "insert cursor fixture", err)
		}
	}
	return db, table
}

func testCursorModes(t *testing.T, p profile) {
	db, table := cursorFixture(t, p)
	session, err := db.Conn(testContext(t))
	if err != nil {
		failOperation(t, "pin cursor session", err)
	}
	defer func() { _ = session.Close() }()
	query := "SELECT ID,V,B FROM " + table + " ORDER BY ID"
	for _, mode := range []oceanbase.CursorMode{oceanbase.CursorForward, oceanbase.CursorStatic} {
		openCtx, cancel := context.WithCancel(testContext(t))
		cursor, e := oceanbase.OpenCursor(openCtx, session, query, oceanbase.CursorOptions{Mode: mode})
		cancel()
		if e != nil {
			failOperation(t, "open cursor mode", e)
		}
		rows, e := cursor.Fetch(testContext(t), oceanbase.CursorPosition{}, 1)
		if e != nil {
			failOperation(t, "fetch cursor after open context ends", e)
		}
		checkCursorRow(t, rows, 1, "v1", true)
		if _, e = cursor.Fetch(testContext(t), oceanbase.CursorPosition{}, 1); !errors.Is(e, oceanbase.ErrCursorBusy) {
			t.Fatal("active cursor batch accepted another operation", e)
		}
		if e = rows.Close(); e != nil {
			failOperation(t, "close cursor batch", e)
		}
		pos := oceanbase.CursorPosition{}
		if mode == oceanbase.CursorStatic {
			pos = oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 2}
		}
		rows, e = cursor.Fetch(testContext(t), pos, 1)
		if e != nil {
			failOperation(t, "fetch second cursor row", e)
		}
		checkCursorRow(t, rows, 2, "v2", true)
		if e = rows.Close(); e != nil {
			failOperation(t, "close second batch", e)
		}
		if e = cursor.Close(); e != nil {
			failOperation(t, "close cursor mode", e)
		}
	}
	sensitive, err := oceanbase.OpenCursor(testContext(t), session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
	if p.mode == oceanbase.ModeMySQL {
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("MySQL sensitive cursor support boundary", err)
		}
		return
	}
	if err != nil {
		failOperation(t, "open Oracle sensitive cursor (requires OceanBase >= 2.2.76)", err)
	}
	defer func() { _ = sensitive.Close() }()
	for _, value := range []string{"first", "changed"} {
		if _, err = db.ExecContext(testContext(t), "UPDATE "+table+" SET V=? WHERE ID=1", value); err != nil {
			failOperation(t, "change sensitive cursor row on another session", err)
		}
		rows, e := sensitive.Fetch(testContext(t), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
		if e != nil {
			failOperation(t, "reposition sensitive cursor", e)
		}
		checkCursorRow(t, rows, 1, value, true)
		if e = rows.Close(); e != nil {
			failOperation(t, "close sensitive batch", e)
		}
	}
}

func checkCursorRow(t *testing.T, rows *sql.Rows, id int, value string, blob bool) {
	t.Helper()
	var gotID int
	var gotValue string
	var gotBlob []byte
	dest := []any{&gotID, &gotValue}
	if blob {
		dest = append(dest, &gotBlob)
	}
	if !rows.Next() {
		t.Fatal("cursor row missing", rows.Err())
	}
	if err := rows.Scan(dest...); err != nil {
		failOperation(t, "scan cursor row", err)
	}
	if gotID != id || gotValue != value || blob && string(gotBlob) != "abc" {
		t.Fatal("cursor row changed", gotID, gotValue, gotBlob)
	}
}

func TestCursorUpdates(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testCursorUpdates(t, p) })
	}
}

func testCursorUpdates(t *testing.T, p profile) {
	db, table := cursorFixture(t, p)
	tr, err := db.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin cursor transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	cursor, err := oceanbase.OpenCursor(testContext(t), tr, "SELECT ID,V FROM "+table, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		failOperation(t, "open updatable cursor", err)
	}
	defer func() { _ = cursor.Close() }()
	key := map[string]any{"ID": 1}
	for _, value := range []string{"v1", "updated"} {
		if err = cursor.Update(testContext(t), key, oceanbase.ColumnChange{Name: "V", Value: value}); err != nil {
			failOperation(t, "update cursor row including ChangedRows no-op", err)
		}
	}
	rows, err := cursor.Refresh(testContext(t), key)
	if err != nil {
		failOperation(t, "refresh cursor row", err)
	}
	checkCursorRow(t, rows, 1, "updated", false)
	if err = rows.Close(); err != nil {
		failOperation(t, "close refresh", err)
	}
	if err = cursor.Insert(testContext(t), oceanbase.ColumnChange{Name: "ID", Value: 3}, oceanbase.ColumnChange{Name: "V", Value: "new"}); err != nil {
		failOperation(t, "insert cursor row", err)
	}
	if err = cursor.Delete(testContext(t), map[string]any{"ID": 2}); err != nil {
		failOperation(t, "delete cursor row", err)
	}
	if err = tr.Rollback(); err != nil {
		failOperation(t, "rollback cursor changes", err)
	}
	if _, err = cursor.Fetch(testContext(t), oceanbase.CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("cursor survived transaction", err)
	}
	tr, err = db.BeginTx(testContext(t), nil)
	if err != nil {
		failOperation(t, "begin cursor rejection transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	if _, err = oceanbase.OpenCursor(testContext(t), tr, "SELECT a.ID FROM "+table+" a JOIN "+table+" b ON a.ID=b.ID", oceanbase.CursorOptions{Updatable: true}); !errors.Is(err, oceanbase.ErrCursor) {
		t.Fatal("joined cursor accepted updates", err)
	}
	cursor, err = oceanbase.OpenCursor(testContext(t), tr, "SELECT ID,V FROM "+table, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		failOperation(t, "open missing-row cursor", err)
	}
	if _, err = db.ExecContext(testContext(t), "DELETE FROM "+table+" WHERE ID=1"); err != nil {
		failOperation(t, "delete cursor row concurrently", err)
	}
	if err = cursor.Update(testContext(t), key, oceanbase.ColumnChange{Name: "V", Value: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing row reported as no-op success", err)
	}
}
