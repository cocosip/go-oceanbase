package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase"
)

const (
	xaActiveState    = "active"
	xaEndedState     = "ended"
	xaResolvedState  = "resolved"
	xaCleanedState   = "rolled_back_by_cleanup"
	xaSuspendVariant = "suspend"
)

func sameXID(a, b oceanbase.XID) bool {
	return a.FormatID == b.FormatID && bytes.Equal(a.GlobalID, b.GlobalID) && bytes.Equal(a.BranchID, b.BranchID)
}

func runXA(ctx context.Context, s *scenario, variant string) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY)")
	if err != nil {
		return err
	}
	// A returned pinned connection must physically disconnect for the recovery scenario.
	s.db.SetMaxIdleConns(0)
	xid := oceanbase.XID{FormatID: 42, GlobalID: make([]byte, 16), BranchID: []byte{0, 255}}
	if _, err = rand.Read(xid.GlobalID); err != nil {
		return err
	}
	c, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	record := &objectRecord{Kind: "XA", Name: fmt.Sprintf("42:%s:00ff", hex.EncodeToString(xid.GlobalID)), State: "start_unconfirmed"}
	s.objects = append(s.objects, record)
	s.cleanups = append(s.cleanups, cleanupAction{"resolve own XA branch and close session", func(ctx context.Context) error {
		return cleanupOwnedXA(ctx, s.db, &c, xid, record)
	}})
	if err = oceanbase.XAStart(ctx, c, xid, oceanbase.XANoFlags); err != nil {
		return err
	}
	record.State = xaActiveState
	if local, e := c.BeginTx(ctx, nil); e == nil {
		return errors.Join(checkFailure("local transaction entered an XA session"), local.Rollback())
	}
	if _, err = c.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		return err
	}
	if err = endAndReassociateXA(ctx, c, xid, variant, record, s.mode); err != nil {
		return err
	}
	want := 1
	if variant == "rollback" {
		err, want = oceanbase.XARollback(ctx, c, xid), 0
	} else {
		if variant != xaOnePhase {
			vote, e := oceanbase.XAPrepare(ctx, c, xid)
			if e != nil {
				return e
			}
			if err = expect(vote == oceanbase.XAPrepared, "DML XA branch did not prepare"); err != nil {
				return err
			}
			record.State = "prepared"
		}
		if variant == "recovery" {
			if err = c.Close(); err != nil {
				return err
			}
			c = nil
			c, err = s.db.Conn(ctx)
			if err != nil {
				return err
			}
		}
		if variant != xaOnePhase {
			xids, e := oceanbase.XARecover(ctx, c)
			if e != nil {
				return e
			}
			found := false
			for _, got := range xids {
				found = found || sameXID(got, xid)
			}
			if err = expect(found, "own prepared XA branch missing from recovery"); err != nil {
				return err
			}
		}
		err = oceanbase.XACommit(ctx, c, xid, variant == xaOnePhase)
	}
	if err != nil {
		return err
	}
	record.State = xaResolvedState
	if err = c.Close(); err != nil {
		return err
	}
	c = nil
	if err = countRows(ctx, s.db, table, want); err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		xids, e := oceanbase.XARecover(ctx, c)
		if e != nil {
			return e
		}
		for _, got := range xids {
			if sameXID(got, xid) {
				return checkFailure("resolved XA branch remained recoverable")
			}
		}
		return nil
	})
}

func cleanupOwnedXA(ctx context.Context, db *sql.DB, c **sql.Conn, xid oceanbase.XID, record *objectRecord) (err error) {
	if *c != nil {
		if record.State != xaResolvedState {
			err = rollbackOwnedXA(ctx, *c, xid, record.State)
			if err == nil {
				record.State = xaCleanedState
			}
		}
		err = errors.Join(err, (*c).Close())
		*c = nil
	}
	if record.State == xaResolvedState || record.State == xaCleanedState {
		return err
	}
	return errors.Join(err, cleanupXA(ctx, db, xid, record))
}

func rollbackOwnedXA(ctx context.Context, c *sql.Conn, xid oceanbase.XID, state string) error {
	switch state {
	case "suspended":
		if err := oceanbase.XAStart(ctx, c, xid, oceanbase.XAResume); err != nil {
			return err
		}
		fallthrough
	case xaActiveState:
		if err := oceanbase.XAEnd(ctx, c, xid, oceanbase.XAFail); err != nil {
			return err
		}
	case xaEndedState, "prepared":
	default:
		return checkFailure("XA state is unconfirmed; fresh recovery required")
	}
	return oceanbase.XARollback(ctx, c, xid)
}

func cleanupXA(ctx context.Context, db *sql.DB, xid oceanbase.XID, record *objectRecord) error {
	if record.State == xaResolvedState {
		return nil
	}
	return withSession(ctx, db, func(c *sql.Conn) error {
		xids, err := oceanbase.XARecover(ctx, c)
		if err != nil {
			record.State = cleanupFailed
			return err
		}
		for _, got := range xids {
			if !sameXID(got, xid) {
				continue
			}
			if err = oceanbase.XARollback(ctx, c, xid); err != nil {
				record.State = cleanupFailed
				return err
			}
			record.State = xaCleanedState
			return nil
		}
		record.State = "not_recovered_outcome_unconfirmed"
		return checkFailure("own XA branch absent from recovery; inspect the recorded XID before manual cleanup")
	})
}

func endAndReassociateXA(ctx context.Context, c *sql.Conn, xid oceanbase.XID, variant string, record *objectRecord, mode oceanbase.Mode) error {
	var err error
	flag := oceanbase.XASuccess
	if variant == xaSuspendVariant {
		flag = oceanbase.XASuspend
	}
	if err = oceanbase.XAEnd(ctx, c, xid, flag); err != nil {
		if variant == xaSuspendVariant && xaFlagRejected(err, mode) {
			return skipped("server rejected XA END SUSPEND (1398); suspend/resume unavailable")
		}
		return err
	}
	record.State = xaEndedState
	if variant == xaSuspendVariant {
		record.State = "suspended"
	}
	if variant == xaSuspendVariant || variant == "join" {
		flag = oceanbase.XAResume
		if variant == "join" {
			flag = oceanbase.XAJoin
		}
		if err = oceanbase.XAStart(ctx, c, xid, flag); err != nil {
			if xaFlagRejected(err, mode) {
				return skipped("server rejected XA reassociation flag (1398); join/resume unavailable")
			}
			return err
		}
		record.State = xaActiveState
		if err = oceanbase.XAEnd(ctx, c, xid, oceanbase.XASuccess); err != nil {
			return err
		}
		record.State = xaEndedState
	}
	return nil
}
