package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/xa"
)

// XID identifies an XA branch. GlobalID is 1..64 bytes; BranchID is 0..64 bytes.
// FormatID -1 is reserved for the null XID. Calls copy both byte slices.
type XID struct {
	FormatID int32
	GlobalID []byte
	BranchID []byte
}

// XAFlags contains XA start/end flags. Only XASuspend|XAMigrate may be combined.
type XAFlags int32

// Standard XA flags; the last four constants are Oracle compatibility extensions.
const (
	XANoFlags            XAFlags = 0
	XAJoin               XAFlags = 0x00200000
	XAResume             XAFlags = 0x08000000
	XASuccess            XAFlags = 0x04000000
	XAFail               XAFlags = 0x20000000
	XASuspend            XAFlags = 0x02000000
	XAOracleReadOnly     XAFlags = 256
	XAOracleSerializable XAFlags = 1024
	XAOracleLoose        XAFlags = 65536
	XAMigrate            XAFlags = 1048576
)

// XAVote is the resource manager's prepare vote.
type XAVote int32

// Prepare votes defined by XA.
const (
	XAPrepared XAVote = 0
	XAReadOnly XAVote = 3
)

// ErrXA classifies XA failures, including validation errors.
var ErrXA = dberrors.ErrXA

// ErrXAOutcomeUnknown requires recovery on a new connection, without replay.
var ErrXAOutcomeUnknown = dberrors.ErrXAOutcomeUnknown

// XAError preserves the XA return code and underlying error.
type XAError = dberrors.XAError

type xaOperation uint8

const (
	xaStart xaOperation = iota
	xaEnd
	xaPrepare
	xaCommit
	xaRollback
	xaRecover
	xaForget
)

type xaRequest struct {
	op       xaOperation
	xid      XID
	flags    XAFlags
	onePhase bool
	vote     XAVote
	xids     []XID
}

// XAStart associates a branch with a dedicated pinned connection. Do not mix XA
// helpers with sql.Tx, raw transaction SQL, or concurrent session operations.
// Once XA is attempted, closing pinned discards the session from its original pool.
func XAStart(ctx context.Context, pinned *sql.Conn, xid XID, flags XAFlags) error {
	return runXA(ctx, pinned, &xaRequest{op: xaStart, xid: xid, flags: flags})
}

// XAEnd ends or suspends association; XAFail makes the branch rollback-only locally.
func XAEnd(ctx context.Context, pinned *sql.Conn, xid XID, flags XAFlags) error {
	return runXA(ctx, pinned, &xaRequest{op: xaEnd, xid: xid, flags: flags})
}

// XAPrepare prepares an ended branch. MySQL reports XAPrepared on success.
func XAPrepare(ctx context.Context, pinned *sql.Conn, xid XID) (XAVote, error) {
	r := &xaRequest{op: xaPrepare, xid: xid}
	err := runXA(ctx, pinned, r)
	return r.vote, err
}

// XACommit commits an ended branch with onePhase, or a prepared/recovered branch.
// An unknown outcome must not be replayed; recovery belongs to the coordinator.
func XACommit(ctx context.Context, pinned *sql.Conn, xid XID, onePhase bool) error {
	return runXA(ctx, pinned, &xaRequest{op: xaCommit, xid: xid, onePhase: onePhase})
}

// XARollback rolls back an ended, prepared, or recovered branch.
func XARollback(ctx context.Context, pinned *sql.Conn, xid XID) error {
	return runXA(ctx, pinned, &xaRequest{op: xaRollback, xid: xid})
}

// XARecover lists prepared branches. The caller owns all returned slices.
// Limits.MaxResultItems/MaxMessageBytes bound recovery; errors return no partial list.
func XARecover(ctx context.Context, pinned *sql.Conn) ([]XID, error) {
	r := &xaRequest{op: xaRecover}
	err := runXA(ctx, pinned, r)
	return r.xids, err
}

// XAForget returns ErrUnsupported: no server forget contract has been established.
func XAForget(ctx context.Context, pinned *sql.Conn, xid XID) error {
	return runXA(ctx, pinned, &xaRequest{op: xaForget, xid: xid})
}

func runXA(ctx context.Context, pinned *sql.Conn, r *xaRequest) error {
	if pinned == nil {
		return fmt.Errorf("%w: a non-nil *sql.Conn is required", ErrXA)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrXA, err)
	}
	if r.op != xaRecover && !xa.ValidXID(xa.XID{FormatID: r.xid.FormatID, GlobalID: r.xid.GlobalID, BranchID: r.xid.BranchID}) {
		return &XAError{Code: -5, Err: ErrBind}
	}
	r.xid.GlobalID = append([]byte(nil), r.xid.GlobalID...)
	r.xid.BranchID = append([]byte(nil), r.xid.BranchID...)
	_, err := pinned.ExecContext(ctx, "", r)
	if err != nil {
		return errors.Join(ErrXA, err)
	}
	return nil
}
func (c *conn) execXA(ctx context.Context, r *xaRequest) (driver.Result, error) {
	if r == nil {
		return nil, ErrXA
	}
	request := xa.Request{Op: xa.Operation(r.op), XID: xa.XID{FormatID: r.xid.FormatID, GlobalID: r.xid.GlobalID, BranchID: r.xid.BranchID}, Flags: int32(r.flags), OnePhase: r.onePhase}
	if err := c.engine.XA(ctx, &request); err != nil {
		return nil, err
	}
	r.vote = XAVote(request.Vote)
	if request.XIDs != nil {
		r.xids = make([]XID, len(request.XIDs))
		for i, x := range request.XIDs {
			r.xids[i] = XID{FormatID: x.FormatID, GlobalID: x.GlobalID, BranchID: x.BranchID}
		}
	}
	return command.Result{Known: true}, nil
}
