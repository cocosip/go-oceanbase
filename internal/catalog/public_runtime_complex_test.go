package catalog_test

import (
	context "context"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDescriptorRefresh(t *testing.T) {
	const query = "SELECT v FROM complex_fixture"
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 18, 0, [][]byte{complexColumnFixture(1)}) {
			return
		}
		for version := byte(1); version <= 2; version++ {
			if !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(18)) {
				return
			}
			row := []byte{0, 0, 0, 1, '7'}
			fields := []string{"A"}
			if version == 2 {
				row = append(row, 1, '8')
				fields = append(fields, "B")
			}
			if !testpeer.SendPackets(ctx, t, p, []byte{1}, complexColumnFixture(version), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) || !serveComplexDictionary(ctx, t, p, fields) {
				return
			}
		}
		if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 18)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	for version := uint64(1); version <= 2; version++ {
		var v oceanbase.Object
		if err = s.QueryRowContext(ctx).Scan(&v); err != nil {
			t.Fatal(err)
		}
		if v.Version != version || len(v.Fields) != int(version) {
			t.Fatal(v)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptorNoReentry(t *testing.T) {
	const query = "SELECT v, id FROM complex_fixture"
	column := complexColumnFixture(7)
	db := publictest.TxDB(t, oceanbase.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServePrepare(ctx, t, p, query, 17, 0, [][]byte{column, testpeer.TextColumn("id", 8, 63, 0)}) || !testpeer.ReadCommand(ctx, t, p, publictest.ExecuteHeader(17)) {
			return
		}
		row := []byte{0, 0, 0, 4, '1', '.', '2', '0', 42, 0, 0, 0, 0, 0, 0, 0}
		if !testpeer.SendPackets(ctx, t, p, []byte{2}, column, testpeer.TextColumn("id", 8, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}) {
			return
		}
		// net.Pipe blocks until the entire business response has been read.
		if !serveComplexDictionary(ctx, t, p, []string{"N"}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 17)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := db.PrepareContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	var value oceanbase.Object
	var id int64
	if err = s.QueryRowContext(ctx).Scan(&value, &id); err != nil {
		t.Fatal(err)
	}
	if id != 42 || value.Version != 7 || len(value.Fields) != 1 || value.Fields[0].Value != oceanbase.Number("1.20") {
		t.Fatal(value, id)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}
