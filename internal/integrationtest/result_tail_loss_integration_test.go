//go:build integration

package integrationtest_test

import (
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestResultTailLoss drops the final rowset EOF of a text SELECT and verifies
// that the driver reports an error instead of returning the row and evicts the
// desynchronized session.
func TestResultTailLoss(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestResultTailLoss(t, p) })
	}
}

func integrationTestResultTailLoss(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	pinned, fault := integrationtest.OpenReceiptLoss(t, p)
	ctx := integrationtest.TestContext(t)
	query := "SELECT 1"
	if p.Mode == oceanbase.ModeOracle {
		query += " FROM dual"
	}
	fault.ArmTextResult()
	var value int
	err := pinned.QueryRowContext(ctx, query).Scan(&value)
	if err == nil {
		t.Fatal("result tail loss returned a value")
	}
	if !fault.Acknowledged() {
		t.Fatal("fixture did not consume the rowset tail")
	}
	if err = pinned.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("tail-loss session remained usable", err)
	}
}
