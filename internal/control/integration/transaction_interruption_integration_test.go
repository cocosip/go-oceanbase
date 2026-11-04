//go:build integration

package integration

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestTransactionInterruption cancels a transaction blocked on another session's
// row lock, then verifies the interrupted transaction's earlier insert is rolled
// back and the pool replaces the evicted session.
func TestTransactionInterruption(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestTransactionInterruption(t, p) })
	}
}

func integrationTestTransactionInterruption(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	connector, err := oceanbase.NewConnector(integrationtest.ProfileConfig(t, p))
	if err != nil {
		integrationtest.FailOperation(t, "configure interruption pool", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(3)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGTI_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create interruption table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop interruption table", err)
		}
	}()
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		integrationtest.FailOperation(t, "seed interruption table", err)
	}
	holder, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin interruption lock holder", err)
	}
	defer func() { _ = holder.Close() }()
	lockTx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin interruption lock", err)
	}
	if _, err = lockTx.ExecContext(ctx, "UPDATE "+table+" SET id=id WHERE id=1"); err != nil {
		integrationtest.FailOperation(t, "lock interruption row", err)
	}
	session, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin interrupted transaction", err)
	}
	defer func() { _ = session.Close() }()
	tx, err := session.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin interrupted transaction", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (2)"); err != nil {
		integrationtest.FailOperation(t, "insert interrupted transaction", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(1500 * time.Millisecond)
		cancel()
	}()
	_, err = tx.ExecContext(runCtx, "UPDATE "+table+" SET id=id WHERE id=1")
	cancel()
	<-done
	if err == nil {
		t.Fatal("blocked transaction update was not cancelled")
	}
	// Roll back before pinging: Go 1.27's closing mutex can deadlock when a
	// discarded transaction connection is pinged while the tx is still open.
	if err = tx.Rollback(); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("interrupted transaction was not evicted", err)
	}
	if err = session.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("interrupted transaction session remained usable", err)
	}
	_ = session.Close()
	if err = lockTx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback interruption lock", err)
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "reborrow interrupted session", err)
	}
	defer func() { _ = fresh.Close() }()
	var count int
	if err = fresh.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatal("interrupted transaction left a partial effect", count, err)
	}
	_ = fresh.Close()
	_ = holder.Close()
	if stats := db.Stats(); stats.InUse != 0 {
		t.Fatal("interruption workload retained a lease", stats)
	}
}
