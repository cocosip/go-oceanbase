//go:build integration

package cursor_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestCursorUpdates(t *testing.T, p integrationtest.Profile, mode oceanbase.CursorMode) {
	db, table := integrationCursorFixture(t, p)
	tr, err := db.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin cursor transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	cursor, err := oceanbase.OpenCursor(integrationtest.TestContext(t), tr, "SELECT ID,V FROM "+table+" ORDER BY ID", oceanbase.CursorOptions{Mode: mode, Updatable: true})
	if err != nil {
		integrationtest.FailOperation(t, "open updatable cursor", err)
	}
	defer func() { _ = cursor.Close() }()
	key := map[string]any{"ID": 1}
	for _, value := range []string{"v1", "updated"} {
		if err = cursor.Update(integrationtest.TestContext(t), key, oceanbase.ColumnChange{Name: "V", Value: value}); err != nil {
			integrationtest.FailOperation(t, "update cursor row including ChangedRows no-op", err)
		}
	}
	rows, err := cursor.Refresh(integrationtest.TestContext(t), key)
	if err != nil {
		integrationtest.FailOperation(t, "refresh cursor row", err)
	}
	integrationCheckCursorRow(t, rows, 1, "updated", false)
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close refresh", err)
	}
	rows, err = cursor.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1)
	if err != nil {
		integrationtest.FailOperation(t, "fetch updated cursor", err)
	}
	want := "v1"
	if mode == oceanbase.CursorSensitive {
		want = "updated"
	}
	integrationCheckCursorRow(t, rows, 1, want, false)
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close updated cursor fetch", err)
	}
	if err = cursor.Insert(integrationtest.TestContext(t), oceanbase.ColumnChange{Name: "ID", Value: 3}, oceanbase.ColumnChange{Name: "V", Value: "new"}); err != nil {
		integrationtest.FailOperation(t, "insert cursor row", err)
	}
	if err = cursor.Delete(integrationtest.TestContext(t), map[string]any{"ID": 2}); err != nil {
		integrationtest.FailOperation(t, "delete cursor row", err)
	}
	if err = tr.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback cursor changes", err)
	}
	if _, err = cursor.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("cursor survived transaction", err)
	}
	tr, err = db.BeginTx(integrationtest.TestContext(t), nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin cursor rejection transaction", err)
	}
	defer func() { _ = tr.Rollback() }()
	if _, err = oceanbase.OpenCursor(integrationtest.TestContext(t), tr, "SELECT a.ID FROM "+table+" a JOIN "+table+" b ON a.ID=b.ID", oceanbase.CursorOptions{Updatable: true}); !errors.Is(err, oceanbase.ErrCursor) {
		t.Fatal("joined cursor accepted updates", err)
	}
	cursor, err = oceanbase.OpenCursor(integrationtest.TestContext(t), tr, "SELECT ID,V FROM "+table, oceanbase.CursorOptions{Mode: mode, Updatable: true})
	if err != nil {
		integrationtest.FailOperation(t, "open missing-row cursor", err)
	}
	if _, err = db.ExecContext(integrationtest.TestContext(t), "DELETE FROM "+table+" WHERE ID=1"); err != nil {
		integrationtest.FailOperation(t, "delete cursor row concurrently", err)
	}
	if err = cursor.Update(integrationtest.TestContext(t), key, oceanbase.ColumnChange{Name: "V", Value: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("missing row reported as no-op success", err)
	}
}

func integrationCursorFixture(t *testing.T, p integrationtest.Profile) (*sql.DB, string) {
	t.Helper()
	cfg := integrationtest.ProfileConfig(t, p)
	cfg.PrepareMode = oceanbase.PrepareServer
	if p.Mode == oceanbase.ModeMySQL {
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
	typ, blob, suffix := integrationIntegrationLongBlobType, "X'616263'", integrationIntegrationInnoDBSuffix
	if p.Mode == oceanbase.ModeOracle {
		typ, blob, suffix = integrationIntegrationBlobType, "TO_BLOB(HEXTORAW('616263'))", ""
	}
	if _, err = db.ExecContext(integrationtest.TestContext(t), "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,V VARCHAR(20),B "+typ+")"+suffix); err != nil {
		integrationtest.FailOperation(t, "create cursor fixture", err)
	}
	t.Cleanup(func() {
		if _, e := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); e != nil {
			integrationtest.FailOperation(t, "drop cursor fixture", e)
		}
	})
	for i := 1; i <= 2; i++ {
		if _, err = db.ExecContext(integrationtest.TestContext(t), "INSERT INTO "+table+" VALUES (?,? ,"+blob+")", i, fmt.Sprintf("v%d", i)); err != nil {
			integrationtest.FailOperation(t, "insert cursor fixture", err)
		}
	}
	return db, table
}

