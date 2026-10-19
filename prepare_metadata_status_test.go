package oceanbase

import (
	"context"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOraclePrepareMetadataPreservesTransaction(t *testing.T) {
	const query = "SELECT ? FROM dual"
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) ||
			!servePrepareStatus(ctx, t, p, query, 88, 1, nil, 3) ||
			!readCommand(ctx, t, p, stmtPacket(25, 88)) ||
			!serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s := pinnedSession(t, db)
	defer func() { _ = s.Close() }()
	var physical *conn
	if err := s.Raw(func(c any) error { physical = c.(*conn); return nil }); err != nil {
		t.Fatal(err)
	}
	tx, err := s.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	key := physical.resourceKeyLocked()
	st, err := tx.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if physical.resourceKeyLocked() != key || physical.session.ok.Status&3 != 1 {
		t.Error("prepare metadata changed the transaction")
	}
}
