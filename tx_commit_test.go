package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type commitBoundaryConn struct {
	net.Conn
	afterWrite func()
}

func (c commitBoundaryConn) Write(data []byte) (int, error) {
	n, err := c.Conn.Write(data)
	if err == nil && n == len(data) && bytes.Equal(data, []byte("\x03COMMIT")) {
		c.afterWrite()
	}
	return n, err
}

func TestCommitCanceledAfterWrite(t *testing.T) {
	testCommitContextAfterWrite(t, false)
}

func TestCommitDeadlineAfterWrite(t *testing.T) {
	testCommitContextAfterWrite(t, true)
}

func testCommitContextAfterWrite(t *testing.T, deadline bool) {
	t.Helper()
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			afterWrite := cancel
			want := context.Canceled
			if deadline {
				afterWrite = func() { <-ctx.Done() }
				want = context.DeadlineExceeded
			}
			var dials, commits atomic.Int32
			closed := make(chan struct{})
			status := byte(2)
			start := []txStep{{testMySQLReadWrite, 3}}
			if mode == ModeOracle {
				status = 6
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
			}
			dial := peerDial(t, func(peerctx context.Context, p *wire.PacketIO) {
				defer close(closed)
				if !serveConnect(peerctx, t, p, status) || !serveBaseline(peerctx, t, p, status) {
					return
				}
				peerctx = context.Background()
				if !serveTxSteps(peerctx, t, p, start) || !readQuery(peerctx, t, p, testCommitSQL) {
					return
				}
				commits.Add(1)
				p.ResetSequence()
				if _, err := p.ReadMessage(context.Background()); err == nil {
					t.Error("unconfirmed commit retained its connection or sent another command")
				}
			})
			connector, err := NewConnector(Config{User: testUser, Mode: mode, DialContext: func(dialctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				raw, err := dial(dialctx, network, addr)
				if err != nil {
					return raw, err
				}
				return commitBoundaryConn{Conn: raw, afterWrite: afterWrite}, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = tx.Commit()
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("unconfirmed commit did not close its physical connection")
			}
			if !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, want) || errors.Is(err, driver.ErrBadConn) {
				t.Fatalf("sent COMMIT cancellation lost outcome uncertainty: %v", err)
			}
			if commits.Load() != 1 || dials.Load() != 1 || db.Stats().OpenConnections != 0 {
				t.Fatalf("commits=%d dials=%d stats=%+v", commits.Load(), dials.Load(), db.Stats())
			}
			if err = tx.Rollback(); !errors.Is(err, sql.ErrTxDone) {
				t.Fatalf("unknown commit allowed rollback: %v", err)
			}
		})
	}
}

func TestCommitServerRejection(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			if mode == ModeOracle {
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, start) || !readQuery(ctx, t, p, testCommitSQL) || !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
					return
				}
				p.ResetSequence()
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("rejected COMMIT retained its connection or sent another command")
				}
			})
			defer func() { _ = db.Close() }()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = tx.Commit()
			var server *Error
			if !errors.As(err, &server) || server.Code != 1064 || errors.Is(err, ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) || db.Stats().OpenConnections != 0 {
				t.Fatalf("complete COMMIT rejection lost server evidence: %v, stats=%+v", err, db.Stats())
			}
		})
	}
}
