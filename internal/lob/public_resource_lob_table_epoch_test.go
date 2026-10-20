package lob_test

import (
	bytes "bytes"
	context "context"
	errors "errors"
	fmt "fmt"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTableBlobOpenTransactionInvalidation(t *testing.T) {
	const schemaStage, dictionaryStage = "schema", "dictionary"
	const oracleTableSQL = "FROM ALL_TABLES t"
	for _, tc := range []struct {
		mode  config.Mode
		stage string
	}{{config.ModeMySQL, schemaStage}, {config.ModeMySQL, dictionaryStage}, {config.ModeMySQL, "length"}, {config.ModeOracle, dictionaryStage}, {config.ModeOracle, "locator"}} {
		for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
			label := map[config.Mode]string{config.ModeMySQL: publictest.TransactionMySQLLabel, config.ModeOracle: publictest.TransactionOracleLabel}[tc.mode]
			t.Run(label+"/"+tc.stage+"/"+variant, func(t *testing.T) {
				active, ended := byte(3), byte(2)
				start, finish := []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}
				if tc.mode == config.ModeOracle {
					active, ended = 5, 4
					start = []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}
					finish = []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}
				}
				db := publictest.BatchRewriteDB(t, config.Config{Mode: tc.mode}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServeTxSteps(ctx, t, p, start) {
						return
					}
					if tc.stage == schemaStage {
						if !publictest.ServeTableRows(ctx, t, p, ended, []string{sessionReadDatabaseSQL}, []byte{253}, []any{"s"}) {
							return
						}
					} else {
						status := active
						if tc.stage == dictionaryStage {
							status = ended
						}
						if tc.mode == config.ModeMySQL {
							if !publictest.ServeTableDictionary(ctx, t, p, status, publictest.TableDictionary()...) {
								return
							}
						} else if !publictest.ServeTableRows(ctx, t, p, status, []string{oracleTableSQL}, []byte{253, 253, 253, 253, 253, 253, 253, 253},
							[]any{"s", "t", publictest.BaseTable, nil, "id", valueformat.TypeNameNumber, "NO", "1"}, []any{"s", "t", publictest.BaseTable, nil, "b", valueformat.TypeNameBlob, "NO", nil}) {
							return
						}
						if tc.stage != dictionaryStage {
							if tc.mode == config.ModeMySQL {
								if !publictest.ServeTableRows(ctx, t, p, ended, []string{publictest.TestOctetLength}, []byte{8}, []any{"9"}) {
									return
								}
							} else if !publictest.ServeLOBQueryStatus(ctx, t, p, `SELECT "b" FROM "s"."t" WHERE "id" =  7  FOR UPDATE`, 210, ended, publictest.LobOutFixture(9)) {
								return
							}
						}
					}
					if !testpeer.ServeOK(ctx, t, p, []byte("\x03SELECT 1"), ended) || !publictest.ServeTxSteps(ctx, t, p, finish) {
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
				target := publictest.TableTarget()
				if tc.stage == schemaStage {
					target.Schema = ""
				}
				lob, err := oceanbase.OpenTableBlob(ctx, tx, target)
				if lob != nil {
					_ = lob.Close()
					t.Error("invalidating open published a handle")
				}
				if !errors.Is(err, oceanbase.ErrResource) {
					t.Error("invalidating open lost transaction error", err)
				}
				if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
					t.Fatal(err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTableBlobSizeTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		t.Run(variant, func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeMySQL}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) ||
					!publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) ||
					!publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"9"}) ||
					!publictest.ServeTableRows(ctx, t, p, 2, []string{publictest.TestOctetLength}, []byte{8}, []any{"10"}) ||
					!publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 2}, {Query: publictest.TestRollbackSQL, Status: 2}}) {
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
			lob, err := oceanbase.OpenTableBlob(ctx, tx, publictest.TableTarget())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lob.Close() }()
			if size, e := lob.Size(ctx); size != 0 || !errors.Is(e, oceanbase.ErrResource) {
				t.Errorf("stale size published: %d, %v", size, e)
			}
			if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
				t.Fatal(err)
			}
			if err = tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobTransactionEndsBetweenChunks(t *testing.T) {
	const shortRead, write = "short-read", "write"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, operation := range []string{publictest.TestBlobRead, shortRead, write} {
			for _, prefix := range []int{0, 1} {
				t.Run(fmt.Sprintf("%s/%s/prefix=%d", variant, operation, prefix), func(t *testing.T) {
					db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeMySQL, Limits: config.Limits{MaxLOBBytes: 4}}, variant, func(ctx context.Context, p *wire.PacketIO) {
						if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) ||
							!publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) ||
							!publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"9"}) {
							return
						}
						if operation == write && !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestForUpdate}, []byte{8}, []any{"9"}) {
							return
						}
						for i := 0; i <= prefix; i++ {
							status := byte(3)
							if i == prefix {
								status = 2
							}
							if operation == write {
								command := publictest.ReadTableCommand(ctx, t, p)
								for _, part := range []string{"UPDATE `s`.`t` SET `b` = CONCAT(", fmt.Sprintf("LEFT(`b`, %d )", i*4), " X'78787878' ", fmt.Sprintf("SUBSTRING(`b`, %d )", i*4+5), "WHERE `id` =  7 "} {
									if !bytes.Contains(command, []byte(part)) {
										t.Errorf("mutation %q missing %q", command, part)
										return
									}
								}
								if !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, status, 0, 0, 0}) {
									return
								}
							} else {
								data := []byte("abcd")
								if operation == shortRead && i == prefix {
									data = data[:2]
								}
								if !publictest.ServeTableRows(ctx, t, p, status, []string{fmt.Sprintf("SUBSTRING(`b`, %d , 4 )", i*4+1)}, []byte{252}, []any{data}) {
									return
								}
							}
						}
						if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 2}, {Query: publictest.TestRollbackSQL, Status: 2}}) {
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
					lob, err := oceanbase.OpenTableBlob(ctx, tx, publictest.TableTarget())
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
					if n != prefix*4 || !errors.Is(err, oceanbase.ErrResource) {
						t.Errorf("completed=%d want=%d, error=%v", n, prefix*4, err)
					}
					if _, err = lob.Size(ctx); !errors.Is(err, oceanbase.ErrResource) {
						t.Fatal("stale table target remained usable", err)
					}
					if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
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
