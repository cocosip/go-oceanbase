package catalog_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestBlobTargetRejections(t *testing.T) {
	for _, kind := range []string{"view", "generated", "key-column", "missing-key", "extra-key", "unsupported-key-type", publictest.TestUnknownEngine} {
		t.Run(kind, func(t *testing.T) {
			dict := publictest.TableDictionary()
			target := publictest.TableTarget()
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
			case publictest.TestUnknownEngine:
				dict[0][3] = publictest.TestNonLockingEngine
				dict[1][3] = publictest.TestNonLockingEngine
			}
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTableDictionary(ctx, t, p, 2, dict...) {
					return
				}
				if kind == publictest.TestUnknownEngine {
					if !publictest.ServeTableRows(ctx, t, p, 2, []string{publictest.TestOctetLength}, []byte{8}, []any{"1"}) {
						return
					}
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			pinned := publictest.PinnedSession(t, db)
			b, err := oceanbase.OpenTableBlob(context.Background(), pinned, target)
			if kind == publictest.TestUnknownEngine {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, dberrors.ErrUnsupported) {
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

func TestBlobTarget(t *testing.T) {
	var nc *sql.Conn
	var nt *sql.Tx
	for _, s := range []oceanbase.Session{nil, nc, nt, new(sql.DB), &publictest.ResourceSessionWrapper{}} {
		if _, err := oceanbase.OpenTableBlob(context.Background(), s, publictest.TableTarget()); !errors.Is(err, oceanbase.ErrResource) {
			t.Fatalf("%T: %v", s, err)
		}
	}
	t.Run("immutable-composite-key", func(t *testing.T) {
		v := &tableKeyValuer{value: []byte{1, 2}}
		db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
			if !publictest.ServeTableRows(ctx, t, p, 2, []string{"SELECT DATABASE()"}, []byte{253}, []any{"s"}) {
				return
			}
			dict := publictest.TableDictionary()
			dict = append(dict, []any{"s", "t", publictest.BaseTable, "InnoDB", "k`x", "varbinary", "", "2"})
			if !publictest.ServeTableDictionary(ctx, t, p, 2, dict...) {
				return
			}
			for range 2 {
				if !publictest.ServeTableRows(ctx, t, p, 2, []string{"OCTET_LENGTH(`b`)", "`s`.`t`", "`id` =  7 ", "`k``x` =  X'0102'"}, []byte{8}, []any{"3"}) {
					return
				}
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
		defer func() { _ = db.Close() }()
		pinned := publictest.PinnedSession(t, db)
		key := map[string]any{"id": 7, "k`x": v}
		b, err := oceanbase.OpenTableBlob(context.Background(), pinned, oceanbase.BlobTarget{Table: "t", Column: "B", Key: key})
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
