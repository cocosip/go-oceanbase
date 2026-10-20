package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/execution"
	coresession "github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
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

type resourceBoundaryConnector struct{ connection *conn }

func (c resourceBoundaryConnector) Connect(context.Context) (driver.Conn, error) {
	return c.connection, nil
}
func (resourceBoundaryConnector) Driver() driver.Driver { return drv{} }

func resourceBoundarySession(t *testing.T, serve func(context.Context, *wire.PacketIO)) (*sql.Conn, *coresession.Session) {
	t.Helper()
	cfg, err := config.Normalize(config.Config{User: testpeer.User, Mode: config.ModeMySQL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, server := net.Pipe()
	physical := coresession.New(cfg, &coresession.Negotiated{Raw: client, Conn: client, Packets: wire.NewPacketIO(client, cfg.Limits.MaxMessageBytes), Capabilities: wire.ClientProtocol41, Mode: config.ModeMySQL, OK: wire.OK{Status: 2}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, wire.NewPacketIO(server, cfg.Limits.MaxMessageBytes))
	}()
	db := sql.OpenDB(resourceBoundaryConnector{connection: &conn{engine: execution.New(nil, physical)}})
	pinned, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pinned.Close()
		_ = db.Close()
		cancel()
		physical.Discard()
		_ = server.Close()
		<-done
	})
	return pinned, physical
}

func resourceBoundaryHandle(t *testing.T, physical *coresession.Session, session Session) *resourceHandle {
	t.Helper()
	op, err := physical.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer op.Release()
	ref, err := op.Register(coresession.LOBResource)
	if err != nil {
		t.Fatal(err)
	}
	if err = op.Publish(ref); err != nil {
		t.Fatal(err)
	}
	return &resourceHandle{session: session, ref: ref}
}

func TestResourceCanceledClose(t *testing.T) {
	pinned, physical := resourceBoundarySession(t, func(ctx context.Context, p *wire.PacketIO) {
		testpeer.ReadCommand(ctx, t, p, []byte{1})
	})
	h := resourceBoundaryHandle(t, physical, pinned)
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
}

func TestResourceCancellationAtDispatch(t *testing.T) {
	pinned, physical := resourceBoundarySession(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03START TRANSACTION READ WRITE"), 3) || !testpeer.ServeOK(ctx, t, p, []byte("\x03ROLLBACK"), 2) {
			return
		}
		testpeer.ReadCommand(ctx, t, p, []byte{1})
	})
	tx, err := pinned.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	h := resourceBoundaryHandle(t, physical, tx)
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
