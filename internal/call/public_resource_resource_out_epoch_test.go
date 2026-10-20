package call_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	io "io"
	strings "strings"
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

func TestResourceOutTransactionInvalidation(t *testing.T) {
	const immediate, consume, early = "immediate", "consume", "early"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, typ := range []byte{163, 210, 211} {
			for _, style := range []string{immediate, consume, early} {
				for _, metadata := range []bool{false, true} {
					name := variant + "/" + map[byte]string{163: "cursor", 210: valueformat.TypeNameBlob, 211: valueformat.TypeNameClob}[typ] + "/" + style + "/" + map[bool]string{false: publictest.LobOpenFinal, true: publictest.LobOpenMetadata}[metadata]
					t.Run(name, func(t *testing.T) {
						db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
							if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !serveEpochResourceOut(ctx, t, p, typ, map[bool]byte{false: 0, true: 8}[style != immediate], metadata, 4) {
								return
							}
							if typ == 163 && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 97)) {
								return
							}
							if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 96)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 4}, {Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
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
						var cur *oceanbase.Cursor
						var blob *oceanbase.Blob
						var clob *oceanbase.Clob
						var value int64 = 7
						var output any = oceanbase.CursorOut{Dest: &cur}
						switch typ {
						case 210:
							output = oceanbase.Out{Dest: &blob, Type: valueformat.TypeNameBlob}
						case 211:
							output = oceanbase.Out{Dest: &clob, Type: valueformat.TypeNameClob}
						}
						e, err := oceanbase.Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &value}, output)
						if style != immediate && err != nil {
							t.Fatal("ordinary rows failed before finalization", err)
						}
						if style == consume {
							checkResourceOutOrdinaryRow(t, e.Rows())
						}
						if e != nil {
							err = errors.Join(err, e.Close())
							if !e.Snapshot().Done || !errors.Is(e.Snapshot().Err, oceanbase.ErrResource) {
								t.Error("execution snapshot lost epoch failure", e.Snapshot())
							}
						}
						if value != 7 || cur != nil || blob != nil || clob != nil || !errors.Is(err, oceanbase.ErrResource) {
							_ = cur.Close()
							_ = blob.Close()
							_ = clob.Close()
							t.Fatalf("stale OUT publication: scalar=%d cursor=%v blob=%v clob=%v err=%v", value, cur, blob, clob, err)
						}
						if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
							t.Fatal("complete output response damaged session", err)
						}
						if err = tx.Rollback(); err != nil {
							t.Fatal(err)
						}
					})
				}
			}
		}
	}
}

func TestBufferedResourceOutTransactionEnd(t *testing.T) {
	const nestedLOBStage = "nested-lob"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, typ := range []byte{163, 210, 211, 160} {
			for _, stage := range []string{publictest.LobOpenFinal, nestedLOBStage} {
				if stage == nestedLOBStage && typ != 160 {
					continue
				}
				t.Run(variant+"/"+map[byte]string{163: "cursor", 210: valueformat.TypeNameBlob, 211: valueformat.TypeNameClob, 160: valueformat.TypeNameObject}[typ]+"/"+stage, func(t *testing.T) {
					db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
						tail := byte(4)
						if stage == nestedLOBStage {
							tail = 5
						}
						if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !serveEpochResourceOut(ctx, t, p, 163, typ, false, tail) {
							return
						}
						if stage == nestedLOBStage && !publictest.ServeComplexDictionaryFields(ctx, t, p, "T", []oceanbase.Attribute{{Name: "B", Type: valueformat.TypeNameBlob}}) {
							return
						}
						p.ResetSequence()
						if data, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
							t.Errorf("stale ordinary resource reused connection: %x %v", data, err)
						}
					})
					defer func() { _ = db.Close() }()
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					defer cancel()
					tx, err := db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = tx.Rollback() }()
					var cur *oceanbase.Cursor
					var value int64 = 7
					e, err := oceanbase.Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &value}, oceanbase.CursorOut{Dest: &cur})
					if !errors.Is(err, oceanbase.ErrResource) || cur != nil || value != 7 || e == nil {
						t.Fatal("stale buffered response lost failure", value, cur, err)
					}
					if !e.Snapshot().Done || !errors.Is(e.Snapshot().Err, oceanbase.ErrResource) {
						t.Fatal("buffered response snapshot lost failure", e.Snapshot())
					}
				})
			}
		}
	}
}

