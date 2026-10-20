package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func resourceEngine(t *testing.T, cleanup time.Duration, serve func(context.Context, *wire.PacketIO)) *Engine {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: testpeer.User, Mode: config.ModeMySQL, Limits: config.Limits{CleanupTimeout: cleanup}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, wire.NewPacketIO(server, cfg.Limits.MaxMessageBytes))
	}()
	e := New(nil, session.New(cfg, &session.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes), Capabilities: wire.ClientProtocol41, Mode: config.ModeMySQL, OBVersion: "4.3.5.1", OK: wire.OK{Status: 2}}))
	t.Cleanup(func() { cancel(); e.Discard(); _ = server.Close(); <-done })
	return e
}

func registerEngineResources(t *testing.T, e *Engine, parentSQL string, count int, publish, closeParent bool) ([]session.ResourceRef, *statement.Statement) {
	t.Helper()
	op, err := e.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer e.release(op)
	var parent *statement.Statement
	if parentSQL != "" {
		parent, err = statement.Prepare(t.Context(), op, parentSQL)
		if err != nil {
			t.Fatal(err)
		}
	}
	refs := make([]session.ResourceRef, count)
	for i := range refs {
		refs[i], err = e.lobs.OwnNative(op, wire.Column{Type: 210}, []byte{'!', 'B', 'O', 'L', 2, 0xa0, 0, 0}, parent)
		if err != nil {
			t.Fatal(err)
		}
		if publish {
			if err = op.Publish(refs[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if parent != nil && closeParent {
		if err = parent.Close(t.Context(), op); err != nil {
			t.Fatal(err)
		}
	}
	return refs, parent
}

func validateEngineResource(t *testing.T, e *Engine, ref session.ResourceRef) error {
	t.Helper()
	op, err := e.acquire(t.Context())
	if err != nil {
		return err
	}
	defer e.release(op)
	return op.Validate(ref)
}

func closeEngineResource(e *Engine, ref session.ResourceRef) error {
	op, err := e.acquire(context.Background())
	if err != nil {
		return err
	}
	defer e.release(op)
	return lifetime.Close(context.Background(), op, &e.cursors, &e.lobs, ref)
}

func resourcePrepare(ctx context.Context, t *testing.T, p *wire.PacketIO, query string, id byte, parameters byte, status byte) bool {
	t.Helper()
	if !testpeer.ReadCommand(ctx, t, p, append([]byte{22}, query...)) {
		return false
	}
	packets := [][]byte{{0, id, 0, 0, 0, 0, 0, parameters, 0, 0, 0, 0}}
	for range parameters {
		packets = append(packets, testpeer.TextColumn("p", 253, 45, 0))
	}
	if parameters > 0 {
		packets = append(packets, []byte{254, 0, 0, status, 0})
	}
	return testpeer.SendPackets(ctx, t, p, packets...)
}

func resourceClose(ctx context.Context, t *testing.T, p *wire.PacketIO, id byte) bool {
	t.Helper()
	return testpeer.ReadCommand(ctx, t, p, []byte{25, id, 0, 0, 0})
}

func TestResourceLease(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !resourcePrepare(ctx, t, p, "SELECT 1", 7, 0, 2) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) || !resourceClose(ctx, t, p, 7) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
	})
	refs, _ := registerEngineResources(t, e, "SELECT 1", 2, true, true)
	if err := closeEngineResource(e, refs[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.Ping(t.Context()); err != nil {
		t.Fatal("first lease closed the statement", err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := closeEngineResource(e, refs[1]); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := e.Ping(t.Context()); err != nil {
		t.Fatal("last lease failed to release statement", err)
	}
	if err := validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("closed handle remained usable", err)
	}
}

func TestResourceGeneration(t *testing.T) {
	for _, finish := range []string{"COMMIT", "ROLLBACK"} {
		t.Run(finish, func(t *testing.T) {
			e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				for i, query := range []string{"START TRANSACTION READ WRITE", finish, "START TRANSACTION READ WRITE", "ROLLBACK"} {
					status := byte(3)
					if i%2 == 1 {
						status = 2
					}
					if !testpeer.ServeOK(ctx, t, p, append([]byte{3}, query...), status) {
						return
					}
				}
			})
			old, _ := registerEngineResources(t, e, "", 1, true, false)
			tx, err := e.BeginTx(t.Context(), driver.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = validateEngineResource(t, e, old[0]); !errors.Is(err, dberrors.ErrResource) {
				t.Fatal("Conn handle crossed into transaction", err)
			}
			refs, _ := registerEngineResources(t, e, "", 1, true, false)
			if err = e.FinishTransaction(tx, finish == "COMMIT"); err != nil {
				t.Fatal(err)
			}
			tx, err = e.BeginTx(t.Context(), driver.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
				t.Fatal("old transaction handle survived new transaction", err)
			}
			if err = closeEngineResource(e, refs[0]); err != nil {
				t.Fatal("close after transaction end", err)
			}
			if err = e.FinishTransaction(tx, false); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("reset", func(t *testing.T) {
		e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
			if !resourcePrepare(ctx, t, p, "SELECT 1", 9, 0, 2) || !resourceClose(ctx, t, p, 9) {
				return
			}
			testpeer.ServeOK(ctx, t, p, []byte{14}, 2)
		})
		refs, _ := registerEngineResources(t, e, "SELECT 1", 1, true, true)
		if err := e.ResetSession(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := e.Ping(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
			t.Fatal("old handle survived pool reset", err)
		}
	})
}

func TestResourceClosePreservesTransaction(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ WRITE"), 3) || !resourcePrepare(ctx, t, p, "SELECT 1", 11, 0, 3) || !resourceClose(ctx, t, p, 11) || !testpeer.ServeOK(ctx, t, p, []byte("\x03UPDATE t SET v=2"), 3) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 2)
	})
	tx, err := e.BeginTx(t.Context(), driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := registerEngineResources(t, e, "SELECT 1", 1, true, true)
	if err = e.CloseResource(t.Context(), refs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Exec(t.Context(), "UPDATE t SET v=2", nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if err = e.FinishTransaction(tx, false); err != nil {
		t.Fatal(err)
	}
}

func TestResourceWrongConnection(t *testing.T) {
	makeEngine := func() *Engine {
		return resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) { testpeer.ServeOK(ctx, t, p, []byte{14}, 2) })
	}
	first, second := makeEngine(), makeEngine()
	refs, _ := registerEngineResources(t, first, "", 1, true, false)
	if err := second.CloseResource(t.Context(), refs[0]); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("wrong physical connection accepted handle", err)
	}
	if err := validateEngineResource(t, first, refs[0]); err != nil {
		t.Fatal("wrong-connection rejection destroyed original", err)
	}
	if err := first.CloseResource(t.Context(), refs[0]); err != nil {
		t.Fatal(err)
	}
	for _, e := range []*Engine{first, second} {
		if err := e.Ping(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResourceUnpublished(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "error"}[failure], func(t *testing.T) {
			e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				if !resourcePrepare(ctx, t, p, "SELECT 1", 15, 0, 2) || !testpeer.ReadQuery(ctx, t, p, "UPDATE t SET v=3") {
					return
				}
				packet := []byte{0, 0, 0, 2, 0, 0, 0}
				if failure {
					packet = []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}
				}
				if testpeer.SendPackets(ctx, t, p, packet) {
					resourceClose(ctx, t, p, 15)
				}
			})
			refs, _ := registerEngineResources(t, e, "SELECT 1", 1, false, true)
			_, err := e.Exec(t.Context(), "UPDATE t SET v=3", nil, Options{})
			if (err != nil) != failure {
				t.Fatal(err)
			}
			if err = validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
				t.Fatal("unpublished handle escaped finalization", err)
			}
		})
	}
}

