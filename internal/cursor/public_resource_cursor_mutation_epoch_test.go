package cursor_test

import (
	context "context"
	binary "encoding/binary"
	errors "errors"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestCursorRefreshTransactionInvalidation(t *testing.T) {
	const lobKind = "lob"
	for _, kind := range []string{publictest.TransactionMySQLLabel, publictest.TransactionOracleLabel, lobKind} {
		for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
			t.Run(kind+"/"+variant, func(t *testing.T) {
				mode, status, ended, typ, quote := config.ModeMySQL, byte(3), byte(2), byte(252), "`"
				finish := []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}
				if kind != publictest.TransactionMySQLLabel {
					mode, status, ended, typ, quote = config.ModeOracle, 5, 4, 253, "\""
					finish = []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}
				}
				row := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), 1, 'v')
				if kind == lobKind {
					typ = 210
					row = wire.AppendComplexLenBytes(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), publictest.LobOutFixture(3))
				}
				table, id, b := quote+"s"+quote+"."+quote+"t"+quote, quote+"id"+quote, quote+"b"+quote
				query := "SELECT " + id + " AS " + id + "," + b + " AS " + b + " FROM " + table + " WHERE " + id + " = ?"
				db := publictest.BatchRewriteDB(t, config.Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveUpdatableEpochCursor(ctx, t, p, mode, typ) || !publictest.ServePrepareStatus(ctx, t, p, query, 82, 1, nil, status) ||
						!testpeer.ReadCommand(ctx, t, p, binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(82), 0, 1, 8, 0), 7)) ||
						!testpeer.SendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", typ), []byte{254, 0, 0, status, 0}, row, []byte{254, 0, 0, ended, 0}) {
						return
					}
					// A complete invalidating response permits cleanup, but no locator reads.
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 82)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 81)) ||
						!testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !publictest.ServeTxSteps(ctx, t, p, finish) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				cur, err := oceanbase.OpenCursor(ctx, tx, cursorEpochQuery, oceanbase.CursorOptions{Updatable: true})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cur.Close() }()
				rows, err := cur.Refresh(ctx, map[string]any{"id": 7})
				if rows != nil {
					_ = rows.Close()
					t.Fatal("stale refresh published rows")
				}
				if !errors.Is(err, oceanbase.ErrResource) {
					t.Fatal("refresh lost transaction invalidation", err)
				}
				if _, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
					t.Fatal("failed refresh retained cursor", err)
				}
				if _, err = tx.ExecContext(ctx, "SELECT 1"); err != nil {
					t.Fatal("complete refresh damaged session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCursorMutationFinalTransactionInvalidation(t *testing.T) {
	const deleteOperation = "delete"
	const insertOperation = "insert"
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
			for _, op := range []string{"update", deleteOperation, insertOperation} {
				t.Run(map[config.Mode]string{config.ModeMySQL: publictest.TransactionMySQLLabel, config.ModeOracle: publictest.TransactionOracleLabel}[mode]+"/"+variant+"/"+op, func(t *testing.T) {
					status, ended, typ, quote := byte(3), byte(2), byte(252), "`"
					value := any([]byte("v"))
					finish := []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}
					if mode == config.ModeOracle {
						status, ended, typ, quote, value = 5, 4, 253, "\"", "v"
						finish = []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}
					}
					table, id, b := quote+"s"+quote+"."+quote+"t"+quote, quote+"id"+quote, quote+"b"+quote
					db := publictest.BatchRewriteDB(t, config.Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !serveUpdatableEpochCursor(ctx, t, p, mode, typ) {
							return
						}
						query, count, affected := "UPDATE "+table+" SET "+b+" = ? WHERE "+id+" = ?", 2, byte(0)
						execute := binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(82), 0, 1, typ, 0, 8, 0, 1, 'v'), 7)
						switch op {
						case deleteOperation:
							query, count, affected = "DELETE FROM "+table+" WHERE "+id+" = ?", 1, 1
							execute = binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(82), 0, 1, 8, 0), 7)
						case insertOperation:
							query, affected = "INSERT INTO "+table+" ("+id+","+b+") VALUES (?,?)", 1
							execute = append(binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(82), 0, 1, 8, 0, typ, 0), 7), 1, 'v')
						}
						if !publictest.ServePrepareStatus(ctx, t, p, query, 82, count, nil, status) {
							return
						}
						if op != insertOperation && !publictest.ServeTableRows(ctx, t, p, status, []string{"SELECT 1 FROM " + table + " WHERE " + id + " =  7  FOR UPDATE"}, []byte{8}, []any{"1"}) {
							return
						}
						if !testpeer.ReadCommand(ctx, t, p, execute) || !testpeer.SendPackets(ctx, t, p, []byte{0, affected, 0, ended, 0, 0, 0}) ||
							!testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 82)) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 81)) ||
							!testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !publictest.ServeTxSteps(ctx, t, p, finish) {
							return
						}
						p.ResetSequence()
						testpeer.ServeQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					cur, err := oceanbase.OpenCursor(ctx, tx, cursorEpochQuery, oceanbase.CursorOptions{Updatable: true})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = cur.Close() }()
					key := map[string]any{"id": 7}
					switch op {
					case "update":
						err = cur.Update(ctx, key, oceanbase.ColumnChange{Name: "b", Value: value})
					case deleteOperation:
						err = cur.Delete(ctx, key)
					case insertOperation:
						err = cur.Insert(ctx, oceanbase.ColumnChange{Name: "id", Value: 7}, oceanbase.ColumnChange{Name: "b", Value: value})
					}
					if !errors.Is(err, oceanbase.ErrResource) {
						t.Fatal("mutation accepted an ended transaction", err)
					}
					if _, err = cur.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
						t.Fatal("failed mutation retained cursor", err)
					}
					if _, err = tx.ExecContext(ctx, "SELECT 1"); err != nil {
						t.Fatal("complete mutation damaged session", err)
					}
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
