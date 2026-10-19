package oceanbase

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const optimizedSelect = "SELECT ? FROM dual"

func combinedQueryPacket(id uint32, query string, value int64, reuse, _ bool) []byte {
	b := append(stmtPacket(161, id), 0, 1, 0, 0, 0, byte(len(query)))
	b = append(b, query...)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = append(b, 0, 1)
	if reuse {
		b[len(b)-1] = 0
	} else {
		b = append(b, 8, 0)
	}
	b = binary.LittleEndian.AppendUint64(b, uint64(value))
	b = append(b, make([]byte, 16)...)
	var sum uint32
	for _, v := range []byte(query) {
		sum ^= uint32(v)
		for range 8 {
			if sum&1 != 0 {
				sum = sum>>1 ^ 0x82f63b78
			} else {
				sum >>= 1
			}
		}
	}
	binary.LittleEndian.PutUint32(b[len(b)-8:], sum)
	return b
}

func TestPrepareExecuteLOBOpen(t *testing.T) {
	for _, typ := range []byte{210, 211} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) {
			query := "SELECT lob FROM t WHERE id=?"
			db := optimizationDB(t, ModeOracle, ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
				if !readCommand(ctx, t, p, combinedQueryPacket(0, query, 1, false, false)) {
					return
				}
				value := lobInlineFixture(testLOBABC)
				row := append([]byte{0, 0, byte(len(value))}, value...)
				if !sendPackets(ctx, t, p, combinedHeader(7, 1, 1, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("lob", typ, 45, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}, commandPacket(0, 0, 6)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			defer func() { _ = pinned.Close() }()
			if typ == 210 {
				b, err := OpenBlob(context.Background(), pinned, query, 1)
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
				c, err := OpenClob(context.Background(), pinned, query, 1)
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

func TestPrepareExecuteQueries(t *testing.T) {
	db := optimizationDB(t, ModeOracle, ExecutionOptions{PrepareExecute: true, StatementCacheSize: 1, ReuseParameterTypes: true, StatementChecksum: true}, func(ctx context.Context, p *wire.PacketIO) {
		for i := int64(1); i <= 3; i++ {
			id := uint32(0)
			if i > 1 {
				id = 7
			}
			if !readCommand(ctx, t, p, combinedQueryPacket(id, optimizedSelect, i, i > 1, true)) {
				return
			}
			row := binary.LittleEndian.AppendUint64([]byte{0, 0}, uint64(i))
			if !sendPackets(ctx, t, p, combinedHeader(7, 1, 1, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("v", 8, 63, 0), []byte{254, 0, 0, 6, 0}, row, []byte{254, 0, 0, 6, 0}, commandPacket(0, 0, 6)) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	for i := int64(1); i <= 3; i++ {
		var got int64
		if err := db.QueryRowContext(context.Background(), optimizedSelect, i).Scan(&got); err != nil || got != i {
			t.Fatal(got, err)
		}
	}
}

func TestPrepareExecuteFailureNoReplay(t *testing.T) {
	db := optimizationDB(t, ModeOracle, ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !readCommand(ctx, t, p, combinedQueryPacket(0, testBatchSQL, 1, false, false)) || !sendPackets(ctx, t, p, []byte{255, 38, 4, '#', '2', '3', '0', '0', '0', 'x'}) {
			return
		}
		if !serveOK(ctx, t, p, []byte{14}, 6) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(context.Background(), testBatchSQL, 1); err == nil {
		t.Fatal("server failure lost")
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal("failed combined command replayed or lost synchronization", err)
	}
}

func TestPrepareExecuteExplicitStatement(t *testing.T) {
	db := optimizationDB(t, ModeOracle, ExecutionOptions{PrepareExecute: true, ReuseParameterTypes: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !servePrepareStatus(ctx, t, p, testBatchSQL, 7, 1, nil, 6) {
			return
		}
		for i := int64(1); i <= 2; i++ {
			if !readCommand(ctx, t, p, combinedQueryPacket(7, testBatchSQL, i, i > 1, false)) {
				return
			}
			header := combinedHeader(7, 0, 1, 0)
			header[16] = 0
			if !sendPackets(ctx, t, p, header, textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}) {
				return
			}
			p.RestartResponseSequence()
			if !sendPackets(ctx, t, p, commandPacket(1, 0, 6)) {
				return
			}
		}
		if !readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
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
