package oceanbase

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBTransactionEndsBetweenChunks(t *testing.T) {
	for _, variant := range []string{"plain", testCompression, testOB20} {
		for _, operation := range []string{"read-blob", "read-clob", "write-blob", "write-clob"} {
			for _, prefix := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/prefix=%v", variant, operation, prefix), func(t *testing.T) {
					characters := strings.HasSuffix(operation, "clob")
					write := strings.HasPrefix(operation, "write")
					typ := byte(210)
					if characters {
						typ = 211
					}
					locator := lobOutFixture(6)
					if write {
						locator = lobOutFixture(0)
					}
					db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) ||
							!serveLOBQueryStatus(ctx, t, p, "SELECT lob FROM t", typ, 5, locator) {
							return
						}
						for i := 0; i <= prefix; i++ {
							status := byte(4)
							if i < prefix {
								status = 5
							}
							query := lobReadBlobSQL
							amount, offset := int64(6-2*i), int64(1+2*i)
							if characters {
								query, amount = lobReadClobSQL, 8191
							}
							params := []wire.Parameter{lobInteger(amount), {Type: uint16(typ), Data: locator, LengthEncoded: true}, lobInteger(offset), {Type: 6}, {Type: 6}}
							columns := [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}
							if characters {
								columns[1] = textColumn("v", 253, 45, 0)
							}
							row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 2)
							row = append(row, 2, 'a', 'b')
							if write {
								amount = 32767
								part := strings.Repeat("x", int(amount))
								paramType := uint16(203)
								if characters {
									amount, part, paramType = 8191, strings.Repeat("中", 8191), 253
								}
								query = lobWriteSQL
								params = []wire.Parameter{{Type: uint16(typ), Data: locator, LengthEncoded: true}, lobInteger(amount), lobInteger(1 + int64(i)*amount), {Type: paramType, Data: []byte(part), LengthEncoded: true}}
								locator = lobOutFixture(uint64((i + 1) * len(part)))
								columns = [][]byte{textColumn("lob", typ, 45, 0)}
								row = append([]byte{0, 0, byte(len(locator))}, locator...)
							}
							if !serveLOBCallStatus(ctx, t, p, query, params, columns, row, status) {
								return
							}
						}
						// A complete status change must stop LOB traffic without poisoning the session.
						if !serveOK(ctx, t, p, []byte{14}, 4) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					pinned := pinnedSession(t, db)
					tx, err := pinned.BeginTx(context.Background(), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					var n int64
					var want int64
					if characters {
						lob, openErr := OpenClob(context.Background(), tx, "SELECT lob FROM t")
						if openErr != nil {
							t.Fatal(openErr)
						}
						defer func() { _ = lob.Close() }()
						if write {
							n, err = lob.WriteChars(context.Background(), 0, strings.Repeat("中", 16383))
							want = int64(prefix) * 8191
						} else {
							var value string
							value, err = lob.ReadChars(context.Background(), 0, 6)
							n, want = int64(len(value)), int64(prefix)*2
						}
					} else {
						lob, openErr := OpenBlob(context.Background(), tx, "SELECT lob FROM t")
						if openErr != nil {
							t.Fatal(openErr)
						}
						defer func() { _ = lob.Close() }()
						var count int
						if write {
							count, err = lob.WriteAtContext(context.Background(), []byte(strings.Repeat("x", 65535)), 0)
							want = int64(prefix) * 32767
						} else {
							count, err = lob.ReadAtContext(context.Background(), make([]byte, 6), 0)
							want = int64(prefix) * 2
						}
						n = int64(count)
					}
					if n != want || !errors.Is(err, ErrResource) {
						t.Fatalf("completed=%d want=%d, error=%v; stale locator must stop", n, want, err)
					}
					if err = pinned.PingContext(context.Background()); err != nil {
						t.Fatal("complete response poisoned the session", err)
					}
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestLOBMaterializationTransactionEndsBetweenChunks(t *testing.T) {
	locator := lobOutFixture(6)
	db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) ||
			!serveLOBQueryStatus(ctx, t, p, "SELECT lob FROM t", 210, 5, locator) {
			return
		}
		params := []wire.Parameter{lobInteger(6), {Type: 210, Data: locator, LengthEncoded: true}, lobInteger(1), {Type: 6}, {Type: 6}}
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 2)
		row = append(row, 2, 'a', 'b')
		if !serveLOBCallStatus(ctx, t, p, lobReadBlobSQL, params, [][]byte{textColumn("n", 8, 63, 0), textColumn("v", 203, 63, 0)}, row, 4) {
			return
		}
		p.ResetSequence()
		if command, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("materialization continued after invalidation: command=%x", command[:min(16, len(command))])
		}
	})
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var value []byte
	err = tx.QueryRowContext(context.Background(), "SELECT lob FROM t").Scan(&value)
	if !errors.Is(err, ErrResource) || value != nil {
		t.Fatalf("stale materialization published: value=%q err=%v", value, err)
	}
	_ = tx.Rollback()
	if db.Stats().Idle != 0 {
		t.Fatal("failed materialization returned its connection to the pool")
	}
}
