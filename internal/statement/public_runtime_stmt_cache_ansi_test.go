package statement_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestStatementCacheANSIQuotesSwitchDatabase(t *testing.T) {
	const setMode = "SET sql_mode='ANSI_QUOTES'"
	const switchDatabase = `SELECT 1 AS "a\"; USE db2; SELECT 1 AS "b" -- "`
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: testUser, Mode: oceanbase.ModeMySQL, AllowMultiStatements: true,
		Execution: oceanbase.ExecutionOptions{StatementCacheSize: 1, ReuseParameterTypes: true},
		DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
			if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
				return
			}
			ctx = context.Background()
			if !testpeer.ServeOK(ctx, t, p, []byte("\x03"+setMode), 2) {
				return
			}
			if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(7, 1, false), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
				return
			}
			more := []byte{254, 0, 0, 10, 0}
			end := []byte{254, 0, 0, 2, 0}
			column := testpeer.TextColumn("v", 8, 63, 0)
			if !testpeer.ReadQuery(ctx, t, p, switchDatabase) || !testpeer.SendPackets(ctx, t, p,
				[]byte{1}, column, more, []byte{1, '1'}, more,
				publictest.CommandPacket(0, 0, 10),
				[]byte{1}, column, end, []byte{1, '1'}, end) {
				return
			}
			if !publictest.ServePrepare(ctx, t, p, testBatchSQL, 8, 1, nil) || !testpeer.ServeOK(ctx, t, p, optimizationExecute(8, 2, false), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 8)) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	if _, err = db.ExecContext(ctx, setMode); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, testBatchSQL, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, switchDatabase); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, testBatchSQL, 2); err != nil {
		t.Fatal(err)
	}
}
