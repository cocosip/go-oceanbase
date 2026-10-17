package oceanbase

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestSavepointCharset(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, encoding := range []string{testUTF8MB4, localFilenameCharset} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, encoding, framing), func(t *testing.T) {
					testSavepointCharset(t, mode, encoding, framing)
				})
			}
		}
	}
}

func testSavepointCharset(t *testing.T, mode Mode, encoding, framing string) {
	t.Helper()
	start := []txStep{{testMySQLReadWrite, 3}}
	end := []txStep{{testRollbackSQL, 2}}
	active, ready := byte(3), byte(2)
	quoted := "`中\"``;x`"
	if encoding == localFilenameCharset {
		quoted = "`\xd6\xd0\"``;x`"
	}
	if mode == ModeOracle {
		start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
		end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
		active, ready = 5, 6
		quoted = "\"中\"\"`;x\""
		if encoding == localFilenameCharset {
			quoted = "\"\xd6\xd0\"\"`;x\""
		}
	}
	var sessions atomic.Int32
	done := make(chan struct{})
	db := batchRewriteDB(t, Config{Mode: mode, Session: SessionOptions{Charset: encoding}}, framing, func(ctx context.Context, p *wire.PacketIO) {
		if sessions.Add(1) != 1 {
			t.Error("savepoint opened a second physical connection")
			return
		}
		defer close(done)
		if !serveTxSteps(ctx, t, p, start) {
			return
		}
		steps := []txStep{{"SAVEPOINT " + quoted, active}, {"ROLLBACK TO SAVEPOINT " + quoted, active}}
		if mode == ModeMySQL {
			steps = append(steps, txStep{"RELEASE SAVEPOINT " + quoted, active})
		}
		if !serveTxSteps(ctx, t, p, steps) || !serveTxSteps(ctx, t, p, end) || !serveOK(ctx, t, p, []byte{14}, ready) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	name := "中\"`;x"
	if err = Savepoint(ctx, tx, name); err != nil {
		t.Fatal(err)
	}
	if err = RollbackTo(ctx, tx, name); err != nil {
		t.Fatal(err)
	}
	err = ReleaseSavepoint(ctx, tx, name)
	if mode == ModeOracle {
		if !errors.Is(err, ErrUnsupported) {
			t.Fatal("Oracle RELEASE did not reject before sending", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("savepoint changed the session boundary", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if sessions.Load() != 1 || db.Stats().OpenConnections != 0 {
		t.Fatal("savepoint lost its physical connection ownership")
	}
}

func TestSavepointUnrepresentable(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			end := []txStep{{testRollbackSQL, 2}}
			active := byte(3)
			quoted := "`after_rejection`"
			if mode == ModeOracle {
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
				end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
				active = 5
				quoted = "\"after_rejection\""
			}
			db := batchRewriteDB(t, Config{Mode: mode, Session: SessionOptions{Charset: localFilenameCharset}}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, start) || !serveTxSteps(ctx, t, p, []txStep{{"SAVEPOINT " + quoted, active}}) || !serveTxSteps(ctx, t, p, end) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err = Savepoint(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("unrepresentable SAVEPOINT reached the wire", err)
			}
			if err = RollbackTo(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("unrepresentable ROLLBACK TO reached the wire", err)
			}
			if mode == ModeMySQL {
				if err = ReleaseSavepoint(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
					t.Fatal("unrepresentable RELEASE reached the wire", err)
				}
			}
			if err = Savepoint(ctx, tx, "after_rejection"); err != nil {
				t.Fatal("encoding rejection poisoned the transaction", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
