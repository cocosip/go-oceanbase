//go:build integration

package xa_test

import (
	"context"
	"crypto/rand"
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

const (
	integrationXACommitReceipt  = "commit"
	integrationXAPrepareReceipt = "prepare"
)

func TestMySQLXAReceiptLoss(t *testing.T) {
	profiles, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if p.Mode != oceanbase.ModeMySQL {
			continue // Oracle XA has a combined PL response, not a single OK.
		}
		for _, phase := range []string{integrationXAPrepareReceipt, integrationXACommitReceipt} {
			t.Run(p.Name+"/"+phase, func(t *testing.T) { integrationXAReceiptLoss(t, p, phase) })
		}
	}
}

func integrationXAReceiptLoss(t *testing.T, p integrationtest.Profile, phase string) {
	t.Helper()
	observer := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBG_XACK_%d", time.Now().UnixNano())
	if _, err := observer.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create XA receipt-loss table", err)
	}
	t.Cleanup(func() {
		if _, err := observer.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop XA receipt-loss table", err)
		}
	})
	pinned, fault := integrationtest.OpenReceiptLoss(t, p)
	xid := oceanbase.XID{FormatID: 42, GlobalID: make([]byte, 16), BranchID: []byte{0, 255}}
	if _, err := rand.Read(xid.GlobalID); err != nil {
		t.Fatal(err)
	}
	// A disconnected unprepared branch rolls back; recover and resolve only our prepared XID.
	t.Cleanup(func() {
		_ = pinned.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := observer.Conn(cleanup)
		if err != nil {
			integrationtest.FailOperation(t, "pin XA receipt cleanup", err)
		}
		defer func() { _ = conn.Close() }()
		if integrationRecoveredXID(t, conn, xid) {
			if err := oceanbase.XARollback(cleanup, conn, xid); err != nil {
				integrationtest.FailOperation(t, "rollback own receipt-loss branch", err)
			}
		}
	})
	if err := oceanbase.XAStart(ctx, pinned, xid, oceanbase.XANoFlags); err != nil {
		integrationtest.FailOperation(t, "start receipt-loss XA", err)
	}
	if _, err := pinned.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		integrationtest.FailOperation(t, "insert receipt-loss XA", err)
	}
	if err := oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XASuccess); err != nil {
		integrationtest.FailOperation(t, "end receipt-loss XA", err)
	}
	var err error
	if phase == integrationXAPrepareReceipt {
		fault.Arm()
		_, err = oceanbase.XAPrepare(ctx, pinned, xid)
	} else {
		if vote, err := oceanbase.XAPrepare(ctx, pinned, xid); err != nil || vote != oceanbase.XAPrepared {
			t.Fatal("receipt-loss XA did not prepare", vote, err)
		}
		fault.Arm()
		err = oceanbase.XACommit(ctx, pinned, xid, false)
	}
	if !fault.Acknowledged() || !errors.Is(err, oceanbase.ErrXA) || !errors.Is(err, oceanbase.ErrXAOutcomeUnknown) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, oceanbase.ErrCommitUnknown) != (phase == integrationXACommitReceipt) {
		t.Fatal("XA receipt loss classification", phase, fault.Acknowledged(), err)
	}
	if err = pinned.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("XA receipt-loss session remained usable", err)
	}
	recovered, err := observer.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin receipt-loss recovery", err)
	}
	defer func() { _ = recovered.Close() }()
	if found := integrationRecoveredXID(t, recovered, xid); found != (phase == integrationXAPrepareReceipt) {
		t.Fatal("independent XA recovery state", phase, found)
	}
	var count int
	want := 1
	if phase == integrationXAPrepareReceipt {
		want = 0
	}
	if err = recovered.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
		t.Fatal("XA effect before explicit recovery", phase, count, err)
	}
	if phase == integrationXAPrepareReceipt {
		if err = oceanbase.XACommit(ctx, recovered, xid, false); err != nil {
			integrationtest.FailOperation(t, "commit independently recovered branch", err)
		}
	}
	if integrationRecoveredXID(t, recovered, xid) {
		t.Fatal("resolved receipt-loss branch remained prepared")
	}
	if err = recovered.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatal("independent XA final effect", count, err)
	}
}

func integrationRecoveredXID(t *testing.T, conn *sql.Conn, xid oceanbase.XID) bool {
	t.Helper()
	xids, err := oceanbase.XARecover(integrationtest.TestContext(t), conn)
	if err != nil {
		integrationtest.FailOperation(t, "recover receipt-loss XA", err)
	}
	for _, got := range xids {
		if integrationSameXID(got, xid) {
			return true
		}
	}
	return false
}