const integrationIntegrationBlobType = "BLOB"

const integrationIntegrationInnoDBSuffix = " ENGINE=InnoDB"

const integrationIntegrationLongBlobType = "LONGBLOB"

func integrationCheckCursorRow(t *testing.T, rows *sql.Rows, id int, value string, blob bool) {
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
		integrationtest.FailOperation(t, "scan cursor row", err)
	}
	if gotID != id || gotValue != value || blob && string(gotBlob) != "abc" {
		t.Fatal("cursor row changed", gotID, gotValue, gotBlob)
	}
}

func integrationTestSensitiveOrdinalProjection(t *testing.T, c *sql.Conn, table string) {
	t.Helper()
	cur, err := oceanbase.OpenCursor(integrationtest.TestContext(t), c, "SELECT /*+ FULL(x) */ ID AS A,ID AS B FROM "+table+" x ORDER BY 2 DESC,1", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
	if err != nil {
		integrationtest.FailOperation(t, "open repeated projection with ordinal ordering", err)
	}
	defer func() { _ = cur.Close() }()
	rows, err := cur.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 2)
	if err != nil {
		integrationtest.FailOperation(t, "fetch repeated projection", err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil || len(columns) != 2 || columns[0] != "A" || columns[1] != "B" {
		t.Fatal("projection aliases changed", columns, err)
	}
	for _, want := range []int{2, 1} {
		var a, b int
		if !rows.Next() || rows.Scan(&a, &b) != nil || a != want || b != want {
			t.Fatal("ordinal ordering or repeated projection changed", a, b, want, rows.Err())
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected repeated-projection tail", rows.Err())
	}
}

func integrationTestCursorModes(t *testing.T, p integrationtest.Profile) {
	db, table := integrationCursorFixture(t, p)
	session, err := db.Conn(integrationtest.TestContext(t))
	if err != nil {
		integrationtest.FailOperation(t, "pin cursor session", err)
	}
	defer func() { _ = session.Close() }()
	query := "SELECT ID,V,B FROM " + table + " ORDER BY ID"
	for _, mode := range []oceanbase.CursorMode{oceanbase.CursorForward, oceanbase.CursorStatic} {
		openCtx, cancel := context.WithCancel(integrationtest.TestContext(t))
		cursor, e := oceanbase.OpenCursor(openCtx, session, query, oceanbase.CursorOptions{Mode: mode})
		cancel()
		if e != nil {
			integrationtest.FailOperation(t, "open cursor mode", e)
		}
		rows, e := cursor.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1)
		if e != nil {
			integrationtest.FailOperation(t, "fetch cursor after open context ends", e)
		}
		integrationCheckCursorRow(t, rows, 1, "v1", true)
		if _, e = cursor.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1); !errors.Is(e, oceanbase.ErrCursorBusy) {
			t.Fatal("active cursor batch accepted another operation", e)
		}
		if e = rows.Close(); e != nil {
			integrationtest.FailOperation(t, "close cursor batch", e)
		}
		pos := oceanbase.CursorPosition{}
		if mode == oceanbase.CursorStatic {
			pos = oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 2}
		}
		rows, e = cursor.Fetch(integrationtest.TestContext(t), pos, 1)
		if e != nil {
			integrationtest.FailOperation(t, "fetch second cursor row", e)
		}
		integrationCheckCursorRow(t, rows, 2, "v2", true)
		if e = rows.Close(); e != nil {
			integrationtest.FailOperation(t, "close second batch", e)
		}
		if e = cursor.Close(); e != nil {
			integrationtest.FailOperation(t, "close cursor mode", e)
		}
	}
	sensitive, err := oceanbase.OpenCursor(integrationtest.TestContext(t), session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
	if p.Mode == oceanbase.ModeMySQL {
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("MySQL sensitive cursor support boundary", err)
		}
		return
	}
	if err != nil {
		integrationtest.FailOperation(t, "open Oracle sensitive cursor (requires OceanBase >= 2.2.76)", err)
	}
	defer func() { _ = sensitive.Close() }()
	for _, value := range []string{"first", "changed"} {
		if _, err = db.ExecContext(integrationtest.TestContext(t), "UPDATE "+table+" SET V=? WHERE ID=1", value); err != nil {
			integrationtest.FailOperation(t, "change sensitive cursor row on another session", err)
		}
		rows, e := sensitive.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
		if e != nil {
			integrationtest.FailOperation(t, "reposition sensitive cursor", e)
		}
		defer func() { _ = rows.Close() }()
		integrationCheckCursorRow(t, rows, 1, value, true)
		if e = rows.Close(); e != nil {
			integrationtest.FailOperation(t, "close sensitive batch", e)
		}
	}
}
