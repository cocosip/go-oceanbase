// Package session owns the physical connection and its exclusive protocol lease.
package session

import (
	"context"
	"database/sql/driver"
	"sync/atomic"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Session contains physical state; feature managers retain their own concrete resources.
type Session struct {
	gate               chan struct{}
	closed             atomic.Bool
	dirty              atomic.Bool
	cfg                config.Config
	protocol           *Negotiated
	responseActive     bool
	lastUsed           time.Time
	epoch              Epoch
	statementEpoch     uint64
	nextResource       uint64
	resources          map[uint64]ResourceMeta
	cancellation       cancellation
	managedTransaction bool
}

// New adopts a negotiated transport and an already normalized configuration snapshot.
func New(cfg config.Config, protocol *Negotiated) *Session {
	return &Session{
		gate: make(chan struct{}, 1), cfg: cfg, protocol: protocol,
		epoch: Epoch{1, 1, 1}, statementEpoch: 1,
		resources: make(map[uint64]ResourceMeta),
	}
}

// Operation is the sole lease for protocol and gate-protected session state.
type Operation struct {
	session  *Session
	released atomic.Bool
}

// StatementScope is an opaque physical-owner and identity generation token.
type StatementScope struct {
	owner *Session
	epoch uint64
}

// StatementScope identifies the physical owner and current statement generation.
func (op *Operation) StatementScope() StatementScope {
	return StatementScope{owner: op.session, epoch: op.session.statementEpoch}
}

// Acquire waits for protocol ownership, honoring cancellation before and after acquisition.
func (s *Session) Acquire(ctx context.Context) (*Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.gate
			return nil, err
		}
		if s.closed.Load() {
			<-s.gate
			return nil, driver.ErrBadConn
		}
		if p := s.protocol.Packets; p != nil && (s.cfg.Logger != nil || s.cfg.Diagnostics.Request != nil || s.cfg.Diagnostics.Packet != nil) {
			p.EnableDiagnostics(s.cfg.Diagnostics.MaxRequests, s.cfg.Diagnostics.MaxPackets)
		}
		return &Operation{session: s}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Release returns diagnostics for delivery after unlocking. Repeated releases do nothing.
func (op *Operation) Release() ([]wire.RequestDiagnostic, int) {
	if op == nil || !op.released.CompareAndSwap(false, true) {
		return nil, 0
	}
	s := op.session
	var records []wire.RequestDiagnostic
	var dropped int
	if p := s.protocol.Packets; p != nil {
		if s.closed.Load() {
			p.MarkDiagnosticFailure()
		}
		records, dropped = p.TakeDiagnostics()
	}
	s.lastUsed = time.Now()
	<-s.gate
	return records, dropped
}

// Discard closes the transport without waiting for its occupied protocol gate.
func (s *Session) Discard() {
	if s.closed.CompareAndSwap(false, true) {
		s.EndCancellation(true)
		if s.protocol.Raw != nil {
			_ = s.protocol.Raw.Close()
		}
	}
}

// Closed is safe while another operation owns the protocol.
func (s *Session) Closed() bool { return s.closed.Load() }

// Dirty records that this session cannot be returned to its original pool baseline.
func (s *Session) Dirty() bool { return s.dirty.Load() }

// MarkDirty is independent of protocol ownership and never resets the flag.
func (s *Session) MarkDirty() { s.dirty.Store(true) }

// Config returns the immutable configuration snapshot; callers must not mutate its maps.
func (s *Session) Config() config.Config { return s.cfg }

// Config returns the immutable configuration snapshot; callers must not mutate its maps.
func (op *Operation) Config() config.Config { return op.session.cfg }

// SetConfig commits an independently owned configuration after an identity change.
func (op *Operation) SetConfig(cfg config.Config) { op.session.cfg = cfg }

// Discard closes the physical transport without waiting for protocol ownership.
func (op *Operation) Discard() { op.session.Discard() }

// MarkDirty prevents this physical session from returning to its pool baseline.
func (op *Operation) MarkDirty() { op.session.MarkDirty() }

// Dirty reports whether the physical session has left its pool baseline.
func (op *Operation) Dirty() bool { return op.session.Dirty() }

// EndCancellation retires the identity and waits for any pending cancellation attempt.
func (op *Operation) EndCancellation(invalidate bool) { op.session.EndCancellation(invalidate) }

// Closed reports whether the physical session has been discarded.
func (op *Operation) Closed() bool { return op.session.Closed() }

// Protocol returns negotiated transport state by value while the caller owns the operation.
func (op *Operation) Protocol() Negotiated { return *op.session.protocol }

// Packets is available only through the operation that owns the physical protocol.
func (op *Operation) Packets() *wire.PacketIO { return op.session.protocol.Packets }

// LastUsed is the completion time of the preceding operation.
func (op *Operation) LastUsed() time.Time { return op.session.lastUsed }

// SetOK records a completely decoded status response; observation belongs to the caller.
func (op *Operation) SetOK(ok wire.OK) {
	s := op.session
	previous := s.protocol.OK.Status
	ended := previous&1 != 0 && ok.Status&1 == 0
	started := previous&1 == 0 && ok.Status&1 != 0 && !s.managedTransaction
	if ended || started || previous&2 != ok.Status&2 {
		s.epoch.transaction++
	}
	s.protocol.OK = ok
}

// ManagedTransaction records whether transaction boundaries are controlled by the driver.
func (op *Operation) ManagedTransaction(active bool) { op.session.managedTransaction = active }

// SetVersion records the initialization probe result.
func (op *Operation) SetVersion(version string) { op.session.protocol.OBVersion = version }

// SetIdentity commits a successful change-user negotiation.
func (op *Operation) SetIdentity(identity Negotiated) { *op.session.protocol = identity }

// BeginResponse rejects a second command before the current response boundary is complete.
func (op *Operation) BeginResponse() error {
	if err := op.check(); err != nil {
		return err
	}
	if op.session.responseActive {
		return dberrors.ErrUnsupported
	}
	op.session.responseActive = true
	return nil
}

// EndResponse marks a complete boundary without releasing the operation.
func (op *Operation) EndResponse() { op.session.responseActive = false }

// ResponseActive reports whether another ordinary command would cross a response boundary.
func (op *Operation) ResponseActive() bool { return op.session.responseActive }

func (op *Operation) check() error {
	if op == nil || op.released.Load() || op.session.closed.Load() {
		return driver.ErrBadConn
	}
	return nil
}
