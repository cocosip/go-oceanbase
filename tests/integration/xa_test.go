//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const (
	xaOnePhase      = "one-phase"
	xaSuspend       = "suspend"
	xaJoin          = "join"
	xaActiveState   = "active"
	xaEndedState    = "ended"
	xaResolvedState = "resolved"
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
	state := "unconfirmed"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := cleanupXABranch(cleanup, db, pinned, x, state); e != nil {
			failOperation(t, "cleanup own XA branch", e)
		}
	}()
	if err = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); err != nil {
		failOperation(t, "XA start", err)
	}
	state = xaActiveState
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
		failXAOperation(t, p.mode, "XA end", err, scenario == xaSuspend)
	}
	state = xaEndedState
	if scenario == xaSuspend {
		state = "suspended"
	}
	if scenario == xaSuspend || scenario == xaJoin {
		startFlag := oceanbase.XAResume
		if scenario == xaJoin {
			startFlag = oceanbase.XAJoin
		}
		if err = oceanbase.XAStart(ctx, pinned, x, startFlag); err != nil {
			failXAOperation(t, p.mode, "XA reassociate", err, true)
		}
		state = xaActiveState
		if err = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); err != nil {
			failOperation(t, "XA end reassociation", err)
		}
		state = xaEndedState
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
			state = "prepared"
		}
		if disconnect {
			pinned = recoverXASession(ctx, t, db, pinned, x)
		}
		err = oceanbase.XACommit(ctx, pinned, x, scenario == xaOnePhase)
	}
	if err != nil {
		failOperation(t, "finish XA branch", err)
	}
	state = xaResolvedState
	if err = pinned.Close(); err != nil {
		failOperation(t, "close XA session", err)
	}
	pinned = nil
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
		t.Fatal("XA visibility", count, err)
	}
}

func failXAOperation(t *testing.T, mode oceanbase.Mode, operation string, err error, optionalFlag bool) {
	t.Helper()
	if optionalFlag && xaFlagRejected(err, mode) && os.Getenv("OB_TEST_REQUIRE") != "1" {
		t.Skip("server rejected " + operation + " flag (1398); XA suspend/resume or join unavailable")
	}
	failOperation(t, operation, err)
}

func cleanupXABranch(ctx context.Context, db *sql.DB, pinned *sql.Conn, xid oceanbase.XID, state string) (err error) {
	if pinned != nil {
		if state != xaResolvedState {
			if state == "suspended" {
				err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XAResume)
				if err == nil {
					state = xaActiveState
				}
			}
			if state == xaActiveState && err == nil {
				err = oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XAFail)
				if err == nil {
					state = xaEndedState
				}
			}
			if (state == xaEndedState || state == "prepared") && err == nil {
				err = oceanbase.XARollback(ctx, pinned, xid)
				if err == nil {
					state = xaResolvedState
				}
			}
		}
		err = errors.Join(err, pinned.Close())
	}
	if state == xaResolvedState {
		return err
	}
	// Release the only pool lease before recovering, and touch only our own XID.
	c, e := db.Conn(ctx)
	if e != nil {
		return errors.Join(err, e)
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	xids, e := oceanbase.XARecover(ctx, c)
	if e != nil {
		return errors.Join(err, e)
	}
	for _, got := range xids {
		if sameXID(got, xid) {
			return errors.Join(err, oceanbase.XARollback(ctx, c, xid))
		}
	}
	return errors.Join(err, errors.New("own XA branch outcome unconfirmed"))
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
