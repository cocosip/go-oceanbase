//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const (
	xaOnePhase = "one-phase"
	xaSuspend  = "suspend"
	xaJoin     = "join"
)

func TestXATransactions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			for _, scenario := range []string{xaOnePhase, "two-phase", "rollback", xaSuspend, xaJoin} {
				t.Run(scenario, func(t *testing.T) { testXABranch(t, p, scenario, false) })
			}
		})
	}
}

func TestXARecoveryAfterDisconnect(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testXABranch(t, p, "two-phase", true) })
	}
}

func testXABranch(t *testing.T, p profile, scenario string, disconnect bool) {
	t.Helper()
	db := openProfile(t, p)
	ctx := testContext(t)
	table := fmt.Sprintf("obgo_xa_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		failOperation(t, "create XA table", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(testContext(t), "DROP TABLE "+table); err != nil {
			failOperation(t, "drop XA table", err)
		}
	})
	x := oceanbase.XID{FormatID: 42, GlobalID: make([]byte, 16), BranchID: []byte{0, 255}}
	if _, err := rand.Read(x.GlobalID); err != nil {
		t.Fatal(err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin XA session", err)
	}
	defer func() { _ = pinned.Close() }()
	// Cleanup only this test's prepared branch; never touch another coordinator's XIDs.
	defer func() {
		cleanup := testContext(t)
		c, e := db.Conn(cleanup)
		if e != nil {
			t.Error("cannot open XA recovery cleanup")
			return
		}
		defer func() { _ = c.Close() }()
		xids, e := oceanbase.XARecover(cleanup, c)
		if e != nil {
			failOperation(t, "cleanup XA recover", e)
		}
		for _, got := range xids {
			if sameXID(got, x) {
				if e = oceanbase.XARollback(cleanup, c, x); e != nil {
					failOperation(t, "cleanup XA rollback", e)
				}
			}
		}
	}()
	if err = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); err != nil {
		failOperation(t, "XA start", err)
	}
	if _, err = pinned.BeginTx(ctx, nil); err == nil {
		t.Fatal("local transaction entered XA session")
	}
	if _, err = pinned.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		failOperation(t, "XA insert", err)
	}
	endFlag := oceanbase.XASuccess
	if scenario == xaSuspend {
		endFlag = oceanbase.XASuspend
	}
	if err = oceanbase.XAEnd(ctx, pinned, x, endFlag); err != nil {
		failOperation(t, "XA end", err)
	}
	if scenario == xaSuspend || scenario == xaJoin {
		startFlag := oceanbase.XAResume
		if scenario == xaJoin {
			startFlag = oceanbase.XAJoin
		}
		if err = oceanbase.XAStart(ctx, pinned, x, startFlag); err != nil {
			failOperation(t, "XA reassociate", err)
		}
		if err = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); err != nil {
			failOperation(t, "XA end reassociation", err)
		}
	}
	want := 1
	if scenario == "rollback" {
		want = 0
		err = oceanbase.XARollback(ctx, pinned, x)
	} else {
		if scenario != xaOnePhase {
			var vote oceanbase.XAVote
			vote, err = oceanbase.XAPrepare(ctx, pinned, x)
			if err != nil || vote != oceanbase.XAPrepared {
				t.Fatal("DML branch did not prepare", vote, err)
			}
		}
		if disconnect {
			pinned = recoverXASession(ctx, t, db, pinned, x)
		}
		err = oceanbase.XACommit(ctx, pinned, x, scenario == xaOnePhase)
	}
	if err != nil {
		failOperation(t, "finish XA branch", err)
	}
	if err = pinned.Close(); err != nil {
		failOperation(t, "close XA session", err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
		t.Fatal("XA visibility", count, err)
	}
}

func sameXID(a, b oceanbase.XID) bool {
	return a.FormatID == b.FormatID && bytes.Equal(a.GlobalID, b.GlobalID) && bytes.Equal(a.BranchID, b.BranchID)
}

func recoverXASession(ctx context.Context, t *testing.T, db *sql.DB, previous *sql.Conn, x oceanbase.XID) *sql.Conn {
	t.Helper()
	if err := previous.Close(); err != nil {
		failOperation(t, "close prepared session", err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin recovery session", err)
	}
	xids, err := oceanbase.XARecover(ctx, pinned)
	if err != nil {
		_ = pinned.Close()
		failOperation(t, "recover prepared branch", err)
	}
	for _, got := range xids {
		if sameXID(got, x) {
			return pinned
		}
	}
	_ = pinned.Close()
	t.Fatal("prepared branch missing after disconnect")
	return nil
}
