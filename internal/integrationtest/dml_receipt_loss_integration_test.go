//go:build integration

package integrationtest_test

import (
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestDMLReceiptLoss drops the OK of an autocommit INSERT and verifies the
// driver reports an error, evicts the session, and leaves the server-applied
// effect visible to an independent session.
func TestDMLReceiptLoss(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestDMLReceiptLoss(t, p) })
	}
}

func integrationTestDMLReceiptLoss(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGDM_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create dml receipt table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop dml receipt table", err)
		}
	}()
	pinned, fault := integrationtest.OpenReceiptLoss(t, p)
	fault.ArmCommand()
	if _, err := pinned.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err == nil {
		t.Fatal("autocommit insert with a lost receipt succeeded")
	}
	if !fault.Acknowledged() {
		t.Fatal("fixture did not consume the insert receipt")
	}
	if err := pinned.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("dml receipt-loss session remained usable", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost insert receipt did not leave the server-applied effect", count, err)
	}
}
