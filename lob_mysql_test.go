package oceanbase

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testNonLockingEngine = "MyISAM"
	testBlobRead         = "read"
	testBecameNull       = "became-null"
	testSQLNull          = "null-value"
	testUnknownEngine    = "unknown-engine"
	testOctetLength      = "OCTET_LENGTH"
	testForUpdate        = "FOR UPDATE"
)

type tableKeyValuer struct {
	calls atomic.Int32
	value []byte
}

func TestTableBlobOracle(t *testing.T) {
	for _, keyType := range []string{typeNameNumber, typeNameChar, typeNameDate} {
		t.Run(keyType, func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTableRows(ctx, t, p, 6, []string{"SYS_CONTEXT('USERENV','CURRENT_SCHEMA')"}, []byte{253}, []any{"S"}) {
					return
				}
				if !serveTableRows(ctx, t, p, 6, []string{"ALL_TABLES", "ALL_OBJECTS", "ALL_TAB_COLUMNS", "ALL_CONSTRAINTS", "ALL_CONS_COLUMNS"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"S", "T", blobBaseTable, nil, "ID", keyType, "NO", "1"}, []any{"S", "T", blobBaseTable, nil, "B\"?", typeNameBlob, "NO", nil}) {
					return
				}
				if !serveLOBQuery(ctx, t, p, `SELECT "B""?" FROM "S"."T" WHERE "ID" =  7 `, 210, lobInlineFixture("ok")) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			b, err := OpenTableBlob(context.Background(), pinned, BlobTarget{Table: "T", Column: "B\"?", Key: map[string]any{"ID": 7}})
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 2)
			if n, err := b.ReadAtContext(context.Background(), data, 0); n != 2 || err != nil || string(data) != "ok" {
				t.Fatal(n, data, err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
			_ = pinned.Close()
		})
	}
}

