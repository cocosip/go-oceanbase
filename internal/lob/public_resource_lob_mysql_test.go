package lob_test

import (
	bytes "bytes"
	context "context"
	sql "database/sql"
	errors "errors"
	io "io"
	math "math"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTableBlobTx(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "write", true: "readonly"}[readonly], func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				start := publictest.TestMySQLReadWrite
				if readonly {
					start = "START TRANSACTION READ ONLY"
				}
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: start, Status: 3}}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) || !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"3"}) {
					return
				}
				if !readonly && !serveTableBlobMutations(ctx, t, p) {
					return
				}
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			ctx := context.Background()
			tr, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readonly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := oceanbase.OpenTableBlob(ctx, tr, publictest.TableTarget())
			if err != nil {
				t.Fatal(err)
			}
			if readonly {
				if _, err = b.WriteAtContext(ctx, []byte{1}, 0); !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal(err)
				}
				if err = b.Truncate(ctx, 0); !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal(err)
				}
			} else {
				if _, err = b.WriteAtContext(ctx, []byte{1}, 4); !errors.Is(err, oceanbase.ErrLOB) {
					t.Fatal(err)
				}
				if err = b.Truncate(ctx, 4); !errors.Is(err, oceanbase.ErrLOB) {
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
			if _, err = b.Size(ctx); !errors.Is(err, sql.ErrTxDone) && !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal(err)
			}
			if err = b.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobBounds(t *testing.T) {
	for _, kind := range []string{"missing", publictest.TestSQLNull, "empty", publictest.TestBlobRead} {
		t.Run(kind, func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTableDictionary(ctx, t, p, 2, publictest.TableDictionary()...) {
					return
				}
				var row [][]any
				switch kind {
				case publictest.TestSQLNull:
					row = [][]any{{nil}}
				case "empty":
					row = [][]any{{"0"}}
				case publictest.TestBlobRead:
					row = [][]any{{"20000000"}}
				}
				if !publictest.ServeTableRows(ctx, t, p, 2, []string{"OCTET_LENGTH(`b`)"}, []byte{8}, row...) {
					return
				}
				if kind == publictest.TestBlobRead {
					if !publictest.ServeTableRows(ctx, t, p, 2, []string{"SUBSTRING(`b`, 20000000 , 3 )"}, []byte{252}, []any{[]byte{255}}) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			b, err := oceanbase.OpenTableBlob(context.Background(), pinned, publictest.TableTarget())
			switch kind {
			case "missing":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			case publictest.TestSQLNull:
				if b != nil || err != nil {
					t.Fatal(b, err)
				}
			default:
				if err != nil || b == nil {
					t.Fatal(b, err)
				}
				if _, err = b.ReadAtContext(context.Background(), make([]byte, 2), math.MaxInt64); !errors.Is(err, oceanbase.ErrLOB) {
					t.Fatal(err)
				}
				if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, dberrors.ErrUnsupported) {
					t.Fatal(err)
				}
				if kind == publictest.TestBlobRead {
					buf := make([]byte, 3)
					n, e := b.ReadAtContext(context.Background(), buf, 19999999)
					if n != 1 || e != io.EOF || buf[0] != 255 {
						t.Fatal(n, buf, e)
					}
				}
				if err = b.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = b.Size(context.Background()); !errors.Is(err, oceanbase.ErrResource) {
					t.Fatal(err)
				}
			}
			_ = pinned.Close()
		})
	}
}

func TestTableBlobOpenTimeout(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTableDictionary(ctx, t, p, 2, publictest.TableDictionary()...) {
			return
		}
		if command := publictest.ReadTableCommand(ctx, t, p); !bytes.Contains(command, []byte(publictest.TestOctetLength)) {
			t.Errorf("bad open %q", command)
			return
		}
		p.ResetSequence()
		if data, err := p.ReadMessage(ctx); err == nil {
			t.Errorf("timeout replayed %x", data)
		}
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if b, err := oceanbase.OpenTableBlob(ctx, pinned, publictest.TableTarget()); b != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(b, err)
	}
	_ = pinned.Close()
}

