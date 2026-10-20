package oceanbase_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// A canceled streaming response must not close another borrower's in-flight transaction.
func TestPoolCancelIsolation(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, framing := range []string{driverFixtureStandard, driverFixtureCompression, driverFixtureOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
				defer stop()
				queryCtx, cancelQuery := context.WithCancel(ctx)
				defer cancelQuery()
				db, sessions, secondClosed := newPoolCancelDB(ctx, t, mode, framing, cancelQuery)
				defer func() { _ = db.Close() }()
				db.SetMaxOpenConns(2)
				db.SetMaxIdleConns(2)
				first, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = first.Close() }()
				testpeer.Await(t, func() bool { return sessions.Load() == 1 })
				second, err := db.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = second.Close() }()
				var canceled, surviving driverPhysical
				if err = first.Raw(func(raw any) error { canceled = raw.(driverPhysical); return nil }); err != nil {
					t.Fatal(err)
				}
				if err = second.Raw(func(raw any) error { surviving = raw.(driverPhysical); return nil }); err != nil {
					t.Fatal(err)
				}
				runPoolCancelWorkload(ctx, queryCtx, t, first, second)
				if err = first.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
					t.Fatal(err)
				}
				if canceled.IsValid() || canceled == surviving || !surviving.IsValid() || sessions.Load() != 2 {
					t.Fatal("physical cancellation crossed the pool boundary")
				}
				if err = second.PingContext(ctx); err != nil {
					t.Fatal(err)
				}
				if err = second.Raw(func(raw any) error {
					c := raw.(driverPhysical)
					if c != surviving || !c.IsValid() {
						t.Error("surviving physical session was replaced or invalidated")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err = second.Close(); err != nil {
					t.Fatal(err)
				}
				if stats := db.Stats(); stats.OpenConnections != 1 || stats.InUse != 0 || stats.Idle != 1 {
					t.Fatal("cancellation leaked or replaced a pool session", stats)
				}
				if err = db.Close(); err != nil || surviving.IsValid() || db.Stats().OpenConnections != 0 {
					t.Fatal("pool did not close its surviving session", err, db.Stats())
				}
				select {
				case <-secondClosed:
				case <-ctx.Done():
					t.Fatal("surviving peer did not observe physical close")
				}
			})
		}
	}
}

func newPoolCancelDB(ctx context.Context, t *testing.T, mode oceanbase.Mode, framing string, cancelQuery context.CancelFunc) (*sql.DB, *atomic.Int32, <-chan struct{}) {
	t.Helper()
	status, active := byte(2), byte(3)
	begin := []publictest.TxStep{{Query: driverFixtureMySQLReadWrite, Status: active}}
	end := []publictest.TxStep{{Query: driverFixtureCommitSQL, Status: status}}
	probeEnd := []publictest.TxStep{{Query: driverFixtureRollbackSQL, Status: status}}
	if mode == oceanbase.ModeOracle {
		status, active = 6, 5
		begin = []publictest.TxStep{{Query: driverFixtureAutocommitOff, Status: 4}, {Query: driverFixtureOracleReadWrite, Status: active}}
		end = []publictest.TxStep{{Query: driverFixtureCommitSQL, Status: 4}, {Query: driverFixtureAutocommitOn, Status: status}}
		probeEnd = []publictest.TxStep{{Query: driverFixtureRollbackSQL, Status: 4}, {Query: driverFixtureAutocommitOn, Status: status}}
	}
	var sessions atomic.Int32
	firstClosed := make(chan struct{})
	secondClosed := make(chan struct{})
	db := publictest.BatchRewriteDB(t, oceanbase.Config{Mode: mode}, framing, func(_ context.Context, p *wire.PacketIO) {
		switch sessions.Add(1) {
		case 1:
			defer close(firstClosed)
			if !testpeer.ReadQuery(ctx, t, p, sessionStabilitySelect) || !testpeer.SendPackets(ctx, t, p,
				[]byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, status, 0}, []byte{1, '7'}) {
				return
			}
			if _, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				t.Error("canceled response did not close its transport", err)
			}
		case 2:
			defer close(secondClosed)
			if !publictest.ServeTxSteps(ctx, t, p, begin) || !publictest.ServePrepareStatus(ctx, t, p, sessionStabilityUpdate, 77, 1, nil, active) || !testpeer.ReadCommand(ctx, t, p, driverFixtureOptimizationExecute(77, 9, false)) {
				return
			}
			cancelQuery()
			select {
			case <-firstClosed:
			case <-ctx.Done():
				t.Error("canceled peer retained its transport")
				return
			}
			if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, active)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 77)) ||
				!publictest.ServeTxSteps(ctx, t, p, end) || !publictest.ServeTxSteps(ctx, t, p, begin) || !publictest.ServeTxSteps(ctx, t, p, probeEnd) ||
				!testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
				return
			}
			if !testpeer.ReadCommand(ctx, t, p, []byte{1}) {
				return
			}
			if _, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				t.Error("surviving response did not close its transport", err)
			}
		default:
			t.Error("cancellation caused an unexpected replacement session")
		}
	})
	return db, &sessions, secondClosed
}

func runPoolCancelWorkload(ctx, queryCtx context.Context, t *testing.T, first, second *sql.Conn) {
	t.Helper()
	rows, err := first.QueryContext(queryCtx, sessionStabilitySelect)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var value int64
	if !rows.Next() || rows.Scan(&value) != nil || value != 7 {
		t.Fatal("streaming fixture unreadable", value, rows.Err())
	}
	tx, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(ctx, sessionStabilityUpdate)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	result, err := stmt.ExecContext(ctx, int64(9))
	if err != nil {
		t.Fatal("another borrower's cancellation interrupted execution", err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatal("surviving transaction result", n, err)
	}
	if err = stmt.Close(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("another borrower's cancellation interrupted commit", err)
	}
	probe, err := second.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("surviving session retained its completed transaction", err)
	}
	if err = probe.Rollback(); err != nil {
		t.Fatal("surviving session did not restore autocommit", err)
	}
	if rows.Next() || !errors.Is(rows.Err(), context.Canceled) {
		t.Fatal("canceled rows retained their response", rows.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
}
