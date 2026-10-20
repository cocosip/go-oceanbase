package materialize_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	testing "testing"
	time "time"
	_ "time/tzdata"

	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorColumn(t *testing.T) {
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT cursor_column FROM t", 91, 0, nil) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(91)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("child", 163, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint32([]byte{0, 0}, 92), []byte{254, 0, 0, 6, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.CursorFetchFixture(92, 128)) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("n", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), binary.LittleEndian.AppendUint64([]byte{0, 0}, 43), []byte{254, 0, 0, 134, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 92)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 91)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	stmt, err := s.PrepareContext(ctx, "SELECT cursor_column FROM t")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stmt.Close() }()
	rows, err := stmt.QueryContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var child sql.Rows
	if !rows.Next() || rows.Scan(&child) != nil {
		t.Fatal(rows.Err())
	}
	var n int64
	if !child.Next() || child.Scan(&n) != nil || n != 42 {
		t.Fatal(n, child.Err())
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if child.Next() {
		t.Fatal("child survived parent Rows.Close")
	}
}
