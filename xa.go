package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
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
var ErrXA = errors.New("oceanbase: XA error")

// ErrXAOutcomeUnknown requires recovery on a new connection, without replay.
var ErrXAOutcomeUnknown = errors.New("oceanbase: XA outcome unknown; recover on a new connection")

// XAError preserves the standard XA return code and any underlying server error.
// Codes include protocol -6, invalid -5, duplicate XID -8, RM error -3,
// rollback-only 100, deadlock rollback 102, and timeout rollback 106.
type XAError struct {
	Code int32
	Err  error
}

func (e *XAError) Error() string {
	return fmt.Sprintf("oceanbase: XA return code %d: %v", e.Code, e.Err)
}
func (e *XAError) Unwrap() error { return e.Err }

// Is classifies all XA return codes as ErrXA.
func (*XAError) Is(target error) bool { return target == ErrXA }

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

func validXID(x XID) bool {
	return x.FormatID != -1 && len(x.GlobalID) > 0 && len(x.GlobalID) <= 64 && len(x.BranchID) <= 64
}
func cloneXID(x XID) XID {
	x.GlobalID, x.BranchID = append([]byte(nil), x.GlobalID...), append([]byte(nil), x.BranchID...)
	return x
}
func xidKey(x XID) string {
	return strconv.FormatInt(int64(x.FormatID), 10) + ":" + strconv.Itoa(len(x.GlobalID)) + ":" + string(x.GlobalID) + string(x.BranchID)
}
func runXA(ctx context.Context, pinned *sql.Conn, r *xaRequest) error {
	if pinned == nil {
		return fmt.Errorf("%w: a non-nil *sql.Conn is required", ErrXA)
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrXA, err)
	}
	if r.op != xaRecover && !validXID(r.xid) {
		return &XAError{Code: -5, Err: ErrBind}
	}
	r.xid = cloneXID(r.xid)
	_, err := pinned.ExecContext(ctx, "", r)
	if err != nil {
		return errors.Join(ErrXA, err)
	}
	return nil
}

type xaStage uint8

const (
	xaActive xaStage = iota + 1
	xaEnded
	xaSuspended
	xaPrepared
)

type xaBranch struct {
	stage        xaStage
	rollbackOnly bool
}

func (c *conn) validateXALocked(r *xaRequest) error {
	if c.activeTx != nil || !c.xaUsed && (c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0) {
		return &XAError{Code: -9, Err: errors.New("local transaction cannot enter XA")}
	}
	if r.op == xaForget {
		return errors.Join(ErrXA, ErrUnsupported)
	}
	if !validXAFlags(c.session.mode, r) {
		return &XAError{Code: -5, Err: ErrBind}
	}
	if c.cfg.Routing.Role == RoleReadOnly && r.op != xaRecover {
		return errors.Join(ErrXA, ErrUnsupported)
	}
	key := xidKey(r.xid)
	b, known := c.xaBranches[key]
	if !known && (r.op == xaStart || r.op == xaPrepare) && len(c.xaBranches) >= c.cfg.Limits.MaxResultItems {
		return errors.Join(ErrXA, ErrUnsupported)
	}
	return c.validateXAPhase(r, key, b, known)
}

func validXAFlags(mode Mode, r *xaRequest) bool {
	switch r.op {
	case xaStart:
		if r.flags == XANoFlags || r.flags == XAJoin || r.flags == XAResume {
			return true
		}
		return mode == ModeOracle && (r.flags == XAOracleReadOnly || r.flags == XAOracleSerializable || r.flags == XAOracleLoose)
	case xaEnd:
		return r.flags == XASuccess || r.flags == XAFail || r.flags == XASuspend || mode == ModeOracle && (r.flags == XAMigrate || r.flags == XASuspend|XAMigrate)
	default:
		return true
	}
}

