package control_test

import (
	context "context"
	driver "database/sql/driver"
	errors "errors"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCommitServerRejection(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		t.Run(map[config.Mode]string{config.ModeMySQL: transactionMySQLLabel, config.ModeOracle: transactionOracleLabel}[mode], func(t *testing.T) {
			start := []txStep{{testMySQLReadWrite, 3}}
			if mode == config.ModeOracle {
				start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
			}
			db := txDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, start) || !testpeer.ReadQuery(ctx, t, p, testCommitSQL) || !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
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
			var server *oceanbase.Error
			if !errors.As(err, &server) || server.Code != 1064 || errors.Is(err, oceanbase.ErrCommitUnknown) || errors.Is(err, driver.ErrBadConn) || db.Stats().OpenConnections != 0 {
				t.Fatalf("complete COMMIT rejection lost server evidence: %v, stats=%+v", err, db.Stats())
			}
		})
	}
}

func TestCommitDeadlineAfterWrite(t *testing.T) {
	testCommitContextAfterWrite(t, true)
}

func TestCommitCanceledAfterWrite(t *testing.T) {
	testCommitContextAfterWrite(t, false)
}
