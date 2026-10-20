package execution_test

import (
	context "context"
	binary "encoding/binary"
	fmt "fmt"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPrepareExecuteFailureNoReplay(t *testing.T) {
	db := optimizationDB(t, oceanbase.ModeOracle, oceanbase.ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(0, testBatchSQL, 1, false, false)) || !testpeer.SendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}) {
			return
		}
		if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err == nil {
		t.Fatal("server failure lost")
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal("failed combined command replayed or lost synchronization", err)
	}
}

func TestPrepareExecuteQueries(t *testing.T) {
	db := optimizationDB(t, oceanbase.ModeOracle, oceanbase.ExecutionOptions{PrepareExecute: true, StatementCacheSize: 1, ReuseParameterTypes: true, StatementChecksum: true}, func(ctx context.Context, p *wire.PacketIO) {
		for i := int64(1); i <= 3; i++ {
			id := uint32(0)
			if i > 1 {
				id = 7
			}
			if !testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(id, optimizedSelect, i, i > 1, true)) {
				return
			}
			row := binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(i))
			if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(7, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("v", 8, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}, publictest.CommandPacket(0, 0, 6)) {
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
	for i := int64(1); i <= 3; i++ {
		var got int64
		if err := db.QueryRowContext(context.Background(), optimizedSelect, i).Scan(&got); err != nil || got != i {
			t.Fatal(got, err)
		}
	}
}

func TestPrepareExecuteLOBOpen(t *testing.T) {
	for _, typ := range []byte{210, 211} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) {
			query := "SELECT lob FROM t WHERE id=?"
			db := optimizationDB(t, oceanbase.ModeOracle, oceanbase.ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadCommand(ctx, t, p, publictest.CombinedQueryPacket(0, query, 1, false, false)) {
					return
				}
				value := lobInlineFixture(testLOBABC)
				row := append([]byte{0, 0, byte(len(value))}, value...)
				if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(7, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("lob", typ, 45, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}, publictest.CommandPacket(0, 0, 6)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			if typ == 210 {
				b, err := oceanbase.OpenBlob(context.Background(), pinned, query, 1)
				if err != nil {
					t.Fatal(err)
				}
				data := make([]byte, 3)
				if n, err := b.ReadAtContext(context.Background(), data, 0); err != nil || n != 3 || string(data) != testLOBABC {
					t.Fatal(n, data, err)
				}
				if err := b.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				c, err := oceanbase.OpenClob(context.Background(), pinned, query, 1)
				if err != nil {
					t.Fatal(err)
				}
				if value, err := c.ReadChars(context.Background(), 0, 3); err != nil || value != testLOBABC {
					t.Fatal(value, err)
				}
				if err := c.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
