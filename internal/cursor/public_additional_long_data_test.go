package cursor_test

import (
	context "context"
	binary "encoding/binary"
	strings "strings"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLongDataCursor(t *testing.T) {
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, "SELECT ?", 80, 1, nil) || !testpeer.ReadCommand(ctx, t, p, longDataPacket(config.ModeMySQL, 80, 0, 3, []byte("ab"))) {
			return
		}
		payload := append(publictest.ExecuteHeader(80), 0, 1, 252, 0)
		payload[5] = 1
		if !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 66, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 80)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	session, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	cur, err := oceanbase.OpenCursor(context.Background(), session, "SELECT ?", oceanbase.CursorOptions{}, &oceanbase.LongData{Reader: strings.NewReader("ab"), Length: -1})
	if err != nil {
		t.Fatal(err)
	}
	if err = cur.Close(); err != nil {
		t.Fatal(err)
	}
}

func longDataPacket(mode config.Mode, id uint32, index uint16, piece byte, data []byte) []byte {
	b := binary.LittleEndian.AppendUint16(publictest.StmtPacket(24, id), index)
	if mode == config.ModeOracle {
		b[0] = 162
		b = append(b, piece, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(data)))
	}
	return append(b, data...)
}
