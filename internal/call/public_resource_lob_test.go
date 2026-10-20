package call_test

import (
	context "context"
	binary "encoding/binary"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBScalarOutput(t *testing.T) {
	locator := publictest.LobOutFixture(3)
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeLOBCall(ctx, t, p, "BEGIN ? := f(); END;", []wire.Parameter{{Type: 6}}, [][]byte{testpeer.TextColumn("b", 210, 63, 0)}, append([]byte{0, 0, byte(len(locator))}, locator...)) {
			return
		}
		params := []wire.Parameter{publictest.LOBInteger(3), {Type: 210, Data: locator, LengthEncoded: true}, publictest.LOBInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 3)
		row = append(row, 3, 'a', 'b', 'c')
		if !publictest.ServeLOBCall(ctx, t, p, lob.ReadBlobSQL, params, [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}, row) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var value []byte
	if _, err := db.ExecContext(context.Background(), "BEGIN ? := f(); END;", oceanbase.Out{Dest: &value, Type: "BLOB", Length: 3}); err != nil || string(value) != publictest.TestLOBABC {
		t.Fatal(value, err)
	}
}
