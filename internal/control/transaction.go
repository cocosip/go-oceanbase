package control

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// Transaction is local transaction state protected by the session operation.
type Transaction struct {
	ctx      context.Context
	done     bool
	readOnly bool
}

// Context returns the context that owns the transaction.
func (t *Transaction) Context() context.Context { return t.ctx }

// ReadOnly reports the requested transaction access mode.
func (t *Transaction) ReadOnly() bool { return t.readOnly }

// Done reports whether completion was attempted.
func (t *Transaction) Done() bool { return t.done }

// ActiveTransaction returns the local transaction owning the session.
func (m *Manager) ActiveTransaction() *Transaction { return m.activeTx }

// Begin validates transaction options and expires transaction-bound resources.
func (m *Manager) Begin(ctx context.Context, op *session.Operation, cursors *cursor.Manager, lobs *lob.Manager, opts driver.TxOptions, xaUsed bool) (*Transaction, error) {
	if op.Config().Routing.Role == config.RoleReadOnly && !opts.ReadOnly {
		return nil, fmt.Errorf("%w: read-write transaction on read-only service", dberrors.ErrUnsupported)
	}
	commands, err := TransactionCommands(op.Protocol().Mode, opts)
	if err != nil {
		return nil, err
	}
	if xaUsed {
		return nil, &dberrors.XAError{Code: -9, Err: errors.New("XA session cannot enter a local transaction")}
	}
	if m.activeTx != nil || op.Protocol().OK.Status&1 != 0 || op.Protocol().OK.Status&2 == 0 {
		return nil, fmt.Errorf("oceanbase: transaction already active or autocommit disabled")
	}
	if err = invalidateResources(ctx, op, cursors, lobs, false); err != nil {
		return nil, err
	}
	for _, query := range commands {
		if _, err = command.ExecOK(ctx, op, append([]byte{3}, query...)); err != nil {
			// Setup may already have changed state; this session cannot return to the pool.
			op.Discard()
			return nil, err
		}
	}
	t := &Transaction{ctx: ctx, readOnly: opts.ReadOnly}
	m.activeTx = t
	op.ManagedTransaction(true)
	return t, nil
}

// TransactionCommands validates options and returns mode-specific setup SQL.
func TransactionCommands(mode config.Mode, opts driver.TxOptions) ([]string, error) {
	level, err := IsolationSQL(opts.Isolation)
	if err != nil {
		return nil, err
	}
	if mode == config.ModeOracle {
		iso := sql.IsolationLevel(opts.Isolation)
		if iso != sql.LevelDefault && iso != sql.LevelReadCommitted && iso != sql.LevelSerializable || opts.ReadOnly && iso == sql.LevelReadCommitted {
			return nil, fmt.Errorf("%w: Oracle transaction options", dberrors.ErrUnsupported)
		}
		query := "SET TRANSACTION READ WRITE"
		if opts.ReadOnly {
			query = "SET TRANSACTION READ ONLY"
		} else if level != "" {
			query = "SET TRANSACTION ISOLATION LEVEL " + level
		}
		return []string{"SET autocommit=0", query}, nil
	}
	commands := []string{}
	if level != "" {
		commands = append(commands, "SET TRANSACTION ISOLATION LEVEL "+level)
	}
	access := "READ WRITE"
	if opts.ReadOnly {
		access = "READ ONLY"
	}
	return append(commands, "START TRANSACTION "+access), nil
}

// IsolationSQL validates the supported database/sql isolation levels.
func IsolationSQL(level driver.IsolationLevel) (string, error) {
	switch sql.IsolationLevel(level) {
	case sql.LevelDefault:
		return "", nil
	case sql.LevelReadCommitted:
		return "READ COMMITTED", nil
	case sql.LevelRepeatableRead:
		return "REPEATABLE READ", nil
	case sql.LevelSerializable:
		return "SERIALIZABLE", nil
	default:
		return "", fmt.Errorf("%w: transaction isolation", dberrors.ErrUnsupported)
	}
}

// Finish uses the already acquired operation and the caller's commit or cleanup context.
func (m *Manager) Finish(ctx context.Context, op *session.Operation, cursors *cursor.Manager, lobs *lob.Manager, t *Transaction, commit bool) error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if m.activeTx != t {
		return fmt.Errorf("oceanbase: transaction does not own session")
	}
	defer func() { m.activeTx = nil; op.ManagedTransaction(false) }()
	if op.Closed() {
		return driver.ErrBadConn
	}
	if err := ctx.Err(); err != nil {
		op.Discard()
		return err
	}
	if err := invalidateResources(ctx, op, cursors, lobs, false); err != nil {
		op.Discard()
		return err
	}
	query := "ROLLBACK"
	if commit {
		query = "COMMIT"
	}
	_, err := command.Start(ctx, op, append([]byte{3}, query...), false)
	sent := err == nil || !command.Unsent(err)
	if err == nil {
		_, err = command.ReadOK(ctx, op)
	}
	if err != nil {
		op.Discard()
		if commit && sent {
			var server *dberrors.Error
			if !errors.As(err, &server) {
				return errors.Join(dberrors.ErrCommitUnknown, err)
			}
		}
		return err
	}
	cleanup, stop := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
	defer stop()
	if err := command.RestoreAutocommit(cleanup, op); err != nil {
		op.Discard()
		return errors.Join(dberrors.ErrTxCleanup, err)
	}
	return nil
}

func invalidateResources(ctx context.Context, op *session.Operation, cursors *cursor.Manager, lobs *lob.Manager, reset bool) error {
	if reset {
		op.AdvanceResources()
	} else {
		op.AdvanceTransaction()
	}
	cleanup, cancel := context.WithTimeout(ctx, op.Config().Limits.CleanupTimeout)
	defer cancel()
	return lifetime.Cleanup(cleanup, op, cursors, lobs, true, nil)
}
