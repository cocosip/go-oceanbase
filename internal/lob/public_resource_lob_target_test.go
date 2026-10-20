package lob_test

import (
	context "context"
	sql "database/sql"
	errors "errors"
	math "math"
	strconv "strconv"
	strings "strings"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestBlobTargetDefaultExpression(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		dict := publictest.TableDictionary()
		dict[1][6] = "DEFAULT_GENERATED"
		if !publictest.ServeTableDictionary(ctx, t, p, 2, dict...) || !publictest.ServeTableRows(ctx, t, p, 2, []string{publictest.TestOctetLength}, []byte{8}, []any{"0"}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := publictest.PinnedSession(t, db)
	b, err := oceanbase.OpenTableBlob(context.Background(), c, publictest.TableTarget())
	if err != nil {
		t.Fatal("default expression is not a generated column", err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestTableBlobTxUnknownEngine(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) {
			return
		}
		dict := publictest.TableDictionary()
		for _, row := range dict {
			row[3] = publictest.TestNonLockingEngine
		}
		if !publictest.ServeTableDictionary(ctx, t, p, 3, dict...) || !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"3"}) {
			return
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
	if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal(err)
	}
	_ = b.Close()
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestTableBlobOracleTxLock(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "readonly"}[readonly], func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				start := publictest.TestOracleReadWrite
				if readonly {
					start = "SET TRANSACTION READ ONLY"
				}
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestAutocommitOff, Status: 4}, {Query: start, Status: 5}}) {
					return
				}
				if !publictest.ServeTableRows(ctx, t, p, 5, []string{"ALL_TABLES"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"S", "T", publictest.BaseTable, nil, "ID", valueformat.TypeNameNumber, "NO", "1"}, []any{"S", "T", publictest.BaseTable, nil, "B", valueformat.TypeNameBlob, "NO", nil}) {
					return
				}
				query := `SELECT "B" FROM "S"."T" WHERE "ID" =  7 `
				if !readonly {
					query += " FOR UPDATE"
				}
				if !publictest.ServeLOBQueryStatus(ctx, t, p, query, 210, 5, publictest.LobInlineFixture("ok")) {
					return
				}
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 4}, {Query: publictest.TestAutocommitOn, Status: 6}}) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: readonly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := oceanbase.OpenTableBlob(context.Background(), tr, oceanbase.BlobTarget{Schema: "S", Table: "T", Column: "B", Key: map[string]any{"ID": 7}})
			if err != nil {
				t.Fatal(err)
			}
			_ = b.Close()
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobNoopAtMaxOffset(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) {
			return
		}
		for i := 0; i < 3; i++ {
			parts := []string{publictest.TestOctetLength}
			if i > 0 {
				parts = append(parts, publictest.TestForUpdate)
			}
			if !publictest.ServeTableRows(ctx, t, p, 3, parts, []byte{8}, []any{"9223372036854775807"}) {
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
	ctx := context.Background()
	tr, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	b, err := oceanbase.OpenTableBlob(ctx, tr, publictest.TableTarget())
	if err != nil {
		t.Fatal(err)
	}
	if n, err := b.WriteAtContext(ctx, nil, math.MaxInt64); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if err = b.Truncate(ctx, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestTableBlobWriteWarning(t *testing.T) {
	db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) || !publictest.ServeTableDictionary(ctx, t, p, 3, publictest.TableDictionary()...) {
			return
		}
		for range 2 {
			if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{"0"}) {
				return
			}
		}
		if command := publictest.ReadTableCommand(ctx, t, p); !strings.Contains(string(command), "UPDATE `s`.`t`") {
			t.Errorf("unexpected mutation %q", command)
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestRollbackSQL, Status: 2}}) {
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
	if n, err := b.WriteAtContext(context.Background(), []byte{1}, 0); n != 0 || !errors.Is(err, oceanbase.ErrLOB) {
		t.Fatal("potentially truncated block reported as written", n, err)
	}
	_ = b.Close()
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestTableBlobCapacity(t *testing.T) {
	for typ, capacity := range map[string]int64{"tinyblob": 255, "blob": 65535, "mediumblob": 16777215, "longblob": 4294967295} {
		t.Run(typ, func(t *testing.T) {
			db := publictest.TxDB(t, config.ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !publictest.ServeTxSteps(ctx, t, p, []publictest.TxStep{{Query: publictest.TestMySQLReadWrite, Status: 3}}) {
					return
				}
				dict := publictest.TableDictionary()
				dict[1][5] = typ
				if !publictest.ServeTableDictionary(ctx, t, p, 3, dict...) {
					return
				}
				for range 2 {
					if !publictest.ServeTableRows(ctx, t, p, 3, []string{publictest.TestOctetLength}, []byte{8}, []any{strconv.FormatInt(capacity, 10)}) {
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
			if n, err := b.WriteAtContext(context.Background(), []byte{1, 2}, capacity-1); n != 0 || !errors.Is(err, oceanbase.ErrLOB) {
				t.Fatal("capacity overflow was not rejected before mutation", n, err)
			}
			_ = b.Close()
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
