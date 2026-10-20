package session

import (
	"context"
	"fmt"
	"sync"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
)

// Target is a one-shot execution identity. Copies share the same cancellation attempt.
type Target struct{ identity *cancelIdentity }

// TargetInfo is the immutable identity used to authenticate an auxiliary connection.
type TargetInfo struct {
	ConnectionID                   uint32
	Mode                           config.Mode
	Network, Addr, User, ProxyUser string
	TLSVerified                    bool
}

type cancelIdentity struct {
	owner *Session
	info  TargetInfo
}

type cancellation struct {
	mu                      sync.Mutex
	target                  *cancelIdentity
	used, attempted, active bool
	pending                 *CancelAttempt
}

// CancelAttempt has an independent lifetime and never acquires the target protocol gate.
type CancelAttempt struct {
	identity *cancelIdentity
	done     chan struct{}
	cancel   context.CancelFunc
	sent     bool
}

// ArmTarget captures an execution identity for the current authenticated session.
func (op *Operation) ArmTarget() Target {
	s := op.session
	p, cfg := s.protocol, s.cfg
	state := &s.cancellation
	state.mu.Lock()
	defer state.mu.Unlock()
	state.target = &cancelIdentity{owner: s, info: TargetInfo{p.Hello.ConnectionID, p.Mode, cfg.Network, cfg.Addr, p.User, p.ProxyUser, p.TLSVerified}}
	state.used, state.active, state.attempted = false, false, false
	return Target{identity: state.target}
}

// Info returns the target's immutable connection identity when initialized.
func (t Target) Info() (TargetInfo, bool) {
	if t.identity == nil || t.identity.owner == nil {
		return TargetInfo{}, false
	}
	return t.identity.info, true
}

// Begin reserves the active target's single cancellation attempt.
func (t Target) Begin(cancel context.CancelFunc) (*CancelAttempt, error) {
	if _, ok := t.Info(); !ok {
		return nil, fmt.Errorf("%w: invalid cancel target", dberrors.ErrResource)
	}
	id := t.identity
	s := id.owner
	state := &s.cancellation
	pending := &CancelAttempt{identity: id, done: make(chan struct{}), cancel: cancel}
	state.mu.Lock()
	defer state.mu.Unlock()
	if s.closed.Load() || state.target != id || !state.active || state.attempted {
		return nil, fmt.Errorf("%w: target is not an active SQL command or cancellation is pending", dberrors.ErrResource)
	}
	state.pending, state.attempted = pending, true
	return pending, nil
}

// BeforeSend rechecks expiry while holding the independent cancellation mutex.
func (a *CancelAttempt) BeforeSend(ctx context.Context, connectorClosed bool) error {
	s := a.identity.owner
	state := &s.cancellation
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed.Load() || state.target != a.identity || !state.active || connectorClosed {
		return fmt.Errorf("%w: cancel target expired before send", dberrors.ErrResource)
	}
	a.sent = true
	return nil
}

// Finish retires pending work before closing an uncertain target outside the mutex.
func (a *CancelAttempt) Finish(uncertain bool) {
	s := a.identity.owner
	state := &s.cancellation
	state.mu.Lock()
	closeOriginal := uncertain && s.closed.CompareAndSwap(false, true)
	if uncertain {
		state.target, state.active = nil, false
	}
	state.pending = nil
	close(a.done)
	state.mu.Unlock()
	if closeOriginal && s.protocol.Raw != nil {
		_ = s.protocol.Raw.Close()
	}
}

// BeginCancellation activates an unused armed identity for a business command.
func (s *Session) BeginCancellation() bool {
	state := &s.cancellation
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.target == nil || state.used || s.closed.Load() {
		return false
	}
	state.used, state.active = true, true
	return true
}

// BeginCancellation activates the armed identity for the operation's business command.
func (op *Operation) BeginCancellation() bool { return op.session.BeginCancellation() }

// EndCancellation retires the identity before cleanup SQL or gate release.
func (s *Session) EndCancellation(invalidate bool) {
	state := &s.cancellation
	state.mu.Lock()
	state.target, state.active = nil, false
	pending := state.pending
	if pending != nil && (invalidate || !pending.sent) {
		pending.cancel()
	}
	state.mu.Unlock()
	if pending != nil {
		<-pending.done
	}
}
