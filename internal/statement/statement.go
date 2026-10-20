// Package statement owns prepared statement state under an explicit session operation.
package statement

import (
	"context"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Statement retains server identity, metadata, type acknowledgements and child leases.
// Its methods require the owning session operation; they never acquire or release its gate.
type Statement struct {
	id                          uint32
	plan                        sqltext.Plan
	parameters                  []wire.Column
	scope                       session.StatementScope
	closed, closeRequested      bool
	leases                      int
	automatic, cacheNoBackslash bool
	lastTypes, pendingTypes     []wire.Parameter
}

// Adopt owns a statement identity received by a specialized command.
func Adopt(op *session.Operation, id uint32, plan sqltext.Plan, parameters []wire.Column) *Statement {
	plan.Slots = slices.Clone(plan.Slots)
	return &Statement{id: id, plan: plan, parameters: slices.Clone(parameters), scope: op.StatementScope()}
}

// Prepare performs server preparation and retains the server-authoritative MySQL slots.
func Prepare(ctx context.Context, op *session.Operation, query string) (*Statement, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, cfg := op.Protocol(), op.Config()
	if int64(utf8.RuneCountInString(query)) >= cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	plan := sqltext.Plan{Query: query, SQL: query, Mode: p.Mode}
	if p.Mode == config.ModeOracle {
		var err error
		plan, err = sqltext.BuildBindPlan(query, p.Mode)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := command.EncodeSQL(plan.SQL, cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, err
	}
	header, parameters, err := command.Prepare(ctx, op, encoded)
	if err != nil {
		return nil, err
	}
	if p.Mode == config.ModeMySQL {
		plan.NumInput = int(header.Parameters)
		plan.Slots = make([]sqltext.Slot, plan.NumInput)
		for i := range plan.Slots {
			plan.Slots[i].Index = i
		}
	} else if int(header.Parameters) != len(plan.Slots) {
		op.Discard()
		return nil, fmt.Errorf("%w: server parameter count", dberrors.ErrBind)
	}
	s := Adopt(op, header.ID, plan, parameters)
	if err = ctx.Err(); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), cfg.Limits.CleanupTimeout)
		_ = s.Close(cleanup, op)
		cancel()
		return nil, err
	}
	return s, nil
}

// PrepareAutomatic creates a cache-owned server statement, optionally deferred to combined execution.
func PrepareAutomatic(ctx context.Context, op *session.Operation, query string, combined bool) (*Statement, error) {
	var s *Statement
	var err error
	if combined {
		var plan sqltext.Plan
		plan, err = sqltext.BuildBindPlan(query, op.Protocol().Mode)
		if err == nil {
			s = Adopt(op, 0, plan, nil)
		}
	} else {
		s, err = Prepare(ctx, op, query)
	}
	if s != nil {
		s.automatic = true
	}
	return s, err
}

// ID is the current physical statement identifier.
func (s *Statement) ID() uint32 { return s.id }

// Plan returns a copy of the parsed bind plan.
func (s *Statement) Plan() sqltext.Plan { p := s.plan; p.Slots = slices.Clone(p.Slots); return p }

// Parameters returns a copy of the current parameter descriptions.
func (s *Statement) Parameters() []wire.Column { return slices.Clone(s.parameters) }

// Automatic reports whether completion should recycle or close this statement.
func (s *Statement) Automatic() bool { return s.automatic }

// CheckOpen rejects a statement after close or a physical-owner/identity change.
func (s *Statement) CheckOpen(op *session.Operation) error {
	if s == nil || s.scope != op.StatementScope() {
		return dberrors.ErrResource
	}
	if s.closed || s.closeRequested {
		return fmt.Errorf("oceanbase: statement closed")
	}
	return nil
}

// AcceptCombined commits validated combined-command metadata into its statement owner.
func (s *Statement) AcceptCombined(op *session.Operation, header wire.PrepareExecuteOK, parameters []wire.Column) error {
	if err := s.CheckOpen(op); err != nil {
		return err
	}
	if header.ID == 0 || s.id != 0 && header.ID != s.id || int(header.Parameters) != len(s.plan.Slots) {
		return wire.ErrMalformed
	}
	s.id, s.parameters = header.ID, slices.Clone(parameters)
	return nil
}

// Close requests closing and defers the physical command while child resources hold a lease.
func (s *Statement) Close(ctx context.Context, op *session.Operation) error {
	if s == nil {
		return nil
	}
	if s.scope != op.StatementScope() {
		s.closed, s.closeRequested = true, true
		return nil
	}
	s.closeRequested = true
	if s.leases != 0 || s.closed {
		return nil
	}
	s.closed = true
	return command.CloseStatement(ctx, op, s.id)
}

// Use is one child-resource lease, released exactly once under a session operation.
type Use struct {
	statement *Statement
	released  bool
}

// AcquireLease retains the statement until a dependent cursor or LOB is closed.
func (s *Statement) AcquireLease(op *session.Operation) (*Use, error) {
	if err := s.CheckOpen(op); err != nil {
		return nil, err
	}
	s.leases++
	return &Use{statement: s}, nil
}

// Release ends a lease and executes a previously requested close when the final child exits.
func (u *Use) Release(ctx context.Context, op *session.Operation) error {
	if u == nil || u.released {
		return nil
	}
	u.released = true
	s := u.statement
	s.leases--
	if s.closeRequested && s.leases == 0 {
		return s.Close(ctx, op)
	}
	return nil
}
