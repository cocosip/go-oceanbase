package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func FuzzBlobIdentifiers(f *testing.F) {
	for _, s := range []string{"plain", "a`?;--", "a\"?;--", "a\\`", "中.文", "\x00", "\xff", "?"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if len(name) > 1024 || !validBlobIdentifier(name) {
			return
		}
		for _, mode := range []Mode{ModeMySQL, ModeOracle} {
			id := quoteBlobIdentifier(mode, name)
			for _, noBackslash := range []bool{false, true} {
				plan, err := buildBindPlanEscapes("SELECT "+id+" FROM "+id+"."+id+" WHERE "+id+" = ?", mode, noBackslash)
				if err != nil || plan.numInput != 1 || len(plan.slots) != 1 {
					t.Fatalf("quoted identifier changed SQL bindings: mode %v name %q: %v", mode, name, err)
				}
			}
		}
	})
}

func TestTableBlobCapacity(t *testing.T) {
	for typ, capacity := range map[string]int64{"tinyblob": 255, "blob": 65535, "mediumblob": 16777215, "longblob": 4294967295} {
		t.Run(typ, func(t *testing.T) {
			db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
				if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
					return
				}
				dict := tableDictionary()
				dict[1][5] = typ
				if !serveTableDictionary(ctx, t, p, 3, dict...) {
					return
				}
				for range 2 {
					if !serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{strconv.FormatInt(capacity, 10)}) {
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
			if n, err := b.WriteAtContext(context.Background(), []byte{1, 2}, capacity-1); n != 0 || !errors.Is(err, ErrLOB) {
				t.Fatal("capacity overflow was not rejected before mutation", n, err)
			}
			_ = b.Close()
			if err = tr.Rollback(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTableBlobWriteWarning(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) {
			return
		}
		for range 2 {
			if !serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"0"}) {
				return
			}
		}
		if command := readTableCommand(ctx, t, p); !strings.Contains(string(command), "UPDATE `s`.`t`") {
			t.Errorf("unexpected mutation %q", command)
			return
		}
		if !sendPackets(ctx, t, p, []byte{0, 1, 0, 3, 0, 1, 0}) || !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 2}}) {
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
	if n, err := b.WriteAtContext(context.Background(), []byte{1}, 0); n != 0 || !errors.Is(err, ErrLOB) {
		t.Fatal("potentially truncated block reported as written", n, err)
	}
	_ = b.Close()
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestTableBlobNoopAtMaxOffset(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) || !serveTableDictionary(ctx, t, p, 3, tableDictionary()...) {
			return
		}
		for i := 0; i < 3; i++ {
			parts := []string{testOctetLength}
			if i > 0 {
				parts = append(parts, testForUpdate)
			}
			if !serveTableRows(ctx, t, p, 3, parts, []byte{8}, []any{"9223372036854775807"}) {
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
	ctx := context.Background()
	tr, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Rollback() }()
	b, err := OpenTableBlob(ctx, tr, tableTarget())
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

func TestTableBlobOracleTxLock(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "readonly"}[readonly], func(t *testing.T) {
			db := txDB(t, ModeOracle, func(ctx context.Context, p *wire.PacketIO) {
				start := testOracleReadWrite
				if readonly {
					start = "SET TRANSACTION READ ONLY"
				}
				if !serveTxSteps(ctx, t, p, []txStep{{testAutocommitOff, 4}, {start, 5}}) {
					return
				}
				if !serveTableRows(ctx, t, p, 5, []string{"ALL_TABLES"}, []byte{253, 253, 253, 253, 253, 253, 253, 253}, []any{"S", "T", blobBaseTable, nil, "ID", typeNameNumber, "NO", "1"}, []any{"S", "T", blobBaseTable, nil, "B", typeNameBlob, "NO", nil}) {
					return
				}
				query := `SELECT "B" FROM "S"."T" WHERE "ID" =  7 `
				if !readonly {
					query += " FOR UPDATE"
				}
				if !serveLOBQueryStatus(ctx, t, p, query, 210, 5, lobInlineFixture("ok")) {
					return
				}
				if !serveTxSteps(ctx, t, p, []txStep{{testRollbackSQL, 4}, {testAutocommitOn, 6}}) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			tr, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: readonly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tr.Rollback() }()
			b, err := OpenTableBlob(context.Background(), tr, BlobTarget{Schema: "S", Table: "T", Column: "B", Key: map[string]any{"ID": 7}})
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

func TestTableBlobTxUnknownEngine(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		if !serveTxSteps(ctx, t, p, []txStep{{testMySQLReadWrite, 3}}) {
			return
		}
		dict := tableDictionary()
		for _, row := range dict {
			row[3] = testNonLockingEngine
		}
		if !serveTableDictionary(ctx, t, p, 3, dict...) || !serveTableRows(ctx, t, p, 3, []string{testOctetLength}, []byte{8}, []any{"3"}) {
			return
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
	if _, err = b.WriteAtContext(context.Background(), []byte{1}, 0); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	_ = b.Close()
	if err = tr.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestBlobTargetDefaultExpression(t *testing.T) {
	db := txDB(t, ModeMySQL, func(ctx context.Context, p *wire.PacketIO) {
		dict := tableDictionary()
		dict[1][6] = "DEFAULT_GENERATED"
		if !serveTableDictionary(ctx, t, p, 2, dict...) || !serveTableRows(ctx, t, p, 2, []string{testOctetLength}, []byte{8}, []any{"0"}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	c := pinnedSession(t, db)
	b, err := OpenTableBlob(context.Background(), c, tableTarget())
	if err != nil {
		t.Fatal("default expression is not a generated column", err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}
