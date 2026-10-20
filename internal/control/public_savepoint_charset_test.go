package control_test

import (
	context "context"
	errors "errors"
	fmt "fmt"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	charset "github.com/cocosip/go-oceanbase/internal/charset"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestSavepointUnrepresentable(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			end := []txStep{{testRollbackSQL, 2}}
			active := byte(3)
			quoted := "`after_rejection`"
			if mode == config.ModeOracle {
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
				end = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
				active = 5
				quoted = "\"after_rejection\""
			}
			db := batchRewriteDB(t, config.Config{Mode: mode, Session: config.SessionOptions{Charset: localFilenameCharset}}, testStandard, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, start) || !serveTxSteps(ctx, t, p, []txStep{{"SAVEPOINT " + quoted, active}}) || !serveTxSteps(ctx, t, p, end) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err = oceanbase.Savepoint(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("unrepresentable SAVEPOINT reached the wire", err)
			}
			if err = oceanbase.RollbackTo(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
				t.Fatal("unrepresentable ROLLBACK TO reached the wire", err)
			}
			if mode == config.ModeMySQL {
				if err = oceanbase.ReleaseSavepoint(ctx, tx, "emoji_😀"); !errors.Is(err, charset.ErrEncoding) {
					t.Fatal("unrepresentable RELEASE reached the wire", err)
				}
			}
			if err = oceanbase.Savepoint(ctx, tx, "after_rejection"); err != nil {
				t.Fatal("encoding rejection poisoned the transaction", err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSavepointCharset(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, encoding := range []string{testUTF8MB4, localFilenameCharset} {
			for _, framing := range []string{testStandard, testCompression, testOB20} {
				t.Run(fmt.Sprintf("%d/%s/%s", mode, encoding, framing), func(t *testing.T) {
					testSavepointCharset(t, mode, encoding, framing)
				})
			}
		}
	}
}
