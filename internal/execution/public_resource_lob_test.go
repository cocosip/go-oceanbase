package execution_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBOutputThenRows(t *testing.T) {
	locator := publictest.LobInlineFixture(publictest.TestLOBABC)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		command, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 64<<20)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, command) || !testpeer.SendPackets(ctx, t, p,
			publictest.CombinedHeader(41, 1, 1, 4), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0},
			testpeer.TextColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0},
			[]byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, testpeer.TextColumn("b", 210, 63, 0), []byte{254, 0, 0, 6, 0}, append([]byte{0, 0, byte(len(locator))}, locator...), []byte{254, 0, 0, 6, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 41)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var output int64
	rows, err := db.QueryContext(context.Background(), "CALL p(?)", sql.Out{Dest: &output})
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
	if rows.Next() || rows.Err() != nil || output != 42 {
		t.Fatal(output, rows.Err())
	}
}

func TestLOBOpenCardinalityCleanup(t *testing.T) {
	locator := publictest.LobInlineFixture("x")
	locator[5] |= 1
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBQuery(ctx, t, p, "SELECT b FROM t", 210, locator, locator) {
			return
		}
		for i := 0; i < 2; i++ {
			params := []wire.Parameter{{Type: 210, Data: locator, LengthEncoded: true}, {Type: 6}}
			if !publictest.ServeLOBCall(ctx, t, p, publictest.LOBFreeSQL, params, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, []byte{0, 4}) {
				return
			}
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	if b, err := oceanbase.OpenBlob(context.Background(), pinned, "SELECT b FROM t"); b != nil || !errors.Is(err, oceanbase.ErrLOB) {
		t.Fatal(b, err)
	}
	_ = pinned.Close()
}
