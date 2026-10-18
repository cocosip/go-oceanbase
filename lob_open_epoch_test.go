package oceanbase

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	lobOpenEpochQuery   = "SELECT lob FROM lob_open_epoch WHERE id=?"
	lobOpenEpochLiteral = "SELECT lob FROM lob_open_epoch WHERE id= 1 "
	lobOpenText         = "text"
	lobOpenClient       = "client"
	lobOpenServer       = "server"
	lobOpenCombined     = "combined"
	lobOpenPrepare      = "prepare"
	lobOpenMetadata     = "metadata"
	lobOpenFinal        = "final"
)

func lobOpenEpochConfig(strategy string) Config {
	cfg := Config{Mode: ModeOracle, PrepareMode: PrepareServer, Limits: Limits{CleanupTimeout: 200 * time.Millisecond}}
	if strategy == lobOpenClient {
		cfg.PrepareMode = PrepareClient
	}
	cfg.Execution.PrepareExecute = strategy == lobOpenCombined
	return cfg
}

func openEpochLOB(ctx context.Context, t *testing.T, session Session, typ byte, strategy string) error {
	t.Helper()
	query, args := lobOpenEpochQuery, []any{1}
	if strategy == lobOpenText {
		query, args = lobOpenEpochLiteral, nil
	}
	if typ == 210 {
		lob, err := OpenBlob(ctx, session, query, args...)
		if lob != nil {
			_ = lob.Close()
			t.Fatal("failed opening published a BLOB")
		}
		return err
	}
	lob, err := OpenClob(ctx, session, query, args...)
	if lob != nil {
		_ = lob.Close()
		t.Fatal("failed opening published a CLOB")
	}
	return err
}

func serveEpochLOBCommand(ctx context.Context, t *testing.T, p *wire.PacketIO, strategy string, prepareStatus byte) bool {
	t.Helper()
	switch strategy {
	case lobOpenText, lobOpenClient:
		return readQuery(ctx, t, p, lobOpenEpochLiteral)
	case lobOpenCombined:
		return readCommand(ctx, t, p, combinedQueryPacket(0, lobOpenEpochQuery, 1, false, false))
	default:
		if !servePrepareStatus(ctx, t, p, lobOpenEpochQuery, 95, 1, nil, prepareStatus) {
			return false
		}
		if prepareStatus == 4 {
			return true // The caller expects statement cleanup, without an execute command.
		}
		return readCommand(ctx, t, p, binary.LittleEndian.AppendUint64(append(executeHeader(95), 0, 1, 8, 0), 1))
	}
}

func TestNativeLOBOpenTransactionInvalidation(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, typ := range []byte{210, 211} {
			for _, strategy := range []string{lobOpenText, lobOpenClient, lobOpenServer, lobOpenCombined} {
				for _, stage := range []string{lobOpenPrepare, lobOpenMetadata, lobOpenFinal} {
					if stage == lobOpenPrepare && strategy != lobOpenServer {
						continue
					}
					t.Run(variant+"/"+map[byte]string{210: "blob", 211: "clob"}[typ]+"/"+strategy+"/"+stage, func(t *testing.T) {
						binaryResult := strategy == lobOpenServer || strategy == lobOpenCombined
						db := batchRewriteDB(t, lobOpenEpochConfig(strategy), variant, func(ctx context.Context, p *wire.PacketIO) {
							status := byte(5)
							if stage == lobOpenPrepare {
								status = 4
							}
							if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveEpochLOBCommand(ctx, t, p, strategy, status) {
								return
							}
							if stage != lobOpenPrepare {
								if strategy == lobOpenCombined && !sendPackets(ctx, t, p, combinedHeader(95, 1, 1, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 5, 0}) {
									return
								}
								metadataStatus, rowStatus := byte(5), byte(4)
								if stage == lobOpenMetadata {
									metadataStatus = 4
								}
								if strategy == lobOpenCombined && stage == lobOpenFinal {
									rowStatus = 5 // Only the native final OK ends the transaction.
								}
								if strategy != lobOpenCombined && !sendPackets(ctx, t, p, []byte{1}) {
									return
								}
								value := lobOutFixture(3)
								row := complexLenBytes(nil, value)
								if binaryResult {
									row = complexLenBytes([]byte{0, 0}, value)
								}
								if !sendPackets(ctx, t, p, textColumn("lob", typ, 45, 0), []byte{254, 0, 0, metadataStatus, 0}, row, []byte{254, 0, 0, rowStatus, 0}) {
									return
								}
								if strategy == lobOpenCombined && !sendPackets(ctx, t, p, commandPacket(0, 0, 4)) {
									return
								}
							}
							if binaryResult && !readCommand(ctx, t, p, stmtPacket(25, 95)) {
								return
							}
							if !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 4}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
						if err = openEpochLOB(ctx, t, tx, typ, strategy); !errors.Is(err, ErrResource) {
							t.Fatal("lost opening transaction invalidation", err)
						}
						if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
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

func TestNativeLOBOpenServerErrorCleanup(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, strategy := range []string{lobOpenServer, lobOpenCombined} {
			t.Run(variant+"/"+strategy, func(t *testing.T) {
				db := batchRewriteDB(t, lobOpenEpochConfig(strategy), variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveEpochLOBCommand(ctx, t, p, strategy, 6) || !sendPackets(ctx, t, p, []byte{255, 40, 4, '#', '4', '2', '0', '0', '0', 'n', 'o'}) {
						return
					}
					if strategy == lobOpenServer && !readCommand(ctx, t, p, stmtPacket(25, 95)) {
						return
					}
					if !serveOK(ctx, t, p, []byte{14}, 6) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				defer func() { _ = db.Close() }()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				session := pinnedSession(t, db)
				err := openEpochLOB(ctx, t, session, 210, strategy)
				var server *Error
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
