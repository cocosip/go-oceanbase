package materialize_test

import (
	context "context"
	binary "encoding/binary"
	errors "errors"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBMaterializationTransactionEndsBetweenChunks(t *testing.T) {
	locator := publictest.LobOutFixture(6)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) ||
			!publictest.ServeLOBQueryStatus(ctx, t, p, "SELECT lob FROM t", 210, 5, locator) {
			return
		}
		params := []wire.Parameter{publictest.LOBInteger(6), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 2)
		row = append(row, 2, 'a', 'b')
		if !publictest.ServeLOBCallStatus(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row, 4) {
			return
		}
		p.ResetSequence()
		if command, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("materialization continued after invalidation: command=%x", command[:min(16, len(command))])
		}
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var value []byte
	err = tx.QueryRowContext(context.Background(), "SELECT lob FROM t").Scan(&value)
	if !errors.Is(err, oceanbase.ErrResource) || value != nil {
		t.Fatalf("stale materialization published: value=%q err=%v", value, err)
	}
	_ = tx.Rollback()
	if db.Stats().Idle != 0 {
		t.Fatal("failed materialization returned its connection to the pool")
	}
}
