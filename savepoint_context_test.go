package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestSavepointTxCancellation(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			operations := []savepointOperation{savepointCreate, savepointRollback}
			if mode == ModeMySQL {
				operations = append(operations, savepointRelease)
			}
			for _, operation := range operations {
				t.Run(fmt.Sprintf("%d/%s/%d", mode, framing, operation), func(t *testing.T) {
					testSavepointTxContext(t, mode, framing, operation, false)
				})
			}
		}
	}
}

func TestSavepointTxDeadline(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			testSavepointTxContext(t, mode, testStandard, savepointCreate, true)
		})
	}
}

func testSavepointTxContext(t *testing.T, mode Mode, framing string, operation savepointOperation, deadline bool) {
	t.Helper()
	txctx, cancelTx := context.WithCancel(t.Context())
	if deadline {
		cancelTx()
		txctx, cancelTx = context.WithTimeout(t.Context(), time.Second)
	}
	defer cancelTx()
	start := []txStep{{testMySQLReadWrite, 3}}
	active := byte(3)
	quoted := "`pending`"
	if mode == ModeOracle {
		start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
		active, quoted = 5, "\"pending\""
	}
	verb := "SAVEPOINT "
	switch operation {
	case savepointRollback:
		verb = "ROLLBACK TO SAVEPOINT "
	case savepointRelease:
		verb = "RELEASE SAVEPOINT "
	}
	var sessions atomic.Int32
	closed := make(chan struct{})
	db := batchRewriteDB(t, Config{Mode: mode}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if sessions.Add(1) != 1 {
			t.Error("canceled savepoint opened a second physical connection")
			return
		}
		defer close(closed)
		if !serveTxSteps(ctx, t, p, start) || !serveTxSteps(ctx, t, p, []txStep{{"SAVEPOINT " + quoted, active}}) || !readQuery(ctx, t, p, verb+quoted) {
			return
		}
		if !deadline {
			cancelTx()
		}
		p.ResetSequence()
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("canceled savepoint sent cleanup SQL or reused an unconfirmed response")
		}
	})
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(txctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err = Savepoint(txctx, tx, "pending"); err != nil {
		t.Fatal(err)
	}
	opctx, cancelOp := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelOp()
	switch operation {
	case savepointCreate:
		err = Savepoint(opctx, tx, "pending")
	case savepointRollback:
		err = RollbackTo(opctx, tx, "pending")
	case savepointRelease:
		err = ReleaseSavepoint(opctx, tx, "pending")
	}
	if opctx.Err() != nil || !errors.Is(err, txctx.Err()) || errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("savepoint ignored its transaction context: operation=%v transaction=%v error=%v", opctx.Err(), txctx.Err(), err)
	}
	select {
	case <-closed:
	case <-opctx.Done():
		t.Fatal("canceled savepoint did not close its physical connection")
	}
	if sessions.Load() != 1 {
		t.Fatal("canceled savepoint was replayed")
	}
	if err = tx.Commit(); !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("canceled transaction allowed commit", err)
	}
}