func TestBlobTargetInput(t *testing.T) {
	db := txDB(t, ModeMySQL, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); serveQuit(t, p) })
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	for _, change := range []func(*BlobTarget){
		func(t *BlobTarget) { t.Table = "" }, func(t *BlobTarget) { t.Column = "\x00" }, func(t *BlobTarget) { t.Schema = "\xff" }, func(t *BlobTarget) { t.Key = nil }, func(t *BlobTarget) { t.Key["id"] = nil }, func(t *BlobTarget) { t.Key["id"] = []byte(nil) }, func(t *BlobTarget) { t.Key["id"] = math.NaN() }, func(t *BlobTarget) { t.Key["id"] = sql.Out{Dest: new(int64)} },
	} {
		target := tableTarget()
		change(&target)
		if _, err := OpenTableBlob(context.Background(), pinned, target); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenTableBlob(ctx, pinned, tableTarget()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestTableBlobChunks(t *testing.T) {
	connector, err := NewConnector(Config{User: testUser, Mode: ModeMySQL, Limits: Limits{MaxLOBBytes: 8}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !serveTableDictionary(ctx, t, p, 2, tableDictionary()...) || !serveTableRows(ctx, t, p, 2, []string{testOctetLength}, []byte{8}, []any{"20"}) {
			return
		}
		for _, part := range []struct{ offset, amount, data string }{{"1", "8", "abcdefgh"}, {"9", "8", "ijklmnop"}, {"17", "4", "qrst"}} {
			if !serveTableRows(ctx, t, p, 2, []string{"SUBSTRING(`b`, " + part.offset + " , " + part.amount + " )"}, []byte{252}, []any{[]byte(part.data)}) {
				return
			}
		}
		if !serveTableRows(ctx, t, p, 2, []string{testOctetLength}, []byte{8}, []any{"21"}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	b, err := OpenTableBlob(context.Background(), pinned, tableTarget())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 20)
	if n, err := b.ReadAtContext(context.Background(), buf, 0); n != 20 || err != nil || string(buf) != "abcdefghijklmnopqrst" {
		t.Fatal(n, buf, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = b.ReadAtContext(ctx, buf, 0); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if size, err := b.Size(context.Background()); err != nil || size != 21 {
		t.Fatal(size, err)
	}
	_ = b.Close()
	_ = pinned.Close()
}

func TestTableBlobWriteFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "cancel"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := txDB(t, ModeMySQL, func(peerCtx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(peerCtx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveTableDictionary(peerCtx, t, p, 3, tableDictionary()...) || !serveTableRows(peerCtx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"0"}) || !serveTableRows(peerCtx, t, p, 3, []string{testForUpdate}, []byte{8}, []any{"0"}) {
					return
				}
				first := readTableCommand(peerCtx, t, p)
				if !bytes.Contains(first, []byte("UPDATE `s`.`t`")) {
					t.Errorf("unexpected update %q", first)
					return
				}
				if !cancelled {
					if !sendPackets(peerCtx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) {
						return
					}
					second := readTableCommand(peerCtx, t, p)
					if !bytes.Contains(second, []byte("LEFT(`b`, 32767 )")) {
						t.Errorf("unexpected second block %q", second)
					}
					return
				}
				cancel()
				p.ResetSequence()
				if data, err := p.ReadMessage(peerCtx); err == nil {
					t.Errorf("canceled block replayed: %x", data)
				}
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := OpenTableBlob(context.Background(), tr, tableTarget())
			if err != nil {
				t.Fatal(err)
			}
			n, err := b.WriteAtContext(ctx, make([]byte, 32768), 0)
			if cancelled {
				if n != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal(n, err)
				}
			} else if n != 32767 || err == nil {
				t.Fatal(n, err)
			}
			if _, err = b.Size(context.Background()); err == nil {
				t.Fatal("broken resource remained usable")
			}
		})
	}
}

func TestTableBlobChangedRow(t *testing.T) {
	for _, kind := range []string{"missing-row", testBecameNull, "lock-error"} {
		t.Run(kind, func(t *testing.T) {
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) || !serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"3"}) {
					return
				}
				if kind == "lock-error" {
					command := readTableCommand(ctx, t, p)
					if !bytes.Contains(command, []byte(testForUpdate)) {
						t.Error("missing lock")
						return
					}
					if !sendPackets(ctx, t, p, append([]byte{255, 185, 4, '#', 'H', 'Y', '0', '0', '0'}, []byte("lock wait timeout")...)) {
						return
					}
				} else {
					var rows [][]any
					if kind == testBecameNull {
						rows = [][]any{{nil}}
					}
					if !serveTableRows(ctx, t, p, 3, []string{testForUpdate}, []byte{8}, rows...) {
						return
					}
				}
				if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := OpenTableBlob(context.Background(), tr, tableTarget())
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.WriteAtContext(context.Background(), []byte{1}, 0)
			switch kind {
			case "missing-row":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			case testBecameNull:
				if !errors.Is(err, ErrLOB) {
					t.Fatal(err)
				}
			default:
				var server *Error
				if !errors.As(err, &server) || server.Code != 1209 {
					t.Fatal(err)
				}
			}
			_ = b.Close()
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobOpenTimeout(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTableDictionary(ctx, t, p, 2, tableDictionary()...) {
			return
		}
		if command := readTableCommand(ctx, t, p); !bytes.Contains(command, []byte(testOctetLength)) {
			t.Errorf("bad open %q", command)
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("timeout replayed %x", data)
		}
	})
	defer func() { _ = db.Close() }()
	pinned := pinnedSession(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if b, err := OpenTableBlob(ctx, pinned, tableTarget()); b != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(b, err)
	}
	_ = pinned.Close()
}

func (v *tableKeyValuer) Value() (driver.Value, error) { v.calls.Add(1); return v.value, nil }

func readTableCommand(ctx context.Context, t *testing.T, p *wire.PacketIO) []byte {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil {
		t.Error(err)
		return nil
	}
	return b
}

func serveTableRows(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, contains []string, types []byte, rows ...[]any) bool {
	t.Helper()
	command := readTableCommand(ctx, t, p)
	if len(command) == 0 || command[0] != 3 {
		t.Errorf("expected table SQL, got %x", command)
		return false
	}
	for _, part := range contains {
		if !strings.Contains(string(command[1:]), part) {
			t.Errorf("query %q missing %q", command[1:], part)
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{byte(len(types))}) {
		return false
	}
	for _, typ := range types {
		if !sendPackets(ctx, t, p, textColumn("v", typ, 63, 0)) {
			return false
		}
	}
	if !sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	for _, values := range rows {
		var row []byte
		for _, value := range values {
			if value == nil {
				row = append(row, 251)
				continue
			}
			var b []byte
			switch v := value.(type) {
			case string:
				b = []byte(v)
			case []byte:
				b = v
			default:
				t.Fatalf("bad table fixture %T", value)
			}
			row = append(row, lobLengthPrefix(len(b))...)
			row = append(row, b...)
		}
		if !sendPackets(ctx, t, p, row) {
			return false
		}
	}
	return sendPackets(ctx, t, p, []byte{254, 0, 0, status, 0})
}

func serveTableDictionary(ctx context.Context, t *testing.T, p *wire.PacketIO, status byte, rows ...[]any) bool {
	return serveTableRows(ctx, t, p, status, []string{"information_schema.TABLES", "information_schema.COLUMNS", "information_schema.KEY_COLUMN_USAGE"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, rows...)
}
func tableDictionary() [][]any {
	return [][]any{{"s", "t", blobBaseTable, blobLockingEngine, "id", "bigint", "", "1"}, {"s", "t", blobBaseTable, blobLockingEngine, "b", "longblob", "", nil}}
}
func tableTarget() BlobTarget {
	return BlobTarget{Schema: "s", Table: "t", Column: "b", Key: map[string]any{"id": int64(7)}}
}

func TestBlobTarget(t *testing.T) {
	var nc *sql.Conn
	var nt *sql.Tx
	for _, s := range []Session{nil, nc, nt, new(sql.DB), &resourceSessionWrapper{}} {
		if _, err := OpenTableBlob(context.Background(), s, tableTarget()); !errors.Is(err, ErrResource) {
			t.Fatalf("%T: %v", s, err)
		}
	}
	t.Run("immutable-composite-key", func(t *testing.T) {
		v := &tableKeyValuer{value: []byte{1, 2}}
		db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
			if !serveTableRows(ctx, t, p, 2, []string{"SELECT DATABASE()"}, []byte{253}, []any{"s"}) {
				return
			}
			dict := tableDictionary()
			dict = append(dict, []any{"s", "t", blobBaseTable, blobLockingEngine, "k`x", "varbinary", "", "2"})
			if !serveTableDictionary(ctx, t, p, 2, dict...) {
				return
			}
			for range 2 {
				if !serveTableRows(ctx, t, p, 2, []string{"OCTET_LENGTH(`b`)", "`s`.`t`", "`id` =  7 ", "`k``x` =  X'0102'"}, []byte{8}, []any{"3"}) {
					return
				}
			}
			p.ResetSequence()
			serveQuit(t, p)
		})
		defer func() { _ = db.Close() }()
		pinned := pinnedSession(t, db)
		key := map[string]any{"id": 7, "k`x": v}
		b, err := OpenTableBlob(context.Background(), pinned, BlobTarget{Table: "t", Column: "B", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		key["id"] = 99
		delete(key, "k`x")
		v.value[0] = 255
		if n, err := b.Size(context.Background()); err != nil || n != 3 {
			t.Fatal(n, err)
		}
		if v.calls.Load() != 1 {
			t.Fatal("Valuer calls", v.calls.Load())
		}
		if err = b.Close(); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
	})
}

func TestBlobTargetRejections(t *testing.T) {
	for _, kind := range []string{"view", "generated", "key-column", "missing-key", "extra-key", "unsupported-key-type", testUnknownEngine} {
		t.Run(kind, func(t *testing.T) {
			dict := tableDictionary()
			target := tableTarget()
			switch kind {
			case "view":
				for _, r := range dict {
					r[2] = "VIEW"
				}
			case "generated":
				dict[1][6] = "STORED GENERATED"
			case "key-column":
				target.Column = "id"
			case "missing-key":
				target.Key = map[string]any{"other": 1}
			case "extra-key":
				target.Key["other"] = 1
			case "unsupported-key-type":
				dict[0][5] = "json"
			case testUnknownEngine:
				dict[0][3] = testNonLockingEngine
				dict[1][3] = testNonLockingEngine
			}
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTableDictionary(ctx, t, p, 2, dict...) {
					return
				}
				if kind == testUnknownEngine {
					if !serveTableRows(ctx, t, p, 2, []string{testOctetLength}, []byte{8}, []any{"1"}) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			b, err := OpenTableBlob(context.Background(), pinned, target)
			if kind == testUnknownEngine {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
				_ = b.Close()
			} else if err == nil {
				t.Fatal("unsafe target accepted")
			}
			_ = pinned.Close()
		})
	}
}

func TestTableBlobBounds(t *testing.T) {
	for _, kind := range []string{"missing", testSQLNull, "empty", testBlobRead} {
		t.Run(kind, func(t *testing.T) {
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTableDictionary(ctx, t, p, 2, tableDictionary()...) {
					return
				}
				var row [][]any
				switch kind {
				case testSQLNull:
					row = [][]any{{nil}}
				case "empty":
					row = [][]any{{"0"}}
				case testBlobRead:
					row = [][]any{{"20000000"}}
				}
				if !serveTableRows(ctx, t, p, 2, []string{"OCTET_LENGTH(`b`)"}, []byte{8}, row...) {
					return
				}
				if kind == testBlobRead {
					if !serveTableRows(ctx, t, p, 2, []string{"SUBSTRING(`b`, 20000000 , 3 )"}, []byte{252}, []any{[]byte{255}}) {
						return
					}
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := pinnedSession(t, db)
			b, err := OpenTableBlob(context.Background(), pinned, tableTarget())
			switch kind {
			case "missing":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			case testSQLNull:
				if b != nil || err != nil {
					t.Fatal(b, err)
				}
			default:
				if err != nil || b == nil {
					t.Fatal(b, err)
				}
				if _, err = b.ReadAtContext(context.Background(), make([]byte, 2), math.MaxInt64); !errors.Is(err, ErrLOB) {
					t.Fatal(err)
				}
				if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
				if kind == testBlobRead {
					buf := make([]byte, 3)
					n, e := b.ReadAtContext(context.Background(), buf, 19999999)
					if n != 1 || e != io.EOF || buf[0] != 255 {
						t.Fatal(n, buf, e)
					}
				}
				if err = b.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = b.Size(context.Background()); !errors.Is(err, ErrResource) {
					t.Fatal(err)
				}
			}
			_ = pinned.Close()
		})
	}
}

func TestTableBlobTx(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "readonly"}[readonly], func(t *testing.T) {
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				start := testMySQLReadWrite
				if readonly {
					start = "START TRANSACTION READ ONLY"
				}
				if !serveTxSteps(ctx, t, p, []txStep{{start, 3}}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) || !serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"3"}) {
					return
				}
				if !readonly && !serveTableBlobMutations(ctx, t, p) {
					return
				}
				if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			tr, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readonly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := OpenTableBlob(ctx, tr, tableTarget())
			if err != nil {
				t.Fatal(err)
			}
			if readonly {
				if _, err = b.WriteAtContext(ctx, []byte{1}, 0); !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
				if err = b.Truncate(ctx, 0); !errors.Is(err, ErrUnsupported) {
					t.Fatal(err)
				}
			} else {
				if _, err = b.WriteAtContext(ctx, []byte{1}, 4); !errors.Is(err, ErrLOB) {
					t.Fatal(err)
				}
				if err = b.Truncate(ctx, 4); !errors.Is(err, ErrLOB) {
					t.Fatal(err)
				}
				if n, err := b.WriteAtContext(ctx, []byte("abc"), 0); err != nil || n != 3 {
					t.Fatal(n, err)
				}
				if n, err := b.WriteAtContext(ctx, []byte("d"), 3); err != nil || n != 1 {
					t.Fatal(n, err)
				}
				if n, err := b.WriteAtContext(ctx, nil, 4); err != nil || n != 0 {
					t.Fatal(n, err)
				}
				if err = b.Truncate(ctx, 2); err != nil {
					t.Fatal(err)
				}
			}
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Size(ctx); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, ErrResource) {
				t.Fatal(err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func serveTableBlobMutations(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	// Sparse writes and extension attempts still lock and confirm the actual length.
	for range 2 {
		if !serveTableRows(ctx, t, p, 3, []string{testOctetLength, testForUpdate}, []byte{8}, []any{"3"}) {
			return false
		}
	}
	for _, size := range []string{"3", "3", "4", "4"} {
		if !serveTableRows(ctx, t, p, 3, []string{testOctetLength, testForUpdate}, []byte{8}, []any{size}) {
			return false
		}
		if size == "3" {
			command := readTableCommand(ctx, t, p)
			if !bytes.Contains(command, []byte("CONCAT(LEFT(`b`,")) {
				t.Errorf("unexpected mutation %q", command)
				return false
			}
			if !sendPackets(ctx, t, p, []byte{0, 0, 0, 3, 0, 0, 0}) {
				return false
			}
		}
		if size == "4" {
			break
		}
	}
	if !serveTableRows(ctx, t, p, 3, []string{testOctetLength, testForUpdate}, []byte{8}, []any{"4"}) {
		return false
	}
	command := readTableCommand(ctx, t, p)
	if !bytes.Contains(command, []byte("LEFT(`b`, 2 )")) {
		t.Errorf("unexpected trim %q", command)
		return false
	}
	if !sendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) {
		return false
	}
	return true
}
