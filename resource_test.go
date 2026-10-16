package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type resourceSessionWrapper struct {
	Session
	calls atomic.Int32
}

func (s *resourceSessionWrapper) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	s.calls.Add(1)
	return nil, errors.New("unexpected session call")
}

func TestResourceSession(t *testing.T) {
	var nilConn *sql.Conn
	var nilTx *sql.Tx
	wrapper := &resourceSessionWrapper{}
	for _, session := range []Session{nil, nilConn, nilTx, new(sql.DB), wrapper} {
		h := &resourceHandle{session: session}
		if err := h.close(context.Background()); !errors.Is(err, ErrResource) {
			t.Fatalf("%T: %v", session, err)
		}
	}
	if wrapper.calls.Load() != 0 {
		t.Fatal("wrapper called before concrete session validation")
	}
}

// Raw is test setup only: handles returned here contain no driver connection.
func registerTestResources(t *testing.T, pinned *sql.Conn, session Session, parentSQL string, count int, publish bool) []*resourceHandle {
	t.Helper()
	var handles []*resourceHandle
	err := pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		var parent *stmt
		if parentSQL != "" {
			var err error
			parent, err = c.prepareServer(context.Background(), parentSQL)
			if err != nil {
				return err
			}
		}
		if err := c.lock(context.Background()); err != nil {
			return err
		}
		defer c.unlock()
		for range count {
			h, err := c.registerResourceLocked(session, parent)
			if err != nil {
				return err
			}
			handles = append(handles, h)
			if publish {
				if err = c.publishResourceLocked(h); err != nil {
					return err
				}
			}
		}
		if parent != nil {
			return parent.closeLocked(context.Background())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return handles
}

func checkTestResource(t *testing.T, pinned *sql.Conn, h *resourceHandle) error {
	t.Helper()
	return pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		if err := c.lock(context.Background()); err != nil {
			return err
		}
		defer c.unlock()
		_, err := c.resourceLocked(h)
		return err
	})
}

