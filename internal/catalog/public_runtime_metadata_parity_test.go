package catalog_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/publictest"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMetadataBufferedBudgets(t *testing.T) {
	op := catalogOperation(t, config.Config{Limits: config.Limits{MaxMessageBytes: 1 << 20, MaxResultItems: 1}}, nil)
	if _, err := catalog.BufferedRows(context.Background(), op, oceanbase.MetadataTypeInfo, catalog.BuiltinTypeInfo(config.ModeMySQL)); err != nil {
		t.Fatal(err)
	}
	cfg := op.Config()
	cfg.Limits.MaxResultItems = 0
	op.SetConfig(cfg)
	if _, err := catalog.BufferedRows(context.Background(), op, oceanbase.MetadataTypeInfo, nil); err == nil {
		t.Fatal("missing result budget accepted")
	}
	cfg.Limits.MaxResultItems = 1
	cfg.Limits.MaxMessageBytes = 1
	op.SetConfig(cfg)
	if _, err := catalog.BufferedRows(context.Background(), op, oceanbase.MetadataTypeInfo, nil); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	cfg.Limits.MaxMessageBytes = 1 << 20
	op.SetConfig(cfg)
	if _, err := catalog.BufferedRows(context.Background(), op, oceanbase.MetadataRoutineParameters, make([][]driver.Value, command.MaxColumns+1)); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := catalog.BufferedRows(ctx, op, oceanbase.MetadataTypeInfo, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRoutineMetadataFallbackErrors(t *testing.T) {
	db := publictest.QueryVersionDB(t, testRoutineOldVersion, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, "SHOW CREATE PROCEDURE `s`.`p`") {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, []byte{255, 0x76, 0x04, '#', '4', '2', '0', '0', '0', 'd', 'e', 'n', 'i', 'e', 'd'}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	pinned := publictest.PinnedSession(t, db)

	for _, f := range []oceanbase.MetadataFilter{{Kind: oceanbase.MetadataRoutineParameters}, {Kind: oceanbase.MetadataRoutineParameters, SchemaPattern: "s%", NamePattern: "p"}, {Kind: oceanbase.MetadataRoutineParameters, SchemaPattern: "s", NamePattern: "p_"}} {
		if _, err := oceanbase.Metadata(context.Background(), pinned, f); !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("ambiguous fallback accepted", err)
		}
	}
	_, err := oceanbase.Metadata(context.Background(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataRoutineParameters, SchemaPattern: "s", NamePattern: "p"})
	var server *oceanbase.Error
	if !errors.As(err, &server) || server.Code != 1142 {
		t.Fatal(err)
	}
	if err := pinned.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = pinned.Close()
	_ = db.Close()
}

func TestRoutineMetadataFallbackPublic(t *testing.T) {
	for _, function := range []bool{false, true} {
		db := publictest.QueryVersionDB(t, "", func(ctx context.Context, p *wire.PacketIO) {
			if !testpeer.ReadQuery(ctx, t, p, "SHOW CREATE PROCEDURE `s_%`.`p``x`") {
				return
			}
			kind, ddl := "Create Procedure", "CREATE PROCEDURE `p``x`(IN `a_b` VARCHAR(12),OUT ignored INT) SELECT 1"
			if function {
				if !testpeer.SendPackets(ctx, t, p, []byte{255, 0x19, 0x05, '#', '4', '2', '0', '0', '0', 'm', 'i', 's', 's', 'i', 'n', 'g'}) || !testpeer.ReadQuery(ctx, t, p, "SHOW CREATE FUNCTION `s_%`.`p``x`") {
					return
				}
				kind, ddl = "Create Function", "CREATE FUNCTION `p``x`(`a_b` VARCHAR(12)) RETURNS INT RETURN 1"
			}
			fixture := metadataContractFixture{columns: "routine sql_mode definition", rows: [][]any{{"p`x", "", ddl}}}
			// SHOW CREATE uses a column label containing a space.
			if !testpeer.SendPackets(ctx, t, p, []byte{3}, testpeer.TextColumn("Procedure", 253, 45, 0), testpeer.TextColumn("sql_mode", 253, 45, 0), testpeer.TextColumn(kind, 253, 45, 0), []byte{254, 0, 0, 2, 0}) {
				return
			}
			var row []byte
			for _, v := range fixture.rows[0] {
				s := v.(string)
				row = append(row, byte(len(s)))
				row = append(row, s...)
			}
			if !testpeer.SendPackets(ctx, t, p, row, []byte{254, 0, 0, 2, 0}) || !testpeer.ServeOK(ctx, t, p, []byte{14}, 2) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
		pinned := publictest.PinnedSession(t, db)

		rows, err := oceanbase.Metadata(context.Background(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataRoutineParameters, SchemaPattern: "s/_/%", NamePattern: "p`x", ColumnPattern: "a/_b"})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		for rows.Next() {
			values := make([]any, 15)
			dest := make([]any, 15)
			for i := range dest {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			if values[1] != "s_%" || values[5] != "a_b" || values[6] != int64(1) || values[9] != int64(12) {
				t.Fatal(values)
			}
			count++
		}
		if count != 1 || rows.Err() != nil {
			t.Fatal(count, rows.Err())
		}
		_ = rows.Close()
		if err := pinned.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
		_ = db.Close()
	}
}

func TestMetadataBuiltinTypeInfo(t *testing.T) {
	if oceanbase.MetadataStatementParameters != 17 || oceanbase.MetadataTypeInfo != 18 {
		t.Fatal("metadata enum numbers changed")
	}
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		db := publictest.TxDB(t, mode, func(ctx context.Context, p *wire.PacketIO) {
			status := byte(2)
			if mode == oceanbase.ModeOracle {
				status = 6
			}
			if !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
				return
			}
			p.ResetSequence()
			testpeer.ServeQuit(t, p)
		})
		pinned := publictest.PinnedSession(t, db)
		rows, err := oceanbase.Metadata(context.Background(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataTypeInfo})
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.ColumnTypes()
		if err != nil || len(cols) != 10 || cols[0].Name() != "type_name" || cols[2].ScanType() != reflect.TypeFor[int64]() {
			t.Fatal(cols, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var name, category string
			var precision, lo, hi, radix, auto sql.NullInt64
			var prefix, suffix, params sql.NullString
			if err := rows.Scan(&name, &category, &precision, &lo, &hi, &radix, &prefix, &suffix, &params, &auto); err != nil {
				t.Fatal(err)
			}
			if found[name] || name == "REF" || category == "" {
				t.Fatal("invalid built-in type", name, category)
			}
			found[name] = true
			if name == valueformat.TypeNameNumber && (precision.Int64 != 38 || lo.Int64 != -84 || hi.Int64 != 127 || radix.Int64 != 10) {
				t.Fatal("NUMBER limits")
			}
			if name == valueformat.TypeNameDecimal && (precision.Int64 != 65 || hi.Int64 != 30) {
				t.Fatal("DECIMAL limits")
			}
		}
		if rows.Err() != nil || len(found) == 0 {
			t.Fatal(rows.Err())
		}
		_ = rows.Close()
		if _, err := oceanbase.Metadata(context.Background(), pinned, oceanbase.MetadataFilter{Kind: oceanbase.MetadataTypeInfo, NamePattern: "%"}); !errors.Is(err, oceanbase.ErrBind) {
			t.Fatal(err)
		}
		if err := pinned.PingContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		_ = pinned.Close()
		_ = db.Close()
	}
}
