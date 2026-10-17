package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"sync"
)

// CancelTarget identifies the first ordinary SQL command following QueryTarget.
// Copies share one cancellation attempt; completion, reset and identity changes expire it.
type CancelTarget struct{ identity *cancelIdentity }

type cancelIdentity struct {
	conn          *conn
	id            uint32
	mode          Mode
	network, addr string
	user          string
	proxyUser     string
	tlsVerified   bool
}

// cancelState is independent of the protocol gate, which a running query owns.
type cancelState struct {
	mu        sync.Mutex
	target    *cancelIdentity
	used      bool
	attempted bool
	active    bool
	pending   *cancelAttempt
}

type cancelAttempt struct {
	done   chan struct{}
	cancel context.CancelFunc
	sent   bool
}

// QueryTarget arms a one-shot target before starting SQL on pinned.
// Do not call it while Rows or another operation owns the pinned connection.
func QueryTarget(ctx context.Context, pinned *sql.Conn) (CancelTarget, error) {
	r := &sessionOperationRequest{op: sessionQueryTarget}
	err := runSessionOperation(ctx, pinned, r)
	return r.target, err
}

func (c *conn) queryTargetLocked() CancelTarget {
	s := &c.cancellation
	s.mu.Lock()
	defer s.mu.Unlock()
	s.target = &cancelIdentity{conn: c, id: c.session.hello.ConnectionID, mode: c.session.mode,
		network: c.cfg.Network, addr: c.cfg.Addr, user: c.session.user, proxyUser: c.session.proxyUser, tlsVerified: c.session.tlsVerified}
	s.used, s.active, s.attempted = false, false, false
	return CancelTarget{identity: s.target}
}

// CancelQuery sends KILL QUERY through a new, private connection to the original entry.
// configured must come from NewConnector and authenticate as the target user/tenant.
// A verified TLS target requires verified TLS here too. No discovery or failover runs.
// Success acknowledges KILL only; read the original operation's result independently.
// Server-side ID reuse after a remote disconnect cannot be excluded by this protocol.
func CancelQuery(ctx context.Context, configured driver.Connector, target CancelTarget) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	id := target.identity
	if id == nil || id.conn == nil {
		return fmt.Errorf("%w: invalid cancel target", ErrResource)
	}
	configuredConn, ok := configured.(*connector)
	if !ok || configuredConn == nil {
		return fmt.Errorf("%w: CancelQuery requires this driver's Connector", ErrUnsupported)
	}
	if configuredConn.closed.Load() {
		return ErrConnectorClosed
	}
	cfg := configuredConn.cfg
	if cfg.Network != id.network || cfg.Mode != ModeAuto && cfg.Mode != id.mode {
		return fmt.Errorf("%w: cancel connector network or mode differs", ErrUnsupported)
	}
	if id.tlsVerified && (cfg.TLSConfig == nil || cfg.TLSConfig.InsecureSkipVerify) {
		return fmt.Errorf("%w: cancel connector requires verified TLS", ErrUnsupported)
	}
	return id.conn.cancelQuery(ctx, configuredConn, id)
}

func (c *conn) cancelQuery(ctx context.Context, configuredConn *connector, id *cancelIdentity) (err error) {
	cfg := configuredConn.cfg
	ctx, stop := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer stop()
	s := &c.cancellation
	pending := &cancelAttempt{done: make(chan struct{}), cancel: stop}
	s.mu.Lock()
	if c.closed.Load() || s.target != id || !s.active || s.attempted {
		s.mu.Unlock()
		return fmt.Errorf("%w: target is not an active SQL command or cancellation is pending", ErrResource)
	}
	s.pending = pending
	s.attempted = true
	s.mu.Unlock()
	uncertain := false
	defer func() {
		s.mu.Lock()
		closeOriginal := uncertain && c.closed.CompareAndSwap(false, true)
		if uncertain {
			s.target, s.active = nil, false
		}
		s.pending = nil
		close(pending.done)
		s.mu.Unlock()
		if closeOriginal {
			_ = c.session.raw.Close()
		}
	}()
	cfg.Mode = id.mode
	// Cancellation must not create a database as a side effect of its private login.
	cfg.CreateDatabaseIfMissing = false
	// Synchronous observers could reenter the original connection while it waits for KILL.
	cfg.Logger, cfg.Diagnostics = nil, DiagnosticOptions{}
	auxConnector := &connector{cfg: cfg}
	aux, _, err := auxConnector.dialCandidate(ctx, id.addr)
	if err != nil {
		return err
	}
	defer func() { _ = aux.Close() }()
	if aux.session.user != id.user || aux.session.proxyUser != id.proxyUser || aux.session.mode != id.mode || id.tlsVerified && !aux.session.tlsVerified || aux.session.hello.ConnectionID == id.id {
		return fmt.Errorf("%w: auxiliary connection identity differs or aliases target", ErrResource)
	}
	if err = aux.lock(ctx); err != nil {
		return err
	}
	defer aux.unlock()
	s.mu.Lock()
	if err = ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if c.closed.Load() || s.target != id || !s.active || configuredConn.closed.Load() {
		s.mu.Unlock()
		return fmt.Errorf("%w: cancel target expired before send", ErrResource)
	}
	pending.sent = true
	s.mu.Unlock()
	aux.session.packets.ResetSequence()
	err = aux.session.packets.WriteMessage(ctx, []byte("\x03KILL QUERY "+strconv.FormatUint(uint64(id.id), 10)))
	if err != nil {
		uncertain = !errorsPreSend(err)
		aux.discard()
		return err
	}
	err = aux.readCommandOK(ctx)
	var server *Error
	uncertain = err != nil && !errors.As(err, &server)
	return err
}

func (c *conn) beginCancelExecution() bool {
	s := &c.cancellation
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.target == nil || s.used || c.closed.Load() {
		return false
	}
	s.used, s.active = true, true
	return true
}

// Retire before cleanup SQL or gate release so a late KILL cannot hit the next command.
func (c *conn) endCancelExecution(invalidate bool) {
	s := &c.cancellation
	s.mu.Lock()
	s.target, s.active = nil, false
	pending := s.pending
	if pending != nil && (invalidate || !pending.sent) {
		pending.cancel()
	}
	s.mu.Unlock()
	if pending != nil {
		<-pending.done
	}
}
