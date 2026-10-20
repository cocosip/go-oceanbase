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

func TestStatementTypeFailure(t *testing.T) {
	db := optimizationDB(t, oceanbase.ModeMySQL, oceanbase.ExecutionOptions{ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) {
			return
		}
		for i := int64(0); i < 3; i++ {
			if !testpeer.ReadCommand(ctx, t, p, optimizationExecute(7, i, i == 1)) {
				return
			}
			response := publictest.CommandPacket(1, 0, 2)
			if i == 1 {
				response = []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}
			}
			if !testpeer.SendPackets(ctx, t, p, response) {
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
	for i := 0; i < 3; i++ {
		_, err = s.ExecContext(context.Background(), i)
		if (err != nil) != (i == 1) {
			t.Fatal(i, err)
		}
	}
}
