package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ErrCommitUnknown means the driver cannot establish whether COMMIT took effect.
// The transaction must not be replayed or reported as rolled back.
var ErrCommitUnknown = errors.New("oceanbase: commit outcome unknown")

// ErrTxCleanup means COMMIT or ROLLBACK succeeded but session restoration failed.
var ErrTxCleanup = errors.New("oceanbase: transaction completed but session restoration failed")

type tx struct {
	conn *conn
	ctx  context.Context
	done bool
}

func (c *conn) Begin() (driver.Tx, error) { return c.BeginTx(context.Background(), driver.TxOptions{}) }
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	commands, err := transactionCommands(c.session.mode, opts)
	if err != nil {
		return nil, err
	}
	if err = c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if c.activeTx != nil || c.session.ok.Status&1 != 0 || c.session.ok.Status&2 == 0 {
		return nil, fmt.Errorf("oceanbase: transaction already active or autocommit disabled")
	}
	for _, query := range commands {
		if err = c.commandOK(ctx, append([]byte{3}, query...)); err != nil {
			// Setup may already have changed transaction state; never return this session to the pool.
			c.discard()
			return nil, err
		}
	}
	t := &tx{conn: c, ctx: ctx}
	c.activeTx = t
	return t, nil
}
func transactionCommands(mode Mode, opts driver.TxOptions) ([]string, error) {
	level, err := isolationSQL(opts.Isolation)
	if err != nil {
		return nil, err
	}
	if mode == ModeOracle {
		iso := sql.IsolationLevel(opts.Isolation)
		if iso != sql.LevelDefault && iso != sql.LevelReadCommitted && iso != sql.LevelSerializable || opts.ReadOnly && iso == sql.LevelReadCommitted {
			return nil, fmt.Errorf("%w: Oracle transaction options", ErrUnsupported)
		}
		command := "SET TRANSACTION READ WRITE"
		if opts.ReadOnly {
			command = "SET TRANSACTION READ ONLY"
		} else if level != "" {
			command = "SET TRANSACTION ISOLATION LEVEL " + level
		}
		return []string{"SET autocommit=0", command}, nil
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
func isolationSQL(level driver.IsolationLevel) (string, error) {
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
		return "", fmt.Errorf("%w: transaction isolation", ErrUnsupported)
	}
}
func (t *tx) Commit() error   { return t.finish(true) }
func (t *tx) Rollback() error { return t.finish(false) }
func (t *tx) finish(commit bool) error {
	ctx := t.ctx
	cancel := func() {}
	if !commit {
		ctx, cancel = context.WithTimeout(context.Background(), t.conn.cfg.Limits.CleanupTimeout)
	}
	defer cancel()
	c := t.conn
	if err := c.lock(ctx); err != nil {
		c.discard()
		return err
	}
	defer c.unlock()
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	if c.activeTx != t {
		return fmt.Errorf("oceanbase: transaction does not own session")
	}
	c.activeTx = nil
	if c.closed.Load() {
		return driver.ErrBadConn
	}
	if err := ctx.Err(); err != nil {
		c.discard()
		return err
	}
	command := "ROLLBACK"
	if commit {
		command = "COMMIT"
	}
	if err := c.commandOK(ctx, append([]byte{3}, command...)); err != nil {
		c.discard()
		if commit && err != driver.ErrBadConn && err != context.Canceled && err != context.DeadlineExceeded {
			var server *Error
			if !errors.As(err, &server) {
				return errors.Join(ErrCommitUnknown, err)
			}
		}
		return err
	}
	cleanup, stop := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
	defer stop()
	if err := c.restoreAutocommit(cleanup); err != nil {
		c.discard()
		return errors.Join(ErrTxCleanup, err)
	}
	return nil
}
func (c *conn) restoreAutocommit(ctx context.Context) error {
	if c.session.ok.Status&1 != 0 {
		return wire.ErrMalformed
	}
	if c.session.ok.Status&2 == 0 {
		if err := c.commandOK(ctx, []byte("\x03SET autocommit=1")); err != nil {
			return err
		}
	}
	if c.session.ok.Status&3 != 2 {
		return wire.ErrMalformed
	}
	return nil
}

var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.Tx = (*tx)(nil)
