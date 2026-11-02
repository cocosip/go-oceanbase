package materialize_test

import (
	"context"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestXMLTypeAlongsideNamedObject(t *testing.T) {
	const query = "SELECT v,x FROM mixed"
	xml := append(testpeer.TextColumn("x", 160, 63, 0), 0, 7, 'X', 'M', 'L', 'T', 'Y', 'P', 'E', 251)
	columns := [][]byte{complexColumnFixture(2), xml}
	db := publictest.OutOracleDB(t, oceanbase.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepareStatus(ctx, t, p, query, 53, 0, columns, 6) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(53)) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, columns[0], columns[1], []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 1, '7', 4, '<', 'x', '/', '>'}, []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "N", Type: "NUMBER"}}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 53)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	stmt, err := db.Prepare(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	var object oceanbase.Object
	var text string
	if err := stmt.QueryRow().Scan(&object, &text); err != nil {
		t.Fatal(err)
	}
	if object.Type != "T" || len(object.Fields) != 1 || object.Fields[0].Value != oceanbase.Number("7") || text != "<x/>" {
		t.Fatal(object, text)
	}
}
