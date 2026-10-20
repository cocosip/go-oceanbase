package statement_test

import (
	context "context"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOraclePrepareMetadataPublicTransaction(t *testing.T) {
	const query = "SELECT ? FROM dual"
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testAutocommitOff, Status: 4}, {Query: testOracleReadWrite, Status: 5}}) ||
			!publictest.ServePrepareStatus(ctx, t, p, query, 88, 1, nil, 3) ||
			!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 88)) ||
			!publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: testRollbackSQL, Status: 4}, {Query: testAutocommitOn, Status: 6}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s := publictest.PinnedSession(t, db)
	defer func() { _ = s.Close() }()
	tx, err := s.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
}
