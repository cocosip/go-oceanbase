package oceanbase

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const cursorEpochQuery = "SELECT id,b FROM s.t"

func serveUpdatableEpochCursor(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, typ byte) bool {
	t.Helper()
	const columnsDictionary = "ALL_TAB_COLUMNS"
	status := byte(3)
	steps := []txStep{{testMySQLReadWrite, 3}}
	if mode == ModeOracle {
		status = 5
		steps = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
	}
	if !serveTxSteps(ctx, t, p, steps) || !servePrepareStatus(ctx, t, p, cursorEpochQuery, 81, 0, nil, status) ||
		!readCommand(ctx, t, p, cursorExecuteFixture(81)) ||
		!sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", typ), []byte{254, 0, 0, status | 64, 0}) {
		return false
	}
	if mode == ModeMySQL {
		return serveTableDictionary(ctx, t, p, status, tableDictionary()...)
	}
	typeName := "VARCHAR2"
	if typ == 210 {
		typeName = "BLOB"
	}
	return serveTableRows(ctx, t, p, status, []string{"FROM ALL_TABLES t", columnsDictionary}, []byte{253, 253, 253, 253, 253, 253, 253, 253},
		[]any{"s", "t", blobBaseTable, nil, "id", typeNameNumber, "NO", "1"},
		[]any{"s", "t", blobBaseTable, nil, "b", typeName, "NO", nil})
}

func TestCursorMutationFinalTransactionInvalidation(t *testing.T) {
	const deleteOperation = "delete"
	const insertOperation = "insert"
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
			for _, op := range []string{"update", deleteOperation, insertOperation} {
				t.Run(map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[mode]+"/"+variant+"/"+op, func(t *testing.T) {
					status, ended, typ, quote := byte(3), byte(2), byte(252), "`"
					value := any([]byte("v"))
					finish := []txStep{{testRollbackSQL, 2}}
					if mode == ModeOracle {
						status, ended, typ, quote, value = 5, 4, 253, "\"", "v"
						finish = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
					}
					table, id, b := quote+"s"+quote+"."+quote+"t"+quote, quote+"id"+quote, quote+"b"+quote
					db := batchRewriteDB(t, Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !serveUpdatableEpochCursor(ctx, t, p, mode, typ) {
							return
						}
						query, count, affected := "UPDATE "+table+" SET "+b+" = ? WHERE "+id+" = ?", 2, byte(0)
						execute := binary.LittleEndian.AppendUint64(append(executeHeader(82), 0, 1, typ, 0, 8, 0, 1, 'v'), 7)
						switch op {
						case deleteOperation:
							query, count, affected = "DELETE FROM "+table+" WHERE "+id+" = ?", 1, 1
							execute = binary.LittleEndian.AppendUint64(append(executeHeader(82), 0, 1, 8, 0), 7)
						case insertOperation:
							query, affected = "INSERT INTO "+table+" ("+id+","+b+") VALUES (?,?)", 1
							execute = append(binary.LittleEndian.AppendUint64(append(executeHeader(82), 0, 1, 8, 0, typ, 0), 7), 1, 'v')
						}
						if !servePrepareStatus(ctx, t, p, query, 82, count, nil, status) {
							return
						}
						if op != insertOperation && !serveTableRows(ctx, t, p, status, []string{"SELECT 1 FROM " + table + " WHERE " + id + " =  7  FOR UPDATE"}, []byte{8}, []any{"1"}) {
							return
						}
						if !readCommand(ctx, t, p, execute) || !sendPackets(ctx, t, p, []byte{0, affected, 0, ended, 0, 0, 0}) ||
							!readCommand(ctx, t, p, stmtPacket(25, 82)) || !readCommand(ctx, t, p, stmtPacket(25, 81)) ||
							!serveOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !serveTxSteps(ctx, t, p, finish) {
							return
						}
						p.ResetSequence()
						serveQuit(t, p)
					})
					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					cur, err := OpenCursor(ctx, tx, cursorEpochQuery, CursorOptions{Updatable: true})
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = cur.Close() }()
					key := map[string]any{"id": 7}
					switch op {
					case "update":
						err = cur.Update(ctx, key, ColumnChange{"b", value})
					case deleteOperation:
						err = cur.Delete(ctx, key)
					case insertOperation:
						err = cur.Insert(ctx, ColumnChange{"id", 7}, ColumnChange{"b", value})
					}
					if !errors.Is(err, ErrResource) {
						t.Fatal("mutation accepted an ended transaction", err)
					}
					if _, err = cur.Fetch(ctx, CursorPosition{}, 1); !errors.Is(err, ErrResource) {
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

func TestCursorRefreshTransactionInvalidation(t *testing.T) {
	const lobKind = "lob"
	for _, kind := range []string{transactionMySQLLabel, transactionOracleLabel, lobKind} {
		for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
			t.Run(kind+"/"+variant, func(t *testing.T) {
				mode, status, ended, typ, quote := ModeMySQL, byte(3), byte(2), byte(252), "`"
				finish := []txStep{{testRollbackSQL, 2}}
				if kind != transactionMySQLLabel {
					mode, status, ended, typ, quote = ModeOracle, 5, 4, 253, "\""
					finish = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
				}
				row := append(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), 1, 'v')
				if kind == lobKind {
					typ = 210
					row = complexLenBytes(binary.LittleEndian.AppendUint64([]byte{0, 0}, 7), lobOutFixture(3))
				}
				table, id, b := quote+"s"+quote+"."+quote+"t"+quote, quote+"id"+quote, quote+"b"+quote
				query := "SELECT " + id + " AS " + id + "," + b + " AS " + b + " FROM " + table + " WHERE " + id + " = ?"
				db := batchRewriteDB(t, Config{Mode: mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveUpdatableEpochCursor(ctx, t, p, mode, typ) || !servePrepareStatus(ctx, t, p, query, 82, 1, nil, status) ||
						!readCommand(ctx, t, p, binary.LittleEndian.AppendUint64(append(executeHeader(82), 0, 1, 8, 0), 7)) ||
						!sendPackets(ctx, t, p, []byte{2}, cursorBaseColumn("id", 8), cursorBaseColumn("b", typ), []byte{254, 0, 0, status, 0}, row, []byte{254, 0, 0, ended, 0}) {
						return
					}
					// A complete invalidating response permits cleanup, but no locator reads.
					if !readCommand(ctx, t, p, stmtPacket(25, 82)) || !readCommand(ctx, t, p, stmtPacket(25, 81)) ||
						!serveOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !serveTxSteps(ctx, t, p, finish) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				cur, err := OpenCursor(ctx, tx, cursorEpochQuery, CursorOptions{Updatable: true})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = cur.Close() }()
				rows, err := cur.Refresh(ctx, map[string]any{"id": 7})
				if rows != nil {
					_ = rows.Close()
					t.Fatal("stale refresh published rows")
				}
				if !errors.Is(err, ErrResource) {
					t.Fatal("refresh lost transaction invalidation", err)
				}
				if _, err = cur.Fetch(ctx, CursorPosition{}, 1); !errors.Is(err, ErrResource) {
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
