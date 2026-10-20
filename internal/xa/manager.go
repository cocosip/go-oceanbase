// Package xa owns branch association, XA protocol execution and recovery decoding.
package xa

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// XID is the independently owned representation at the internal boundary.
type XID struct {
	FormatID int32
	GlobalID []byte
	BranchID []byte
}

// Operation selects an XA resource-manager command.
type Operation uint8

// XA operations retain the public request ordering.
const (
	Start Operation = iota
	End
	Prepare
	Commit
	Rollback
	Recover
	Forget
)

// XA flags and votes mirror the public scalar contract.
const (
	NoFlags            int32 = 0
	Join               int32 = 0x00200000
	Resume             int32 = 0x08000000
	Success            int32 = 0x04000000
	Fail               int32 = 0x20000000
	Suspend            int32 = 0x02000000
	OracleReadOnly     int32 = 256
	OracleSerializable int32 = 1024
	OracleLoose        int32 = 65536
	Migrate            int32 = 1048576
	Prepared           int32 = 0
	ReadOnly           int32 = 3
)

// Request carries only scalars and owned byte slices across the public adapter.
type Request struct {
	Op       Operation
	XID      XID
	Flags    int32
	OnePhase bool
	Vote     int32
	XIDs     []XID
}

type stage uint8

const (
	active stage = iota + 1
	ended
	suspended
	prepared
)

type branch struct {
	stage        stage
	rollbackOnly bool
}

// Manager is protected by the caller's session operation.
type Manager struct {
	used     bool
	active   string
	branches map[string]branch
}

// Used reports whether any XA command has been attempted.
func (m *Manager) Used() bool { return m.used }

// ValidXID validates the XA branch identifier bounds.
func ValidXID(x XID) bool {
	return x.FormatID != -1 && len(x.GlobalID) > 0 && len(x.GlobalID) <= 64 && len(x.BranchID) <= 64
}

// CloneXID copies both coordinator-owned byte slices.
func CloneXID(x XID) XID {
	x.GlobalID, x.BranchID = append([]byte(nil), x.GlobalID...), append([]byte(nil), x.BranchID...)
	return x
}

func xidKey(x XID) string {
	return strconv.FormatInt(int64(x.FormatID), 10) + ":" + strconv.Itoa(len(x.GlobalID)) + ":" + string(x.GlobalID) + string(x.BranchID)
}

// Validate runs before lifetime cleanup and sends no protocol messages.
func (m *Manager) Validate(op *session.Operation, r *Request, localTx bool) error {
	if r == nil {
		return dberrors.ErrXA
	}
	p, cfg := op.Protocol(), op.Config()
	if localTx || !m.used && (p.OK.Status&1 != 0 || p.OK.Status&2 == 0) {
		return &dberrors.XAError{Code: -9, Err: errors.New("local transaction cannot enter XA")}
	}
	if r.Op == Forget {
		return errors.Join(dberrors.ErrXA, dberrors.ErrUnsupported)
	}
	if r.Op > Forget || r.Op != Recover && !ValidXID(r.XID) || !validFlags(p.Mode, r) {
		return &dberrors.XAError{Code: -5, Err: dberrors.ErrBind}
	}
	if cfg.Routing.Role == config.RoleReadOnly && r.Op != Recover {
		return errors.Join(dberrors.ErrXA, dberrors.ErrUnsupported)
	}
	key := xidKey(r.XID)
	b, known := m.branches[key]
	if !known && (r.Op == Start || r.Op == Prepare) && len(m.branches) >= cfg.Limits.MaxResultItems {
		return errors.Join(dberrors.ErrXA, dberrors.ErrUnsupported)
	}
	return m.validatePhase(r, key, b, known)
}

func validFlags(mode config.Mode, r *Request) bool {
	switch r.Op {
	case Start:
		if r.Flags == NoFlags || r.Flags == Join || r.Flags == Resume {
			return true
		}
		return mode == config.ModeOracle && (r.Flags == OracleReadOnly || r.Flags == OracleSerializable || r.Flags == OracleLoose)
	case End:
		return r.Flags == Success || r.Flags == Fail || r.Flags == Suspend || mode == config.ModeOracle && (r.Flags == Migrate || r.Flags == Suspend|Migrate)
	default:
		return true
	}
}

func (m *Manager) validatePhase(r *Request, key string, b branch, known bool) error {
	bad := false
	switch r.Op {
	case Start:
		bad = m.active != "" || known && (r.Flags != Join && r.Flags != Resume || r.Flags == Resume && b.stage != suspended || r.Flags == Join && b.stage != ended)
	case End:
		bad = m.active != key || !known || b.stage != active
	case Prepare, Commit:
		if known && b.rollbackOnly {
			return &dberrors.XAError{Code: 100, Err: errors.New("branch is rollback-only")}
		}
		want := ended
		if r.Op == Commit && !r.OnePhase {
			want = prepared
		}
		bad = m.active != "" || known && b.stage != want || r.Op == Commit && r.OnePhase && !known
	case Rollback:
		bad = m.active != "" || known && b.stage == suspended
	case Recover:
		bad = m.active != ""
	}
	if bad {
		return &dberrors.XAError{Code: -6, Err: errors.New("invalid XA branch phase or association")}
	}
	return nil
}

// Execute follows successful validation and concrete lifetime cleanup by the runtime.
func (m *Manager) Execute(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, r *Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Even an RM error may have changed association. Never reset or pool this session.
	m.used = true
	op.MarkDirty()
	var err error
	if op.Protocol().Mode == config.ModeOracle {
		err = oracle(ctx, op, resolver, r)
	} else {
		err = mysql(ctx, op, r)
	}
	if err != nil {
		return err
	}
	if m.branches == nil {
		m.branches = make(map[string]branch)
	}
	key := xidKey(r.XID)
	b := m.branches[key]
	switch r.Op {
	case Start:
		b.stage, m.active = active, key
	case End:
		b.stage, m.active = ended, ""
		if r.Flags == Migrate || r.Flags&Suspend != 0 {
			b.stage = suspended
		}
		b.rollbackOnly = b.rollbackOnly || r.Flags == Fail
	case Prepare:
		if r.Vote == ReadOnly {
			delete(m.branches, key)
			return nil
		}
		b.stage = prepared
	case Commit, Rollback:
		delete(m.branches, key)
		return nil
	case Recover:
		return nil
	}
	m.branches[key] = b
	return nil
}

func mysqlXID(x XID) string {
	return "X'" + hex.EncodeToString(x.GlobalID) + "',X'" + hex.EncodeToString(x.BranchID) + "',0x" + strconv.FormatUint(uint64(uint32(x.FormatID)), 16)
}

func failure(op *session.Operation, r *Request, err error, sent bool) error {
	var server *dberrors.Error
	if errors.As(err, &server) {
		code := int32(-3)
		if op.Protocol().Mode == config.ModeMySQL {
			codes := map[uint32]int32{1397: -4, 1398: -5, 1399: -7, 1400: -9, 1401: -3, 1402: 100, 1440: -8, 1613: 106, 1614: 102}
			if mapped, ok := codes[server.Code]; ok {
				code = mapped
			}
		}
		return &dberrors.XAError{Code: code, Err: err}
	}
	if !sent {
		return err
	}
	return unknown(op, r, err)
}

func unknown(op *session.Operation, r *Request, err error) error {
	op.Discard()
	err = errors.Join(dberrors.ErrXA, dberrors.ErrXAOutcomeUnknown, err)
	if r.Op == Commit {
		err = errors.Join(dberrors.ErrCommitUnknown, err)
	}
	return err
}
