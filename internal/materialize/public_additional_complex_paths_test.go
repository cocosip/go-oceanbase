package materialize_test

import (
	context "context"
	reflect "reflect"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestComplexNestedLOB(t *testing.T) {
	const query = "SELECT v FROM complex_fixture"
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 22, 0, [][]byte{complexColumnFixture(1)}) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(22)) {
			return
		}
		row := []byte{0, 0, 0, 0}
		row = append(row, byte(len(publictest.LobInlineFixture(publictest.TestLOBABC))))
		row = append(row, publictest.LobInlineFixture(publictest.TestLOBABC)...)
		if !testpeer.SendPackets(ctx, t, p, []byte{1}, complexColumnFixture(1), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "CH", Type: "SUB"}}) || !publictest.ServeComplexDictionaryFields(ctx, t, p, "SUB", []oceanbase.Attribute{{Name: "B", Type: valueformat.TypeNameBlob}}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 22)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var v oceanbase.Object
	if err = s.QueryRowContext(ctx).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v.Fields[0].Value.(oceanbase.Object).Fields[0].Value, oceanbase.Raw(publictest.TestLOBABC)) {
		t.Fatal(v)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}
