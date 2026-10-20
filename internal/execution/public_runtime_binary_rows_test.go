package execution_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	math "math"
	testing "testing"
	_ "time/tzdata"

	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestBinaryRows(t *testing.T) {
	query := "SELECT values_from_fixture"
	columns := [][]byte{testpeer.TextColumn("n", 6, 63, 0), testpeer.TextColumn("v", 8, 63, 0), testpeer.TextColumn("u", 8, 63, 32), testpeer.TextColumn("d", 246, 45, 0), testpeer.TextColumn("f", 5, 63, 0), testpeer.TextColumn("b", 252, 63, 0), testpeer.TextColumn("n6", 253, 45, 0), testpeer.TextColumn("n7", 253, 45, 0), testpeer.TextColumn("s", 253, 45, 0), testpeer.TextColumn("n9", 253, 45, 0)}
	row := []byte{0, 4, 1 | 2 | 8}
	row = binary.LittleEndian.AppendUint64(row, uint64(42))
	row = binary.LittleEndian.AppendUint64(row, math.MaxUint64)
	row = append(row, 21)
	row = append(row, "12345678901234567.890"...)
	row = binary.LittleEndian.AppendUint64(row, math.Float64bits(1.25))
	row = append(row, 2, 0, 255, 0)
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 17, 0, columns) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(17)) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{byte(len(columns))}) {
			return
		}
		for _, c := range columns {
			if !testpeer.SendPackets(ctx, t, p, c) {
				return
			}
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 2, 0}, row, []byte{254, 0, 0, 2, 0}) {
			return
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 17)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	s, err := db.PrepareContext(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var n, n6, n7, n9 sql.NullString
	var value int64
	var u, d, text string
	var f float64
	var b []byte
	if err = s.QueryRowContext(context.Background()).Scan(&n, &value, &u, &d, &f, &b, &n6, &n7, &text, &n9); err != nil {
		t.Fatal(err)
	}
	if n.Valid || n6.Valid || n7.Valid || n9.Valid || value != 42 || u != testMaxUint64Text || d != "12345678901234567.890" || f != 1.25 || len(b) != 2 || b[1] != 255 || text != "" {
		t.Fatal(n, value, u, d, f, b, n6, n7, text, n9)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}
