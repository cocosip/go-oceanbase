package execution_test

import (
	context "context"
	errors "errors"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestNativeLOBOpenServerErrorCleanup(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, strategy := range []string{publictest.LobOpenServer, publictest.LobOpenCombined} {
			t.Run(variant+"/"+strategy, func(t *testing.T) {
				db := publictest.BatchRewriteDB(t, lobOpenEpochConfig(strategy), variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveEpochLOBCommand(ctx, t, p, strategy, 6) || !testpeer.SendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
						return
					}
					if strategy == publictest.LobOpenServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 95)) {
						return
					}
					if !testpeer.ServeOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					testpeer.ServeQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				session := publictest.PinnedSession(t, db)
				err := openEpochLOB(ctx, t, session, 210, strategy)
				var server *oceanbase.Error
				if !errors.As(err, &server) || server.Code != 1064 || errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("lost complete server error", err)
				}
				if err = session.PingContext(ctx); err != nil {
					t.Fatal("server error cleanup damaged original session", err)
				}
			})
		}
	}
}

func TestNativeLOBOpenTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{publictest.TestCursorEpochPlain, publictest.TestCompression, publictest.TestOB20} {
		for _, typ := range []byte{210, 211} {
			for _, strategy := range []string{publictest.LobOpenText, publictest.LobOpenClient, publictest.LobOpenServer, publictest.LobOpenCombined} {
				for _, stage := range []string{publictest.LobOpenPrepare, publictest.LobOpenMetadata, publictest.LobOpenFinal} {
					if stage == publictest.LobOpenPrepare && strategy != publictest.LobOpenServer {
						continue
					}
					t.Run(variant+"/"+map[byte]string{210: "blob", 211: "clob"}[typ]+"/"+strategy+"/"+stage, func(t *testing.T) {
						binaryResult := strategy == publictest.LobOpenServer || strategy == publictest.LobOpenCombined
						db := publictest.BatchRewriteDB(t, lobOpenEpochConfig(strategy), variant, func(ctx context.Context, p *wire.PacketIO) {
							status := byte(5)
							if stage == publictest.LobOpenPrepare {
								status = 4
							}
							if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: publictest.TestOracleReadWrite, Status: 5}}) || !serveEpochLOBCommand(ctx, t, p, strategy, status) {
								return
							}
							if stage != publictest.LobOpenPrepare {
								if strategy == publictest.LobOpenCombined && !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(95, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 5, 0}) {
									return
								}
								metadataStatus, rowStatus := byte(5), byte(4)
								if stage == publictest.LobOpenMetadata {
									metadataStatus = 4
								}
								if strategy == publictest.LobOpenCombined && stage == publictest.LobOpenFinal {
									rowStatus = 5 // Only the native final OK ends the transaction.
								}
								if strategy != publictest.LobOpenCombined && !testpeer.SendPackets(ctx, t, p, []byte{1}) {
									return
								}
								value := publictest.LobOutFixture(3)
								row := wire.AppendComplexLenBytes(nil, value)
								if binaryResult {
									row = wire.AppendComplexLenBytes([]byte{0, 0}, value)
								}
								if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("lob", typ, 45, 0), []byte{254, 0, 0, metadataStatus, 0}, row, []byte{254, 0, 0, rowStatus, 0}) {
									return
								}
								if strategy == publictest.LobOpenCombined && !testpeer.SendPackets(ctx, t, p, publictest.CommandPacket(0, 0, 4)) {
									return
								}
							}
							if binaryResult && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 95)) {
								return
							}
							if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TableBlobEpochPingSQL, Status: 4}, {Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
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
						if err = openEpochLOB(ctx, t, tx, typ, strategy); !errors.Is(err, oceanbase.ErrResource) {
							t.Fatal("lost opening transaction invalidation", err)
						}
						if _, err = tx.ExecContext(ctx, publictest.TableBlobEpochPingSQL); err != nil {
							t.Fatal("complete opening response damaged session", err)
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
