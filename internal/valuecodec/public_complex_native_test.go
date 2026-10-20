package valuecodec_test

import (
	"context"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"testing"
	"time"
)

func TestComplexNativeBinding(t *testing.T) {
	const query = "INSERT INTO complex_fixture VALUES (?)"
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "INSERT INTO complex_fixture VALUES (?)", 19, 1, nil) || !publictest.ServeComplexDictionary(ctx, t, p, []string{"N"}) {
			return
		}
		want := append(publictest.ExecuteHeader(19), 0, 1, 160, 0, 1, 'S', 1, 'T', 0, 0, 4, '1', '.', '2', '0')
		if !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 19)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v := oceanbase.Object{Schema: "S", Type: "T", Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number("1.20")}}}
	if _, err := db.ExecContext(ctx, query, v); err != nil {
		t.Fatal(err)
	}
}
