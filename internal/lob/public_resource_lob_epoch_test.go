package lob_test

import (
	context "context"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestLOBTransactionEndsBetweenChunks(t *testing.T) {
	for _, variant := range []string{"plain", publictest.TestCompression, publictest.TestOB20} {
		for _, operation := range []string{"read-blob", "read-clob", "write-blob", "write-clob"} {
			for _, prefix := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/prefix=%v", variant, operation, prefix), func(t *testing.T) {
					characters := strings.HasSuffix(operation, "clob")
					write := strings.HasPrefix(operation, "write")
					typ := byte(210)
					if characters {
						typ = 211
					}
					locator := publictest.LobOutFixture(6)
					if write {
						locator = publictest.LobOutFixture(0)
					}
					db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) ||
							!publictest.ServeLOBQueryStatus(ctx, t, p, "SELECT lob FROM t", typ, 5, locator) {
							return
						}
						for i := 0; i <= prefix; i++ {
							status := byte(4)
							if i < prefix {
								status = 5
							}
							query := lob.ReadBlobSQL
							amount, offset := int64(6-2*i), int64(1+2*i)
							if characters {
								query, amount = lob.ReadClobSQL, 8191
							}
							params := []wire.Parameter{publictest.LOBInteger(amount), {Type: uint16(typ), Data: locator, LengthEncoded: true}, publictest.LOBInteger(offset), {Type: 6}, {Type: 6}}
							columns := [][]byte{testpeer.TextColumn("n", 8, 63, 0), testpeer.TextColumn("v", 203, 63, 0)}
							if characters {
								columns[1] = testpeer.TextColumn("v", 253, 45, 0)
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
								query = lob.WriteSQL
								params = []wire.Parameter{{Type: uint16(typ), Data: locator, LengthEncoded: true}, publictest.LOBInteger(amount), publictest.LOBInteger(1 + int64(i)*amount), {Type: paramType, Data: []byte(part), LengthEncoded: true}}
								locator = publictest.LobOutFixture(uint64((i + 1) * len(part)))
								columns = [][]byte{testpeer.TextColumn("lob", typ, 45, 0)}
								row = append([]byte{0, 0, byte(len(locator))}, locator...)
							}
							if !publictest.ServeLOBCallStatus(ctx, t, p, query, params, columns, row, status) {
								return
							}
						}
						// A complete status change must stop LOB traffic without poisoning the session.
						if !testpeer.ServeOK(ctx, t, p, []byte{14}, 4) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					pinned := publictest.PinnedSession(t, db)
					tx, err := pinned.BeginTx(context.Background(), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					var n int64
					var want int64
					if characters {
						lob, openErr := oceanbase.OpenClob(context.Background(), tx, "SELECT lob FROM t")
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
						lob, openErr := oceanbase.OpenBlob(context.Background(), tx, "SELECT lob FROM t")
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
					if n != want || !errors.Is(err, oceanbase.ErrResource) {
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
