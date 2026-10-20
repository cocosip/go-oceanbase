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

func TestExecutionColumnPrepareExecute(t *testing.T) {
	want := oceanbase.ResultColumn{Name: "combined_label", OriginalName: executionColumnOrigin}
	column := executionColumnPacket(t, want, "utf8mb4")
	db := optimizationDB(t, oceanbase.ModeOracle, oceanbase.ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(0, optimizedSelect, 7, false, false)) || !testpeer.SendPackets(ctx, t, p,
			publictest.CombinedHeader(7, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0},
			column, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 7, 0, 0, 0}, []byte{254, 0, 0, 6, 0}, publictest.CommandPacket(0, 0, 6)) ||
			!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	e, err := oceanbase.Execute(context.Background(), publictest.PinnedSession(t, db), optimizedSelect, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertExecutionColumnRow(t, e, want)
}