func TestTableBlobChangedRow(t *testing.T) {
	for _, kind := range []string{"missing-row", publictest.TestBecameNull, "lock-error"} {
		t.Run(kind, func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) || !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"3"}) {
					return
				}
				if kind == "lock-error" {
					command := publictest.ReadTableCommand(ctx, t, p)
					if !bytes.Contains(command, []byte(publictest.TestForUpdate)) {
						t.Error("missing lock")
						return
					}
					if !testpeer.SendPackets(ctx, t, p, append([]byte{255, 185, 4, '#', 'H', 'Y', '0', '0', '0'}, []byte("lock wait timeout")...)) {
						return
					}
				} else {
					var rows [][]any
					if kind == publictest.TestBecameNull {
						rows = [][]any{{nil}}
					}
					if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestForUpdate}, []byte{8}, rows...) {
						return
					}
				}
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := oceanbase.OpenTableBlob(context.Background(), tr, publictest.TableTarget())
			if err != nil {
				t.Fatal(err)
			}
			_, err = b.WriteAtContext(context.Background(), []byte{1}, 0)
			switch kind {
			case "missing-row":
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			case publictest.TestBecameNull:
				if !errors.Is(err, oceanbase.ErrLOB) {
					t.Fatal(err)
				}
			default:
				var server *oceanbase.Error
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

func TestTableBlobWriteFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "cancel"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := publictest.TxDB(t, config.ModeMySQL, func(peerCtx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(peerCtx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServeTableDictionary(peerCtx, t, p, 3, publictest.TableDictionary()...) || !publictest.ServeTableRows(peerCtx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"0"}) || !publictest.ServeTableRows(peerCtx, t, p, 3, []string{publictest.TestForUpdate}, []byte{8}, []any{"0"}) {
					return
				}
				first := publictest.ReadTableCommand(peerCtx, t, p)
				if !bytes.Contains(first, []byte("UPDATE `s`.`t`")) {
					t.Errorf("unexpected update %q", first)
					return
				}
				if !cancelled {
					if !testpeer.SendPackets(peerCtx, t, p, []byte{0, 1, 0, 3, 0, 0, 0}) {
						return
					}
					second := publictest.ReadTableCommand(peerCtx, t, p)
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
			b, err := oceanbase.OpenTableBlob(context.Background(), tr, publictest.TableTarget())
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

func TestTableBlobChunks(t *testing.T) {
	connector, err := oceanbase.NewConnector(config.Config{User: publictest.TestUser, Mode: config.ModeMySQL, Limits: config.Limits{MaxLOBBytes: 8}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !publictest.ServeTableDictionary(ctx, t, p, 2, publictest.TableDictionary()...) || !publictest.ServeTableRows(ctx, t, p, 2, []string{publictest.TestOctetLength}, []byte{8}, []any{"20"}) {
			return
		}
		for _, part := range []struct{ offset, amount, data string }{{"1", "8", "abcdefgh"}, {"9", "8", "ijklmnop"}, {"17", "4", "qrst"}} {
			if !publictest.ServeTableRows(ctx, t, p, 2, []string{"SUBSTRING(`b`, " + part.offset + " , " + part.amount + " )"}, []byte{252}, []any{[]byte(part.data)}) {
				return
			}
		}
		if !publictest.ServeTableRows(ctx, t, p, 2, []string{publictest.TestOctetLength}, []byte{8}, []any{"21"}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenTableBlob(context.Background(), pinned, publictest.TableTarget())
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

func TestBlobTargetInput(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(_ context.Context, p *wire.PacketIO) { p.ResetSequence(); testpeer.ServeQuit(t, p) })
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	for _, change := range []func(*oceanbase.BlobTarget){
		func(t *oceanbase.BlobTarget) { t.Table = "" }, func(t *oceanbase.BlobTarget) { t.Column = "\x00" }, func(t *oceanbase.BlobTarget) { t.Schema = "\xff" }, func(t *oceanbase.BlobTarget) { t.Key = nil }, func(t *oceanbase.BlobTarget) { t.Key["id"] = nil }, func(t *oceanbase.BlobTarget) { t.Key["id"] = []byte(nil) }, func(t *oceanbase.BlobTarget) { t.Key["id"] = math.NaN() }, func(t *oceanbase.BlobTarget) { t.Key["id"] = sql.Out{Dest: new(int64)} },
	} {
		target := publictest.TableTarget()
		change(&target)
		if _, err := oceanbase.OpenTableBlob(context.Background(), pinned, target); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := oceanbase.OpenTableBlob(ctx, pinned, publictest.TableTarget()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_ = pinned.Close()
}

func TestTableBlobOracle(t *testing.T) {
	for _, keyType := range []string{valueformat.TypeNameNumber, valueformat.TypeNameChar, valueformat.TypeNameDate} {
		t.Run(keyType, func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTableRows(ctx, t, p, 6, []string{"SYS_CONTEXT('USERENV','CURRENT_SCHEMA')"}, []byte{253}, []any{"S"}) {
					return
				}
				if !publictest.ServeTableRows(ctx, t, p, 6, []string{"ALL_TABLES", "ALL_OBJECTS", "ALL_TAB_COLS", "ALL_CONSTRAINTS", "ALL_CONS_COLUMNS"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"S", "T", publictest.BaseTable, nil, "ID", keyType, "NO", "1"}, []any{"S", "T", publictest.BaseTable, nil, "B\"?", valueformat.TypeNameBlob, "NO", nil}) {
					return
				}
				if !publictest.ServeLOBQuery(ctx, t, p, `SELECT "B""?" FROM "S"."T" WHERE "ID" =  7 `, 210, publictest.LobInlineFixture("ok")) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			b, err := oceanbase.OpenTableBlob(context.Background(), pinned, oceanbase.BlobTarget{Table: "T", Column: "B\"?", Key: map[string]any{"ID": 7}})
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
