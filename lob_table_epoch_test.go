package oceanbase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const tableBlobEpochPingSQL = "SELECT 1"

func TestTableBlobTransactionEndsBetweenChunks(t *testing.T) {
	const shortRead, write = "short-read", "write"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, operation := range []string{testBlobRead, shortRead, write} {
			for _, prefix := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/prefix=%d", variant, operation, prefix), func(t *testing.T) {
					db := batchRewriteDB(t, Config{Mode: ModeMySQL, Limits: Limits{MaxLOBBytes: 4}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) ||
							!serveTableDictionary(ctx, t, p, 3, tableDictionary()...) ||
							!serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"9"}) {
							return
						}
						if operation == write && !serveTableRows(ctx, t, p, 3, []string{testForUpdate}, []byte{8}, []any{"9"}) {
							return
						}
						for i := 0; i <= prefix; i++ {
							status := byte(3)
							if i == prefix {
								status = 2
							}
							if operation == write {
								command := readTableCommand(ctx, t, p)
								for _, part := range []string{"UPDATE `s`.`t` SET `b` = CONCAT(", fmt.Sprintf("LEFT(`b`, %d )", i*4), " X'78787878' ", fmt.Sprintf("SUBSTRING(`b`, %d )", i*4+5), "WHERE `id` =  7 "} {
									if !bytes.Contains(command, []byte(part)) {
										t.Errorf("mutation %q missing %q", command, part)
										return
									}
								}
								if !sendPackets(ctx, t, p, []byte{0, 1, 0, status, 0, 0, 0}) {
									return
								}
							} else {
								data := []byte("abcd")
								if operation == shortRead && i == prefix {
									data = data[:2]
								}
								if !serveTableRows(ctx, t, p, status, []string{fmt.Sprintf("SUBSTRING(`b`, %d , 4 )", i*4+1)}, []byte{252}, []any{data}) {
									return
								}
							}
						}
						if !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 2}, {testRollbackSQL, 2}}) {
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
					lob, err := OpenTableBlob(ctx, tx, tableTarget())
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = lob.Close() }()
					data := bytes.Repeat([]byte{'!'}, 9)
					var n int
					if operation == write {
						n, err = lob.WriteAtContext(ctx, bytes.Repeat([]byte{'x'}, 9), 0)
					} else {
						n, err = lob.ReadAtContext(ctx, data, 0)
						if !bytes.Equal(data[:prefix*4], bytes.Repeat([]byte("abcd"), prefix)) || !bytes.Equal(data[prefix*4:], bytes.Repeat([]byte{'!'}, 9-prefix*4)) {
							t.Errorf("stale chunk changed caller buffer: %q", data)
						}
					}
					if n != prefix*4 || !errors.Is(err, ErrResource) {
						t.Errorf("completed=%d want=%d, error=%v", n, prefix*4, err)
					}
					if _, err = lob.Size(ctx); !errors.Is(err, ErrResource) {
						t.Fatal("stale table target remained usable", err)
					}
					if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
						t.Fatal("complete response damaged session", err)
					}
					if err = tx.Rollback(); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestTableBlobSizeTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		t.Run(variant, func(t *testing.T) {
			db := batchRewriteDB(t, Config{Mode: ModeMySQL}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) ||
					!serveTableDictionary(ctx, t, p, 3, tableDictionary()...) ||
					!serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"9"}) ||
					!serveTableRows(ctx, t, p, 2, []string{testOctetLength}, []byte{8}, []any{"10"}) ||
					!serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 2}, {testRollbackSQL, 2}}) {
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
			lob, err := OpenTableBlob(ctx, tx, tableTarget())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lob.Close() }()
			if size, e := lob.Size(ctx); size != 0 || !errors.Is(e, ErrResource) {
				t.Errorf("stale size published: %d, %v", size, e)
			}
			if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobOpenTransactionInvalidation(t *testing.T) {
	const schemaStage, dictionaryStage = "schema", "dictionary"
	const oracleTableSQL = "FROM ALL_TABLES t"
	for _, tc := range []struct {
		mode  Mode
		stage string
	}{{ModeMySQL, schemaStage}, {ModeMySQL, dictionaryStage}, {ModeMySQL, "length"}, {ModeOracle, dictionaryStage}, {ModeOracle, "locator"}} {
		for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
			label := map[Mode]string{ModeMySQL: transactionMySQLLabel, ModeOracle: transactionOracleLabel}[tc.mode]
			t.Run(label+"/"+tc.stage+"/"+variant, func(t *testing.T) {
				active, ended := byte(3), byte(2)
				start, finish := []txStep{{testMySQLReadWrite, 3}}, []txStep{{testRollbackSQL, 2}}
				if tc.mode == ModeOracle {
					active, ended = 5, 4
					start = []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}
					finish = []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}
				}
				db := batchRewriteDB(t, Config{Mode: tc.mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveTxSteps(ctx, t, p, start) {
						return
					}
					if tc.stage == schemaStage {
						if !serveTableRows(ctx, t, p, ended, []string{sessionReadDatabaseSQL}, []byte{253}, []any{"s"}) {
							return
						}
					} else {
						status := active
						if tc.stage == dictionaryStage {
							status = ended
						}
						if tc.mode == ModeMySQL {
							if !serveTableDictionary(ctx, t, p, status, tableDictionary()...) {
								return
							}
						} else if !serveTableRows(ctx, t, p, status, []string{oracleTableSQL}, []byte{253, 253, 253, 253, 253, 253, 253, 253},
							[]any{"s", "t", blobBaseTable, nil, "id", typeNameNumber, "NO", "1"}, []any{"s", "t", blobBaseTable, nil, "b", typeNameBlob, "NO", nil}) {
							return
						}
						if tc.stage != dictionaryStage {
							if tc.mode == ModeMySQL {
								if !serveTableRows(ctx, t, p, ended, []string{testOctetLength}, []byte{8}, []any{"9"}) {
									return
								}
							} else if !serveLOBQueryStatus(ctx, t, p, `SELECT "b" FROM "s"."t" WHERE "id" =  7  FOR UPDATE`, 210, ended, lobOutFixture(9)) {
								return
							}
						}
					}
					if !serveOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !serveTxSteps(ctx, t, p, finish) {
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
				target := tableTarget()
				if tc.stage == schemaStage {
					target.Schema = ""
				}
				lob, err := OpenTableBlob(ctx, tx, target)
				if lob != nil {
					_ = lob.Close()
					t.Error("invalidating open published a handle")
				}
				if !errors.Is(err, ErrResource) {
					t.Error("invalidating open lost transaction error", err)
				}
				if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
					t.Fatal(err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
