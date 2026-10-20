package call_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	strings "strings"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLongDataOracleOutputs(t *testing.T) {
	const query = "BEGIN proc(?,?); END;"
	db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 79, 2, nil) || !testpeer.ReadCommand(ctx, t, p, longDataPacket(config.ModeOracle, 79, 0, 3, []byte("6162"))) || !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) {
			return
		}
		params := []wire.Parameter{{Type: 254, LongData: true}, {Type: 6}}
		payload, err := wire.EncodePrepareExecute(79, []byte(query), params, 1<<20)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, payload) {
			t.Error(err)
			return
		}
		if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(79, 1, 2, 4), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 14, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 79)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int64
	if _, err := db.ExecContext(context.Background(), query, &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &n}); err != nil || n != 42 {
		t.Fatal(n, err)
	}
}

func TestLongDataMySQLOutputs(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		p.ResetSequence()
		init, err := p.ReadMessage(ctx)
		if err != nil || !strings.HasPrefix(string(init), "\x03SET @_obgo_") || !strings.HasSuffix(string(init), "=NULL") {
			t.Error("bad variable initialization", err)
			return
		}
		variable := strings.TrimSuffix(strings.TrimPrefix(string(init), "\x03SET "), "=NULL")
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 0, 0, 2, 0, 0, 0}) || !publictest.ServePrepare(ctx, t, p, "CALL p( ? , "+variable+" )", 81, 1, nil) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, longDataPacket(config.ModeMySQL, 81, 0, 3, []byte("ab"))) || !testpeer.ServeOK(ctx, t, p, append(publictest.ExecuteHeader(81), 0, 1, 252, 0), 2) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 81)) {
			return
		}
		if !testpeer.ReadQuery(ctx, t, p, "SELECT "+variable) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("o", 8, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{2, '4', '2'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte("\x03SET "+variable+"=NULL"), 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int64
	if _, err := db.ExecContext(context.Background(), "CALL p(?,?)", &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1}, sql.Out{Dest: &n}); err != nil || n != 42 {
		t.Fatal(n, err)
	}
}
