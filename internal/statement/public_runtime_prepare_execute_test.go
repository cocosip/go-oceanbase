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

func TestPrepareExecuteExplicitStatement(t *testing.T) {
	db := optimizationDB(t, oceanbase.ModeOracle, oceanbase.ExecutionOptions{PrepareExecute: true, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 6) {
			return
		}
		for i := int64(1); i <= 2; i++ {
			if !testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(7, testBatchSQL, i, i > 1, false)) {
				return
			}
			header := publictest.CombinedHeader(7, 0, 1, 0)
			header[16] = 0
			if !testpeer.SendPackets(ctx, t, p, header, testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}) {
				return
			}
			p.RestartResponseSequence()
			if !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(1, 0, 6)) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), testBatchSQL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for i := 1; i <= 2; i++ {
		r, err := s.ExecContext(context.Background(), i)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	}
}