func TestResourceManualTransaction(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION"), 3) || !resourcePrepare(ctx, t, p, "SELECT 1", 23, 0, 3) || !testpeer.ServeOK(ctx, t, p, []byte("\x03COMMIT"), 2) {
			return
		}
		resourceClose(ctx, t, p, 23)
	})
	if _, err := e.Exec(t.Context(), "START TRANSACTION", nil, Options{}); err != nil {
		t.Fatal(err)
	}
	refs, _ := registerEngineResources(t, e, "SELECT 1", 1, true, true)
	if _, err := e.Exec(t.Context(), "COMMIT", nil, Options{}); err != nil {
		t.Fatal(err)
	}
	if err := validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("observed server transaction end retained locator", err)
	}
}

func TestResourceCleanupTimeout(t *testing.T) {
	e := resourceEngine(t, 25*time.Millisecond, func(ctx context.Context, p *wire.PacketIO) {
		if !resourcePrepare(ctx, t, p, "SELECT 1", 25, 0, 2) {
			return
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("timed out resource cleanup retained physical session")
		}
	})
	refs, _ := registerEngineResources(t, e, "SELECT 1", 1, true, true)
	op, err := e.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	closeErr := e.CloseResource(context.Background(), refs[0])
	e.release(op)
	if !errors.Is(closeErr, context.DeadlineExceeded) || e.Valid() {
		t.Errorf("cleanup timeout must discard: %v valid=%v", closeErr, e.Valid())
	}
}

