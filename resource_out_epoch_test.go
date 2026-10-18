package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const resourceOutEpochQuery = "CALL resource_epoch(?,?)"

func serveEpochResourceOut(ctx context.Context, t *testing.T, p *wire.PacketIO, typ, ordinary byte, metadata bool, tail byte) bool {
	t.Helper()
	payload, err := wire.EncodePrepareExecute(0, []byte(resourceOutEpochQuery), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
	if err != nil {
		t.Error(err)
		return false
	}
	if !readCommand(ctx, t, p, payload) {
		return false
	}
	flags, count := byte(4), uint16(2)
	if ordinary != 0 {
		flags, count = 0, 1
	}
	if !sendPackets(ctx, t, p, combinedHeader(96, count, 2, flags), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}) {
		return false
	}
	if ordinary != 0 {
		column := textColumn("ordinary", ordinary, 45, 0)
		row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 7)
		switch ordinary {
		case 163:
			row = binary.LittleEndian.AppendUint32([]byte{0, 0}, 98)
		case 210, 211:
			row = complexLenBytes([]byte{0, 0}, lobOutFixture(3))
		case 160:
			column, row = complexColumnFixture(7), complexLenBytes([]byte{0, 0, 0}, lobOutFixture(3))
		}
		if !sendPackets(ctx, t, p, column, []byte{254, 0, 0, 5, 0}, row, []byte{254, 0, 0, 5, 0}, commandPacket(0, 0, 13), []byte{2}) {
			return false
		}
	}
	status := byte(5)
	if metadata {
		status = 4
	}
	endStatus := status
	if ordinary != 0 {
		endStatus |= 8
	}
	row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
	switch typ {
	case 163:
		row = binary.LittleEndian.AppendUint32(row, 97)
	case 8:
		row = binary.LittleEndian.AppendUint64(row, 9)
	default:
		row = complexLenBytes(row, lobOutFixture(3))
	}
	return sendPackets(ctx, t, p, textColumn("scalar", 8, 63, 0), textColumn("resource", typ, 45, 0), []byte{254, 0, 0, status, 16}, row, []byte{254, 0, 0, endStatus, 0}, commandPacket(0, 0, tail))
}

