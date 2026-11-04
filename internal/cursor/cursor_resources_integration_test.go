//go:build integration

package cursor_test

import (
	sql "database/sql"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestCursorEarlyCloseTransactions verifies that closing a cursor batch before
// draining it keeps the owning transaction usable, and that commit/rollback
// visibility is unchanged by the early close.
func TestCursorEarlyCloseTransactions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestCursorEarlyCloseTransactions(t, p) })
	}
}

func integrationTestCursorEarlyCloseTransactions(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db, table := integrationCursorFixture(t, p)
	ctx := integrationtest.TestContext(t)
	writer, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin cursor writer", err)
	}
	defer func() { _ = writer.Close() }()
	reader, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin cursor reader", err)
	}
	defer func() { _ = reader.Close() }()
	query := "SELECT ID,V FROM " + table + " ORDER BY ID"
	earlyClose := func(session *sql.Conn) {
		t.Helper()
		cursor, err := oceanbase.OpenCursor(ctx, session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorForward})
		if err != nil {
			integrationtest.FailOperation(t, "open cursor", err)
		}
		rows, err := cursor.Fetch(ctx, oceanbase.CursorPosition{}, 1)
		if err != nil {
			integrationtest.FailOperation(t, "fetch cursor", err)
		}
		var id int
		var value string
		if !rows.Next() {
			t.Fatal("cursor row missing", rows.Err())
		}
		if err = rows.Scan(&id, &value); err != nil {
			t.Fatal(err)
		}
		if err = rows.Close(); err != nil {
			integrationtest.FailOperation(t, "close cursor batch", err)
		}
		if err = cursor.Close(); err != nil {
			integrationtest.FailOperation(t, "close cursor early", err)
		}
	}
	count := func(session *sql.Conn, id int) int {
		t.Helper()
		var n int
		if err := session.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=?", id).Scan(&n); err != nil {
			integrationtest.FailOperation(t, "count cursor transaction row", err)
		}
		return n
	}
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin cursor transaction", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" (ID,V) VALUES (?,?)", 3, "v3"); err != nil {
		integrationtest.FailOperation(t, "insert cursor transaction", err)
	}
	earlyClose(writer)
	if err = tx.Commit(); err != nil {
		integrationtest.FailOperation(t, "commit cursor transaction", err)
	}
	if n := count(reader, 3); n != 1 {
		t.Fatal("committed cursor transaction row not visible", n)
	}
	tx, err = writer.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin cursor rollback transaction", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" (ID,V) VALUES (?,?)", 4, "v4"); err != nil {
		integrationtest.FailOperation(t, "insert cursor rollback transaction", err)
	}
	earlyClose(writer)
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback cursor transaction", err)
	}
	if n := count(reader, 4); n != 0 {
		t.Fatal("rolled-back cursor transaction row visible", n)
	}
	earlyClose(reader)
	if err = reader.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "ping after early cursor close", err)
	}
	var total int
	if err = reader.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&total); err != nil || total != 3 {
		t.Fatal("rows after early cursor close", total, err)
	}
}