func TestComplexResourceOutTransactionEnd(t *testing.T) {
	const descriptorStage = "descriptor-response"
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, stage := range []string{publictest.LobOpenFinal, descriptorStage} {
			t.Run(variant+"/"+stage, func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					payload, err := wire.EncodePrepareExecute(0, []byte(resourceOutEpochQuery), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
					if err != nil {
						t.Error(err)
						return
					}
					status := byte(4)
					if stage == descriptorStage {
						status = 5
					}
					if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !testpeer.ReadCommand(ctx, t, p, payload) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(96, 2, 2, 4), testpeer.TextColumn("p", 6, 63, 0), testpeer.TextColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}, complexColumnFixture(7), testpeer.TextColumn("resource", 163, 63, 0), []byte{254, 0, 0, 5, 16}, []byte{0, 0, 0, 1, '7', 97, 0, 0, 0}, []byte{254, 0, 0, 5, 0}, publictest.CommandPacket(0, 0, status)) {
						return
					}
					if stage == descriptorStage && !publictest.ServeComplexDictionary(ctx, t, p, []string{"N"}) {
						return
					}
					// No dictionary or locator commands may follow a stale deferred row.
					if data, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
						t.Errorf("stale complex output reused connection: %x %v", data, err)
					}
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				v := oceanbase.Object{Type: testComplexUnchanged}
				var cur *oceanbase.Cursor
				_, err = oceanbase.Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &v}, oceanbase.CursorOut{Dest: &cur})
				if !errors.Is(err, oceanbase.ErrResource) || cur != nil || v.Type != testComplexUnchanged {
					t.Fatal("stale complex output published values", v, cur, err)
				}
			})
		}
	}
}

func TestResourceOutEncodingInvalidation(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, invalid := range []bool{false, true} {
			t.Run(variant+"/"+map[bool]string{false: "valid", true: "invalid"}[invalid], func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServeComplexDictionary(ctx, t, p, []string{"N"}) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 6}, {Query: publictest.TestRollbackSQL, Status: 6}}) {
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
				input := oceanbase.Object{Schema: "S", Type: "T", Fields: []oceanbase.Attribute{{Name: "N", Value: oceanbase.Number("7")}}}
				if invalid {
					input.Fields = nil
				}
				var cur *oceanbase.Cursor
				_, err = oceanbase.Execute(ctx, tx, resourceOutEpochQuery, input, oceanbase.CursorOut{Dest: &cur})
				if cur != nil || !errors.Is(err, oceanbase.ErrResource) || invalid && !errors.Is(err, oceanbase.ErrComplex) {
					t.Fatal("lost encoding error or epoch invalidation", cur, err)
				}
				if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
					t.Fatal("encoding rejection damaged session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestScalarOutTransactionEnd(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		t.Run(variant, func(t *testing.T) {
			db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !serveEpochResourceOut(ctx, t, p, 8, 0, false, 4) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 96)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 4}, {Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
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
			var a, b int64
			e, err := oceanbase.Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &a}, sql.Out{Dest: &b})
			if err != nil || a != 42 || b != 9 || !e.Snapshot().Done || e.Snapshot().Err != nil {
				t.Fatal("scalar OUT rejected a completed transaction", a, b, err)
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

func TestResourceOutBeforeExecutionInvalidation(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, stage := range []string{publictest.LobOpenPrepare, "upload"} {
			t.Run(variant+"/"+stage, func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, config.Config{Mode: config.ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					status := byte(5)
					if stage == publictest.LobOpenPrepare {
						status = 4
					}
					if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !publictest.ServePrepareStatus(ctx, t, p, resourceOutEpochQuery, 96, 2, nil, status) {
						return
					}
					if stage == "upload" && (!testpeer.ReadCommand(ctx, t, p, longDataPacket(config.ModeOracle, 96, 0, 3, []byte("6162"))) || !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 4))) {
						return
					}
					if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 96)) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 4}, {Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
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
				var cur *oceanbase.Cursor
				var reader io.Reader = strings.NewReader("ab")
				if stage == publictest.LobOpenPrepare {
					reader = longReaderFunc(func([]byte) (int, error) {
						t.Error("read input after prepare invalidated the resource epoch")
						return 0, io.EOF
					})
				}
				_, err = oceanbase.Execute(ctx, tx, resourceOutEpochQuery, &oceanbase.LongData{Reader: reader, Length: -1}, oceanbase.CursorOut{Dest: &cur})
				if cur != nil || !errors.Is(err, oceanbase.ErrResource) {
					t.Fatal("did not reject invalidated pre-execution epoch", cur, err)
				}
				if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
					t.Fatal("pre-execution rejection damaged session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
