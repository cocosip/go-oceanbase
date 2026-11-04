//go:build integration

package integration

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestCommitReceiptLoss(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		t.Run(p.Name, func(t *testing.T) {
			observer := integrationtest.OpenProfile(t, p)
			ctx := integrationtest.TestContext(t)
			table := fmt.Sprintf("OBG_ACK_%d", time.Now().UnixNano())
			if _, err := observer.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
				integrationtest.FailOperation(t, "create receipt-loss table", err)
			}
			t.Cleanup(func() {
				if _, err := observer.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
					integrationtest.FailOperation(t, "drop receipt-loss table", err)
				}
			})
			pinned, fault := integrationtest.OpenReceiptLoss(t, p)
			tx, err := pinned.BeginTx(ctx, nil)
			if err != nil {
				integrationtest.FailOperation(t, "begin receipt-loss transaction", err)
			}
			defer func() { _ = tx.Rollback() }()
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
				integrationtest.FailOperation(t, "receipt-loss insert", err)
			}
			var count int
			if err = observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
				t.Fatal("uncommitted row visible", count, err)
			}
			fault.Arm()
			err = tx.Commit()
			if !fault.Acknowledged() || !errors.Is(err, oceanbase.ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) {
				t.Fatal("lost successful COMMIT was not classified as unknown", fault.Acknowledged(), err)
			}
			if err = pinned.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
				t.Fatal("receipt-loss session remained usable", err)
			}
			if err = observer.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
				t.Fatal("independent committed effect missing", count, err)
			}
		})
	}
}
