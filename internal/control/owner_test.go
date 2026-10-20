package control

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/connect"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func controlOperation(t *testing.T, cfg config.Config, p *session.Negotiated) (*session.Session, *session.Operation) {
	t.Helper()
	s := session.New(cfg, p)
	op, err := s.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { op.Release() })
	return s, op
}

func controlConnector(cfg config.Config) (*connect.Connector, error) {
	cfg, err := config.Normalize(cfg)
	if err != nil {
		return nil, err
	}
	return connect.New(cfg)
}

func closeControlSession(ctx context.Context, s *session.Session, op *session.Operation) error {
	if s.Closed() {
		return nil
	}
	op.EndCancellation(true)
	op.Packets().ResetSequence()
	err := op.Packets().WriteMessage(ctx, []byte{1})
	s.Discard()
	return err
}

func TestRoleReadOnlyTransaction(t *testing.T) {
	_, op := controlOperation(t, config.Config{Routing: config.RoutingOptions{Role: config.RoleReadOnly}}, &session.Negotiated{Mode: config.ModeMySQL})
	if _, err := New().Begin(context.Background(), op, &cursor.Manager{}, &lob.Manager{}, driver.TxOptions{}, false); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestSessionOperationQuoting(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, status := range []uint16{2, 514} {
			_, op := controlOperation(t, config.Config{}, &session.Negotiated{Mode: mode, OK: wire.OK{Status: status}})
			query, _, _, err := SessionSQL(op, SessionRequest{Kind: SetClientInfo, Info: [3]string{"x\\'y", "", ""}})
			want := "SET @ApplicationName = 'x\\''y', @ClientUser = NULL, @ClientHostname = NULL"
			if mode == config.ModeMySQL && status == 2 {
				want = "SET @ApplicationName = 'x\\\\''y', @ClientUser = NULL, @ClientHostname = NULL"
			}
			if err != nil || query != want {
				t.Fatalf("quote=%q want=%q error=%v", query, want, err)
			}
		}
	}
}

func TestRoutingIdleValidation(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "stale"}[failed], func(t *testing.T) {
			configured, err := controlConnector(config.Config{User: testpeer.User, Routing: config.RoutingOptions{ValidationInterval: time.Minute}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
					return
				}
				ctx = context.Background()
				if !failed {
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, []byte{14}) {
					return
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			s, err := configured.Connect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			op, err := s.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer op.Release()
			m := New()
			resolver, cursors, lobs := &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{}
			// A freshly returned session must not incur a validation round trip.
			if err = m.Reset(context.Background(), op, resolver, cursors, lobs); err != nil {
				t.Fatal(err)
			}
			// A shorter configured interval reaches the same elapsed-idle branch without changing time.
			cfg := op.Config()
			cfg.Routing.ValidationInterval = time.Nanosecond
			op.SetConfig(cfg)
			time.Sleep(20 * time.Millisecond)
			err = m.Reset(context.Background(), op, resolver, cursors, lobs)
			if failed {
				if !errors.Is(err, driver.ErrBadConn) || !s.Closed() {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = closeControlSession(context.Background(), s, op); err != nil {
				t.Fatal(err)
			}
		})
	}
}
