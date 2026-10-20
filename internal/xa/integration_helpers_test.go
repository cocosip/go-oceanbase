//go:build integration

package xa_test

import (
	bytes "bytes"
	context "context"
	rand "crypto/rand"
	sql "database/sql"
	errors "errors"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestXABranch(t *testing.T, p integrationtest.Profile, scenario string, disconnect bool) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("obgo_xa_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create XA table", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop XA table", err)
		}
	})
	x := oceanbase.XID{FormatID: 42, GlobalID: make([]byte, 16), BranchID: []byte{0, 255}}
	if _, err := rand.Read(x.GlobalID); err != nil {
		t.Fatal(err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin XA session", err)
	}
	state := "unconfirmed"
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if e := integrationCleanupXABranch(cleanup, db, pinned, x, state); e != nil {
			integrationtest.FailOperation(t, "cleanup own XA branch", e)
		}
	}()
	if err = oceanbase.XAStart(ctx, pinned, x, oceanbase.XANoFlags); err != nil {
		integrationtest.FailOperation(t, "XA start", err)
	}
	state = integrationXaActiveState
	if _, err = pinned.BeginTx(ctx, nil); err == nil {
		t.Fatal("local transaction entered XA session")
	}
	if _, err = pinned.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		integrationtest.FailOperation(t, "XA insert", err)
	}
	endFlag := oceanbase.XASuccess
	if scenario == integrationXaSuspend {
		endFlag = oceanbase.XASuspend
	}
	if err = oceanbase.XAEnd(ctx, pinned, x, endFlag); err != nil {
		integrationFailXAOperation(t, p.Mode, "XA end", err, scenario == integrationXaSuspend)
	}
	state = integrationXaEndedState
	if scenario == integrationXaSuspend {
		state = "suspended"
	}
	if scenario == integrationXaSuspend || scenario == integrationXaJoin {
		startFlag := oceanbase.XAResume
		if scenario == integrationXaJoin {
			startFlag = oceanbase.XAJoin
		}
		if err = oceanbase.XAStart(ctx, pinned, x, startFlag); err != nil {
			integrationFailXAOperation(t, p.Mode, "XA reassociate", err, true)
		}
		state = integrationXaActiveState
		if err = oceanbase.XAEnd(ctx, pinned, x, oceanbase.XASuccess); err != nil {
			integrationtest.FailOperation(t, "XA end reassociation", err)
		}
		state = integrationXaEndedState
	}
	want := 1
	if scenario == "rollback" {
		want = 0
		err = oceanbase.XARollback(ctx, pinned, x)
	} else {
		if scenario != integrationXaOnePhase {
			var vote oceanbase.XAVote
			vote, err = oceanbase.XAPrepare(ctx, pinned, x)
			if err != nil || vote != oceanbase.XAPrepared {
				t.Fatal("DML branch did not prepare", vote, err)
			}
			state = "prepared"
		}
		if disconnect {
			pinned = integrationRecoverXASession(ctx, t, db, pinned, x)
		}
		err = oceanbase.XACommit(ctx, pinned, x, scenario == integrationXaOnePhase)
	}
	if err != nil {
		integrationtest.FailOperation(t, "finish XA branch", err)
	}
	state = integrationXaResolvedState
	if err = pinned.Close(); err != nil {
		integrationtest.FailOperation(t, "close XA session", err)
	}
	pinned = nil
	var count int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != want {
		t.Fatal("XA visibility", count, err)
	}
}

const (
	integrationXaOnePhase      = "one-phase"
	integrationXaSuspend       = "suspend"
	integrationXaJoin          = "join"
	integrationXaActiveState   = "active"
	integrationXaEndedState    = "ended"
	integrationXaResolvedState = "resolved"
)

func integrationFailXAOperation(t *testing.T, mode oceanbase.Mode, operation string, err error, optionalFlag bool) {
	t.Helper()
	if optionalFlag && integrationtest.XAFlagRejected(err, mode) && os.Getenv("OB_TEST_REQUIRE") != "1" {
		t.Skip("server rejected " + operation + " flag (1398); XA suspend/resume or join unavailable")
	}
	integrationtest.FailOperation(t, operation, err)
}

func integrationRecoverXASession(ctx context.Context, t *testing.T, db *sql.DB, previous *sql.Conn, x oceanbase.XID) *sql.Conn {
	t.Helper()
	if err := previous.Close(); err != nil {
		integrationtest.FailOperation(t, "close prepared session", err)
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin recovery session", err)
	}
	xids, err := oceanbase.XARecover(ctx, pinned)
	if err != nil {
		_ = pinned.Close()
		integrationtest.FailOperation(t, "recover prepared branch", err)
	}
	for _, got := range xids {
		if integrationSameXID(got, x) {
			return pinned
		}
	}
	_ = pinned.Close()
	t.Fatal("prepared branch missing after disconnect")
	return nil
}

func integrationSameXID(a, b oceanbase.XID) bool {
	return a.FormatID == b.FormatID && bytes.Equal(a.GlobalID, b.GlobalID) && bytes.Equal(a.BranchID, b.BranchID)
}

func integrationCleanupXABranch(ctx context.Context, db *sql.DB, pinned *sql.Conn, xid oceanbase.XID, state string) (err error) {
	if pinned != nil {
		if state != integrationXaResolvedState {
			if state == "suspended" {
				err = oceanbase.XAStart(ctx, pinned, xid, oceanbase.XAResume)
				if err == nil {
					state = integrationXaActiveState
				}
			}
			if state == integrationXaActiveState && err == nil {
				err = oceanbase.XAEnd(ctx, pinned, xid, oceanbase.XAFail)
				if err == nil {
					state = integrationXaEndedState
				}
			}
			if (state == integrationXaEndedState || state == "prepared") && err == nil {
				err = oceanbase.XARollback(ctx, pinned, xid)
				if err == nil {
					state = integrationXaResolvedState
				}
			}
		}
		err = errors.Join(err, pinned.Close())
	}
	if state == integrationXaResolvedState {
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
		if integrationSameXID(got, xid) {
			return errors.Join(err, oceanbase.XARollback(ctx, c, xid))
		}
	}
	return errors.Join(err, errors.New("own XA branch outcome unconfirmed"))
}