func TestResourceOutBeforeExecutionInvalidation(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, stage := range []string{lobOpenPrepare, "upload"} {
			t.Run(variant+"/"+stage, func(t *testing.T) {
				db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					status := byte(5)
					if stage == lobOpenPrepare {
						status = 4
					}
					if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !servePrepareStatus(ctx, t, p, resourceOutEpochQuery, 96, 2, nil, status) {
						return
					}
					if stage == "upload" && (!readCommand(ctx, t, p, longDataPacket(ModeOracle, 96, 0, 3, []byte("6162"))) || !sendPackets(ctx, t, p, commandPacket(0, 0, 4))) {
						return
					}
					if !readCommand(ctx, t, p, stmtPacket(25, 96)) || !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 4}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
				var cur *Cursor
				var reader io.Reader = strings.NewReader("ab")
				if stage == lobOpenPrepare {
					reader = longReaderFunc(func([]byte) (int, error) {
						t.Error("read input after prepare invalidated the resource epoch")
						return 0, io.EOF
					})
				}
				_, err = Execute(ctx, tx, resourceOutEpochQuery, &LongData{Reader: reader, Length: -1}, CursorOut{Dest: &cur})
				if cur != nil || !errors.Is(err, ErrResource) {
					t.Fatal("did not reject invalidated pre-execution epoch", cur, err)
				}
				if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
					t.Fatal("pre-execution rejection damaged session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestScalarOutTransactionEnd(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		t.Run(variant, func(t *testing.T) {
			db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveEpochResourceOut(ctx, t, p, 8, 0, false, 4) || !readCommand(ctx, t, p, stmtPacket(25, 96)) || !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 4}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
			var a, b int64
			e, err := Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &a}, sql.Out{Dest: &b})
			if err != nil || a != 42 || b != 9 || !e.Snapshot().Done || e.Snapshot().Err != nil {
				t.Fatal("scalar OUT rejected a completed transaction", a, b, err)
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

func TestResourceOutEncodingInvalidation(t *testing.T) {
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, invalid := range []bool{false, true} {
			t.Run(variant+"/"+map[bool]string{false: "valid", true: "invalid"}[invalid], func(t *testing.T) {
				db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveComplexDictionary(ctx, t, p, []string{"N"}) || !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 6}, {testRollbackSQL, 6}}) {
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
				input := Object{Schema: "S", Type: "T", Fields: []Attribute{{Name: "N", Value: Number("7")}}}
				if invalid {
					input.Fields = nil
				}
				var cur *Cursor
				_, err = Execute(ctx, tx, resourceOutEpochQuery, input, CursorOut{Dest: &cur})
				if cur != nil || !errors.Is(err, ErrResource) || invalid && !errors.Is(err, ErrComplex) {
					t.Fatal("lost encoding error or epoch invalidation", cur, err)
				}
				if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
					t.Fatal("encoding rejection damaged session", err)
				}
				if err = tx.Rollback(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestComplexResourceOutTransactionEnd(t *testing.T) {
	const descriptorStage = "descriptor-response"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, stage := range []string{lobOpenFinal, descriptorStage} {
			t.Run(variant+"/"+stage, func(t *testing.T) {
				db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
					payload, err := wire.EncodePrepareExecute(0, []byte(resourceOutEpochQuery), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
					if err != nil {
						t.Error(err)
						return
					}
					status := byte(4)
					if stage == descriptorStage {
						status = 5
					}
					if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !readCommand(ctx, t, p, payload) || !sendPackets(ctx, t, p, combinedHeader(96, 2, 2, 4), textColumn("p", 6, 63, 0), textColumn("p", 6, 63, 0), []byte{254, 0, 0, 5, 0}, complexColumnFixture(7), textColumn("resource", 163, 63, 0), []byte{254, 0, 0, 5, 16}, []byte{0, 0, 0, 1, '7', 97, 0, 0, 0}, []byte{254, 0, 0, 5, 0}, commandPacket(0, 0, status)) {
						return
					}
					if stage == descriptorStage && !serveComplexDictionary(ctx, t, p, []string{"N"}) {
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
				v := Object{Type: testComplexUnchanged}
				var cur *Cursor
				_, err = Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &v}, CursorOut{Dest: &cur})
				if !errors.Is(err, ErrResource) || cur != nil || v.Type != testComplexUnchanged {
					t.Fatal("stale complex output published values", v, cur, err)
				}
			})
		}
	}
}

func TestBufferedResourceOutTransactionEnd(t *testing.T) {
	const nestedLOBStage = "nested-lob"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, typ := range []byte{163, 210, 211, 160} {
			for _, stage := range []string{lobOpenFinal, nestedLOBStage} {
				if stage == nestedLOBStage && typ != 160 {
					continue
				}
				t.Run(variant+"/"+map[byte]string{163: "cursor", 210: typeNameBlob, 211: typeNameClob, 160: typeNameObject}[typ]+"/"+stage, func(t *testing.T) {
					db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
						tail := byte(4)
						if stage == nestedLOBStage {
							tail = 5
						}
						if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveEpochResourceOut(ctx, t, p, 163, typ, false, tail) {
							return
						}
						if stage == nestedLOBStage && !serveComplexDictionaryFields(ctx, t, p, "T", []Attribute{{Name: "B", Type: typeNameBlob}}) {
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
					var cur *Cursor
					var value int64 = 7
					e, err := Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &value}, CursorOut{Dest: &cur})
					if !errors.Is(err, ErrResource) || cur != nil || value != 7 || e == nil {
						t.Fatal("stale buffered response lost failure", value, cur, err)
					}
					if !e.Snapshot().Done || !errors.Is(e.Snapshot().Err, ErrResource) {
						t.Fatal("buffered response snapshot lost failure", e.Snapshot())
					}
				})
			}
		}
	}
}

func TestResourceOutTransactionInvalidation(t *testing.T) {
	const immediate, consume, early = "immediate", "consume", "early"
	for _, variant := range []string{testCursorEpochPlain, testCompression, testOB20} {
		for _, typ := range []byte{163, 210, 211} {
			for _, style := range []string{immediate, consume, early} {
				for _, metadata := range []bool{false, true} {
					name := variant + "/" + map[byte]string{163: "cursor", 210: typeNameBlob, 211: typeNameClob}[typ] + "/" + style + "/" + map[bool]string{false: lobOpenFinal, true: lobOpenMetadata}[metadata]
					t.Run(name, func(t *testing.T) {
						db := batchRewriteDB(t, Config{Mode: ModeOracle}, variant, func(ctx context.Context, p *wire.PacketIO) {
							if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {testOracleReadWrite, 5}}) || !serveEpochResourceOut(ctx, t, p, typ, map[bool]byte{false: 0, true: 8}[style != immediate], metadata, 4) {
								return
							}
							if typ == 163 && !readCommand(ctx, t, p, stmtPacket(25, 97)) {
								return
							}
							if !readCommand(ctx, t, p, stmtPacket(25, 96)) || !serveTxSteps(ctx, t, p, []txStep{{tableBlobEpochPingSQL, 4}, {testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
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
						var cur *Cursor
						var blob *Blob
						var clob *Clob
						var value int64 = 7
						var output any = CursorOut{Dest: &cur}
						switch typ {
						case 210:
							output = Out{Dest: &blob, Type: typeNameBlob}
						case 211:
							output = Out{Dest: &clob, Type: typeNameClob}
						}
						e, err := Execute(ctx, tx, resourceOutEpochQuery, sql.Out{Dest: &value}, output)
						if style != immediate && err != nil {
							t.Fatal("ordinary rows failed before finalization", err)
						}
						if style == consume {
							checkResourceOutOrdinaryRow(t, e.Rows())
						}
						if e != nil {
							err = errors.Join(err, e.Close())
							if !e.Snapshot().Done || !errors.Is(e.Snapshot().Err, ErrResource) {
								t.Error("execution snapshot lost epoch failure", e.Snapshot())
							}
						}
						if value != 7 || cur != nil || blob != nil || clob != nil || !errors.Is(err, ErrResource) {
							_ = cur.Close()
							_ = blob.Close()
							_ = clob.Close()
							t.Fatalf("stale OUT publication: scalar=%d cursor=%v blob=%v clob=%v err=%v", value, cur, blob, clob, err)
						}
						if _, err = tx.ExecContext(ctx, tableBlobEpochPingSQL); err != nil {
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

func checkResourceOutOrdinaryRow(t *testing.T, rows *sql.Rows) {
	t.Helper()
	var n int64
	if !rows.Next() || rows.Scan(&n) != nil || n != 7 || rows.Next() {
		t.Fatal("ordinary row changed", n, rows.Err())
	}
}
