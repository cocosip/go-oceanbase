package materialize_test

import (
	context "context"
	binary "encoding/binary"
	testing "testing"
	time "time"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBBufferedTransactionContext(t *testing.T) {
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServeLOBQueryStatus(ctx, t, p, "SELECT b FROM t", 210, 5, publictest.LobInlineFixture(publictest.TestLOBABC)) {
			return
		}
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(context.Background(), "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal("buffered rows lost live transaction context", rows.Err())
	}
	var value []byte
	if err = rows.Scan(&value); err != nil || string(value) != publictest.TestLOBABC {
		t.Fatal(value, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestLOBNoReentry(t *testing.T) {
	locator := publictest.LobOutFixture(3)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator) {
			return
		}
		params := []wire.Parameter{publictest.LobIntFixture(3), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LobIntFixture(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !publictest.ServeLOBCall(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "SELECT b FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		t.Fatal(rows.Err())
	}
	var value []byte
	if err = rows.Scan(&value); err != nil || string(value) != publictest.TestLOBABC {
		t.Fatal(value, err)
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal(rows.Err())
	}
}