func TestResourceRetainedStatementClosed(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if resourcePrepare(ctx, t, p, "CALL p(?)", 27, 1, 2) {
			resourceClose(ctx, t, p, 27)
		}
	})
	refs, parent := registerEngineResources(t, e, "CALL p(?)", 1, true, true)
	var out int64
	binding, err := call.NewBinding(call.Output{Dest: &out}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.QueryPrepared(t.Context(), parent, []driver.NamedValue{{Ordinal: 1, Value: binding}}, Options{})
	if err == nil || !strings.Contains(err.Error(), "statement closed") {
		t.Errorf("retained closed statement executed: %v", err)
	}
	if err = e.CloseResource(t.Context(), refs[0]); err != nil {
		t.Fatal(err)
	}
}

func TestResourceLeaseBeforeOwnerClose(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !resourcePrepare(ctx, t, p, "SELECT 1", 29, 0, 2) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		resourceClose(ctx, t, p, 29)
	})
	refs, parent := registerEngineResources(t, e, "SELECT 1", 1, true, false)
	if err := e.CloseResource(t.Context(), refs[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.CloseStatement(parent); err != nil {
		t.Fatal(err)
	}
}

func TestResourceUnpublishedTransactionEnd(t *testing.T) {
	e := resourceEngine(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ WRITE"), 3) || !resourcePrepare(ctx, t, p, "SELECT 1", 31, 0, 3) || !resourceClose(ctx, t, p, 31) {
			return
		}
		testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 2)
	})
	tx, err := e.BeginTx(t.Context(), driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := registerEngineResources(t, e, "SELECT 1", 1, false, true)
	if err = e.FinishTransaction(tx, false); err != nil {
		t.Fatal(err)
	}
	if err = validateEngineResource(t, e, refs[0]); !errors.Is(err, dberrors.ErrResource) {
		t.Fatal("unpublished transaction resource survived end", err)
	}
}

func TestResourceTransactionCleanupBounded(t *testing.T) {
	stalled := make(chan struct{})
	defer close(stalled)
	var bounded atomic.Bool
	e := resourceEngine(t, 25*time.Millisecond, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ WRITE"), 3) || !resourcePrepare(ctx, t, p, "SELECT 1", 33, 0, 3) {
			return
		}
		<-stalled
	})
	tx, err := e.BeginTx(t.Context(), driver.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	registerEngineResources(t, e, "SELECT 1", 1, true, true)
	op, err := e.acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = op.Packets().ReplaceConn(resourceBoundedWriteConn{Conn: op.Protocol().Raw, bounded: &bounded})
	e.release(op)
	if err != nil {
		t.Fatal(err)
	}
	err = e.FinishTransaction(tx, true)
	if err == nil || errors.Is(err, dberrors.ErrCommitUnknown) || !bounded.Load() {
		t.Errorf("unbounded or incorrectly classified pre-commit cleanup: %v bounded=%v", err, bounded.Load())
	}
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