func (c *conn) validateXAPhase(r *xaRequest, key string, b xaBranch, known bool) error {
	bad := false
	switch r.op {
	case xaStart:
		bad = c.xaActive != "" || known && (r.flags != XAJoin && r.flags != XAResume || r.flags == XAResume && b.stage != xaSuspended || r.flags == XAJoin && b.stage != xaEnded)
	case xaEnd:
		bad = c.xaActive != key || !known || b.stage != xaActive
	case xaPrepare, xaCommit:
		if known && b.rollbackOnly {
			return &XAError{Code: 100, Err: errors.New("branch is rollback-only")}
		}
		want := xaEnded
		if r.op == xaCommit && !r.onePhase {
			want = xaPrepared
		}
		bad = c.xaActive != "" || known && b.stage != want || r.op == xaCommit && r.onePhase && !known
	case xaRollback:
		bad = c.xaActive != "" || known && b.stage == xaSuspended
	case xaRecover:
		bad = c.xaActive != ""
	}
	if bad {
		return &XAError{Code: -6, Err: errors.New("invalid XA branch phase or association")}
	}
	return nil
}

func (c *conn) execXA(ctx context.Context, r *xaRequest) (driver.Result, error) {
	if r == nil {
		return nil, ErrXA
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if c.responseActive {
		return nil, errResponseActive
	}
	if err := c.validateXALocked(r); err != nil {
		return nil, err
	}
	if c.session.mode == ModeOracle && !supportsPrepareExecute(c.session.obVersion) {
		return nil, errors.Join(ErrXA, ErrUnsupported)
	}
	if err := c.invalidateResourcesLocked(ctx, false); err != nil {
		return nil, err
	}
	c.clearDescriptors()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Even an RM error may have changed association. Never reset or pool this session.
	c.xaUsed, c.sessionDirty = true, true
	var err error
	if c.session.mode == ModeOracle {
		err = c.oracleXALocked(ctx, r)
	} else {
		err = c.mysqlXALocked(ctx, r)
	}
	if err != nil {
		return nil, err
	}
	if c.xaBranches == nil {
		c.xaBranches = make(map[string]xaBranch)
	}
	key := xidKey(r.xid)
	b := c.xaBranches[key]
	switch r.op {
	case xaStart:
		b.stage, c.xaActive = xaActive, key
	case xaEnd:
		b.stage, c.xaActive = xaEnded, ""
		if r.flags == XAMigrate || r.flags&XASuspend != 0 {
			b.stage = xaSuspended
		}
		b.rollbackOnly = b.rollbackOnly || r.flags == XAFail
	case xaPrepare:
		if r.vote == XAReadOnly {
			delete(c.xaBranches, key)
			return result{known: true}, nil
		}
		b.stage = xaPrepared
	case xaCommit, xaRollback:
		delete(c.xaBranches, key)
		return result{known: true}, nil
	case xaRecover:
		return result{known: true}, nil
	}
	c.xaBranches[key] = b
	return result{known: true}, nil
}

func mysqlXID(x XID) string {
	return "X'" + hex.EncodeToString(x.GlobalID) + "',X'" + hex.EncodeToString(x.BranchID) + "',0x" + strconv.FormatUint(uint64(uint32(x.FormatID)), 16)
}

func (c *conn) xaFailure(r *xaRequest, err error, sent bool) error {
	var server *Error
	if errors.As(err, &server) {
		code := int32(-3)
		if c.session.mode == ModeMySQL {
			codes := map[uint32]int32{
				1397: -4, 1398: -5, 1399: -7, 1400: -9, 1401: -3, 1402: 100,
				1440: -8, 1613: 106, 1614: 102,
			}
			if mapped, ok := codes[server.Code]; ok {
				code = mapped
			}
		}
		return &XAError{Code: code, Err: err}
	}
	if !sent {
		return err
	}
	return c.xaUnknown(r, err)
}

func (c *conn) xaUnknown(r *xaRequest, err error) error {
	c.discard()
	err = errors.Join(ErrXA, ErrXAOutcomeUnknown, err)
	if r.op == xaCommit {
		err = errors.Join(ErrCommitUnknown, err)
	}
	return err
}