func TestResourceLease(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT 1", 7, 0, nil) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) || !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	h := registerTestResources(t, pinned, pinned, "SELECT 1", 2, true)
	if err := h[0].close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pinned.PingContext(context.Background()); err != nil {
		t.Fatal("first lease closed the statement", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := h[1].close(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := pinned.PingContext(context.Background()); err != nil {
		t.Fatal("last lease failed to release statement", err)
	}
	if err := checkTestResource(t, pinned, h[0]); !errors.Is(err, ErrResource) {
		t.Fatal("closed handle remained usable", err)
	}
	_ = pinned.Close()
}

func TestResourceGeneration(t *testing.T) {
	for _, finish := range []string{testCommitSQL, testRollbackSQL} {
		t.Run(finish, func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				steps := []txStep{{testMySQLReadWrite, 3}, {finish, 2}, {testMySQLReadWrite, 3}, {testRollbackSQL, 2}}
				if !serveTxSteps(ctx, t, p, steps) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			old := registerTestResources(t, pinned, pinned, "", 1, true)[0]
			tx, err := pinned.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = checkTestResource(t, pinned, old); !errors.Is(err, ErrResource) {
				t.Fatal("Conn handle crossed into transaction", err)
			}
			h := registerTestResources(t, pinned, tx, "", 1, true)[0]
			if finish == testCommitSQL {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			tx, err = pinned.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = checkTestResource(t, pinned, h); !errors.Is(err, ErrResource) {
				t.Fatal("old transaction handle survived new transaction", err)
			}
			if err = h.close(context.Background()); err != nil {
				t.Fatal("close after transaction end", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
	t.Run("reset", func(t *testing.T) {
		db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
			if !servePrepare(ctx, t, p, "SELECT 1", 9, 0, nil) || !readCommand(ctx, t, p, stmtPacket(25, 9)) || !serveOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
		db.SetMaxOpenConns(1)
		defer func() { _ = db.Close() }()
		pinned := pinnedSession(t, db)
		h := registerTestResources(t, pinned, pinned, "SELECT 1", 1, true)[0]
		_ = pinned.Close()
		next := pinnedSession(t, db)
		if err := next.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := checkTestResource(t, next, h); !errors.Is(err, ErrResource) {
			t.Fatal("old handle survived pool reset", err)
		}
		_ = next.Close()
	})
}

func TestResourceClosePreservesTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, "SELECT 1", 11, 0, nil) {
			return
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 11)) || !serveTxSteps(ctx, t, p, []txStep{{"UPDATE t SET v=2", 3}, {testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := registerTestResources(t, pinned, tx, "SELECT 1", 1, true)[0]
	if err = h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(context.Background(), "UPDATE t SET v=2"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestResourceWrongConnection(t *testing.T) {
	makeDB := func() *sql.DB {
		return queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
			if !serveOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
	}
	first, second := makeDB(), makeDB()
	defer func() { _ = first.Close(); _ = second.Close() }()
	a, b := pinnedSession(t, first), pinnedSession(t, second)
	h := registerTestResources(t, a, a, "", 1, true)[0]
	_, err := b.ExecContext(context.Background(), "", resourceRequest{handle: h})
	if !errors.Is(err, ErrResource) {
		t.Fatal("wrong physical connection accepted handle", err)
	}
	if err = checkTestResource(t, a, h); err != nil {
		t.Fatal("wrong-connection rejection destroyed original", err)
	}
	if err = h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, pinned := range []*sql.Conn{a, b} {
		if err = pinned.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
	}
}

func TestResourceUnpublished(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[failure], func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !servePrepare(ctx, t, p, "SELECT 1", 15, 0, nil) || !readQuery(ctx, t, p, "UPDATE t SET v=3") {
					return
				}
				packet := []byte{0, 0, 0, 2, 0, 0, 0}
				if failure {
					packet = []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}
				}
				if !sendPackets(ctx, t, p, packet) || !readCommand(ctx, t, p, stmtPacket(25, 15)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			h := registerTestResources(t, pinned, pinned, "SELECT 1", 1, false)[0]
			_, err := pinned.ExecContext(context.Background(), "UPDATE t SET v=3")
			if (err != nil) != failure {
				t.Fatal(err)
			}
			if err = checkTestResource(t, pinned, h); !errors.Is(err, ErrResource) {
				t.Fatal("unpublished handle escaped finalization", err)
			}
			_ = pinned.Close()
		})
	}
}

func TestResourceCanceledClose(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT 1", 21, 0, nil) || !readCommand(ctx, t, p, stmtPacket(25, 21)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	h := registerTestResources(t, pinned, pinned, "SELECT 1", 1, true)[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := h.close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if h.closed.Load() {
		t.Error("unsent cancellation made handle permanently uncloseable")
	}
	if err := h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestResourceManualTransaction(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{"START TRANSACTION", 3}}) || !servePrepare(ctx, t, p, "SELECT 1", 23, 0, nil) {
			return
		}
		if !readQuery(ctx, t, p, testCommitSQL) || !sendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 23)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	if _, err := pinned.ExecContext(context.Background(), "START TRANSACTION"); err != nil {
		t.Fatal(err)
	}
	h := registerTestResources(t, pinned, pinned, "SELECT 1", 1, true)[0]
	if _, err := pinned.ExecContext(context.Background(), testCommitSQL); err != nil {
		t.Fatal(err)
	}
	if err := checkTestResource(t, pinned, h); !errors.Is(err, ErrResource) {
		t.Fatal("observed server transaction end retained locator", err)
	}
	_ = pinned.Close()
}

func TestResourceCleanupTimeout(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Limits: Limits{CleanupTimeout: 25 * time.Millisecond}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		if !servePrepare(context.Background(), t, p, "SELECT 1", 25, 0, nil) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(context.Background()); err == nil {
			t.Error("timed out resource cleanup retained physical session")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	h := registerTestResources(t, pinned, pinned, "SELECT 1", 1, true)[0]
	err = pinned.Raw(func(dc any) error {
		return dc.(*conn).lock(context.Background())
	})
	if err != nil {
		t.Fatal(err)
	}
	err = h.close(context.Background())
	closeErr := err
	if err = pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		c.unlock()
		if !errors.Is(closeErr, context.DeadlineExceeded) || c.IsValid() {
			t.Errorf("cleanup timeout must discard: %v valid=%v", closeErr, c.IsValid())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestResourceRetainedStatementClosed(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "CALL p(?)", 27, 1, nil) || !readCommand(ctx, t, p, stmtPacket(25, 27)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	h := registerTestResources(t, pinned, pinned, "CALL p(?)", 1, true)[0]
	err := pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		parent := c.resources.records[h.id].parent
		var out int64
		_, err := parent.ExecContext(context.Background(), []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &out}}})
		if err == nil || !strings.Contains(err.Error(), "statement closed") {
			t.Errorf("retained closed statement executed: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestResourceLeaseBeforeOwnerClose(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepare(ctx, t, p, "SELECT 1", 29, 0, nil) || !serveOK(ctx, t, p, []byte{14}, 2) || !readCommand(ctx, t, p, stmtPacket(25, 29)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	var s *stmt
	var h *resourceHandle
	err := pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		var err error
		s, err = c.prepareServer(context.Background(), "SELECT 1")
		if err != nil {
			return err
		}
		if err = c.lock(context.Background()); err != nil {
			return err
		}
		h, err = c.registerResourceLocked(pinned, s)
		if err == nil {
			err = c.publishResourceLocked(h)
		}
		c.unlock()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = pinned.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestResourceUnpublishedTransactionEnd(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, "SELECT 1", 31, 0, nil) || !readCommand(ctx, t, p, stmtPacket(25, 31)) {
			return
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := registerTestResources(t, pinned, tx, "SELECT 1", 1, false)[0]
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = checkTestResource(t, pinned, h); !errors.Is(err, ErrResource) {
		t.Fatal("unpublished transaction resource survived end", err)
	}
	_ = pinned.Close()
}

func TestResourceTransactionCleanupBounded(t *testing.T) {
	stalled := make(chan struct{})
	defer close(stalled)
	var bounded atomic.Bool
	connector, err := NewConnector(Config{User: testUser, Limits: Limits{CleanupTimeout: 25 * time.Millisecond}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, "SELECT 1", 33, 0, nil) {
			return
		}
		// Deliberately stop receiving; the client must bound its close before COMMIT.
		<-stalled
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	registerTestResources(t, pinned, tx, "SELECT 1", 1, true)
	err = pinned.Raw(func(dc any) error {
		c := dc.(*conn)
		// The outer deadline prevents the test hanging when CleanupTimeout is ignored.
		c.session.packets = wire.NewPacketIO(resourceBoundedWriteConn{Conn: c.session.raw, bounded: &bounded}, c.cfg.Limits.MaxMessageBytes)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = tx.Commit()
	if err == nil || errors.Is(err, ErrCommitUnknown) || !bounded.Load() {
		t.Errorf("unbounded or incorrectly classified pre-commit cleanup: %v bounded=%v", err, bounded.Load())
	}
	_ = pinned.Close()
}

type resourceBoundedWriteConn struct {
	net.Conn
	bounded *atomic.Bool
}

func (c resourceBoundedWriteConn) SetDeadline(deadline time.Time) error {
	if !deadline.IsZero() && time.Until(deadline) <= 100*time.Millisecond {
		c.bounded.Store(true)
	}
	outer := time.Now().Add(300 * time.Millisecond)
	if deadline.IsZero() || deadline.After(outer) {
		deadline = outer
	}
	return c.Conn.SetDeadline(deadline)
}

func TestResourceCancellationAtDispatch(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !servePrepare(ctx, t, p, "SELECT 1", 35, 0, nil) || !readCommand(ctx, t, p, stmtPacket(25, 35)) {
			return
		}
		if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := registerTestResources(t, pinned, tx, "SELECT 1", 1, true)[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = h.close(&resourceCancelAtCheckContext{Context: ctx, cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if h.closed.Load() {
		t.Error("database/sql pre-dispatch cancellation prevented resource release")
	}
	if err = h.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

type resourceCancelAtCheckContext struct {
	context.Context
	cancel context.CancelFunc
	once   sync.Once
}

func (c *resourceCancelAtCheckContext) Err() error {
	err := c.Context.Err()
	c.once.Do(c.cancel)
	return err
}
