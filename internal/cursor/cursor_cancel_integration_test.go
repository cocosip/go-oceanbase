//go:build integration

package cursor_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestCursorCancelOnLockedRow cancels a cursor operation blocked on another
// session's row lock, then verifies the evicted session is replaced.
func TestCursorCancelOnLockedRow(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestCursorCancelOnLockedRow(t, p) })
	}
}

func integrationTestCursorCancelOnLockedRow(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db, table := integrationCursorFixture(t, p)
	ctx := integrationtest.TestContext(t)
	holder, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin cursor lock holder", err)
	}
	defer func() { _ = holder.Close() }()
	tx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin cursor lock", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET V='locked' WHERE ID=1"); err != nil {
		integrationtest.FailOperation(t, "lock cursor row", err)
	}
	session, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin cancelled cursor session", err)
	}
	defer func() { _ = session.Close() }()
	query := "SELECT ID FROM " + table + " WHERE ID=1 FOR UPDATE"
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(1500 * time.Millisecond)
		cancel()
	}()
	cursor, err := oceanbase.OpenCursor(runCtx, session, query, oceanbase.CursorOptions{Mode: oceanbase.CursorForward})
	var rows *sql.Rows
	if err == nil {
		rows, err = cursor.Fetch(runCtx, oceanbase.CursorPosition{}, 1)
	}
	cancel()
	<-done
	if err == nil {
		t.Fatal("cursor operation on a locked row was not cancelled")
	}
	if rows != nil {
		_ = rows.Close()
	}
	if cursor != nil {
		_ = cursor.Close()
	}
	if err = session.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("cancelled cursor session remained usable", err)
	}
	_ = session.Close()
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback cursor lock", err)
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "reborrow cursor session", err)
	}
	defer func() { _ = fresh.Close() }()
	var total int
	if err = fresh.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&total); err != nil || total != 2 {
		t.Fatal("replacement cursor session query", total, err)
	}
}
