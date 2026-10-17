package oceanbase

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	executionColumnQuery   = "SELECT t.base AS visible FROM source_table t"
	executionColumnCatalog = "def"
	executionColumnOrigin  = "base"
)

func TestExecutionColumnResults(t *testing.T) {
	want := []ResultColumn{{Name: "first_result", OriginalName: "a"}, {Name: "second_result", OriginalName: "b"}}
	first := executionColumnPacket(t, want[0], "utf8mb4")
	second := executionColumnPacket(t, want[1], "utf8mb4")
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, executionColumnQuery) || !sendPackets(ctx, t, p,
			[]byte{1}, first, []byte{254, 0, 0, 10, 0}, []byte{254, 0, 0, 10, 0},
			commandPacket(1, 0, 10), []byte{1}, second, []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	e, err := Execute(context.Background(), pinnedSession(t, db), executionColumnQuery)
	if err != nil {
		t.Fatal(err)
	}
	before := e.Snapshot()
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	s := e.Snapshot()
	if len(s.Results) != 3 || s.Results[0].RowSetIndex != 0 || s.Results[1].RowSetIndex != -1 || s.Results[2].RowSetIndex != 1 || s.Results[1].Columns != nil {
		t.Fatal("result indexes or command columns", s)
	}
	for i, result := range []ResultInfo{s.Results[0], s.Results[2]} {
		if !result.Complete || !reflect.DeepEqual(result.Columns, []ResultColumn{want[i]}) {
			t.Fatal("rowset columns overwritten", s)
		}
	}
	if len(before.Results) != 1 || before.Results[0].Complete || !reflect.DeepEqual(before.Results[0].Columns, []ResultColumn{want[0]}) {
		t.Fatal("previous snapshot changed", before)
	}
}

func TestExecutionColumnInvalidMetadata(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "column", true: "terminator"}[eof], func(t *testing.T) {
			db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !readQuery(ctx, t, p, executionColumnQuery) {
					return
				}
				packets := [][]byte{{2}, textColumn("v", 3, 63, 0), {1}}
				if eof {
					packets = [][]byte{{1}, textColumn("v", 3, 63, 0), {254}}
				}
				if !sendPackets(ctx, t, p, packets...) {
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid metadata connection reused")
				}
			})
			defer func() { _ = db.Close() }()
			e, err := Execute(context.Background(), pinnedSession(t, db), executionColumnQuery)
			if !errors.Is(err, wire.ErrMalformed) || e == nil {
				t.Fatal(e, err)
			}
			s := e.Snapshot()
			if !s.Done || len(s.Results) != 0 || !errors.Is(s.Err, wire.ErrMalformed) {
				t.Fatal("partial metadata published", s)
			}
		})
	}
}

func TestExecutionColumnWireBudget(t *testing.T) {
	column := executionColumnPacket(t, ResultColumn{Name: "v", OriginalName: strings.Repeat("n", 80)}, "utf8mb4")
	connector, err := NewConnector(Config{User: testUser, Limits: Limits{MaxMessageBytes: 512}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveConnect(ctx, t, p, 2) || !serveBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !readQuery(ctx, t, p, executionColumnQuery) {
			return
		}
		for i := range 3 {
			if !sendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, 10, 0}) {
				return
			}
			if i < 2 && !sendPackets(ctx, t, p, []byte{254, 0, 0, 10, 0}) {
				return
			}
		}
		if _, err := p.ReadMessage(ctx); err == nil {
			t.Error("over-budget connection reused")
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	e, err := Execute(context.Background(), pinnedSession(t, db), executionColumnQuery)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("cumulative wire budget bypassed", err)
	}
	s := e.Snapshot()
	if !s.Done || len(s.Results) != 2 || !s.Results[0].Complete || !s.Results[1].Complete || !errors.Is(s.Err, wire.ErrMessageTooLarge) {
		t.Fatal("budget lost confirmed prefix or published rejected rowset", s)
	}
}

func TestExecutionColumnSnapshot(t *testing.T) {
	columns := []wire.Column{{Catalog: executionColumnCatalog, Schema: "s", Table: "t", OriginalTable: "source_table", Name: "visible", OriginalName: executionColumnOrigin}, {Name: "expression"}}
	e := &Execution{}
	if err := e.rowset(columns, 4096); err != nil {
		t.Fatal(err)
	}
	columns[0].OriginalName = "mutated source"
	want := []ResultColumn{{Catalog: executionColumnCatalog, Schema: "s", Table: "t", OriginalTable: "source_table", Name: "visible", OriginalName: executionColumnOrigin}, {Name: "expression"}}
	s := e.Snapshot()
	if len(s.Results) != 1 || !reflect.DeepEqual(s.Results[0].Columns, want) {
		t.Fatal(s)
	}
	s.Results[0].Columns[0].Name = "mutated snapshot"
	if !reflect.DeepEqual(e.Snapshot().Results[0].Columns, want) {
		t.Fatal("column snapshot aliases internal storage")
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 20 {
				e.Snapshot().Results[0].Columns[0].Table = "owned copy"
			}
		})
	}
	wg.Wait()
	if !reflect.DeepEqual(e.Snapshot().Results[0].Columns, want) {
		t.Fatal("concurrent snapshots share column storage")
	}
	if err := (*Execution)(nil).rowset(columns, 0); err != nil {
		t.Fatal("ordinary Rows allocated an execution snapshot", err)
	}
}

func TestExecutionColumnBudget(t *testing.T) {
	columns := []wire.Column{{Name: "v", OriginalName: "source"}}
	size := rowsetMetadataSize(columns)
	e := &Execution{}
	if err := e.rowset(columns, size*2-1); err != nil {
		t.Fatal(err)
	}
	if err := e.rowset(columns, size*2-1); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("cumulative metadata budget bypassed", err)
	}
	if len(e.Snapshot().Results) != 1 {
		t.Fatal("rejected columns published")
	}
	if err := e.rowset(columns, size*2); err != nil || len(e.Snapshot().Results) != 2 {
		t.Fatal("exact metadata budget rejected", err)
	}
}

func TestExecutionColumnMetadata(t *testing.T) {
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, prepared := range []bool{false, true} {
			t.Run(map[Mode]string{ModeMySQL: "mysql-columns", ModeOracle: "oracle-columns"}[mode]+"/"+map[bool]string{false: "text", true: "binary"}[prepared], func(t *testing.T) {
				ready := byte(2)
				if mode == ModeOracle {
					ready = 6
				}
				want := ResultColumn{Catalog: executionColumnCatalog, Schema: "业务", Table: "t", OriginalTable: "原表", Name: "别名", OriginalName: "原列"}
				column := executionColumnPacket(t, want, localFilenameCharset)
				query := executionColumnQuery
				var args []any
				if prepared {
					query += " WHERE t.id=?"
					args = []any{int64(7)}
				}
				connector, err := NewConnector(Config{User: testUser, Mode: mode, Session: SessionOptions{Charset: localFilenameCharset}, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !serveConnect(ctx, t, p, ready) || !serveOK(ctx, t, p, []byte("\x03SET NAMES gbk"), ready) || !serveOK(ctx, t, p, []byte("\x03"+testAutocommitOn), ready) || !serveVersion(ctx, t, p, ready, false) || !serveOK(ctx, t, p, []byte("\x03"+transactionUTCZone), ready) {
						return
					}
					ctx = context.Background()
					if prepared {
						if !servePrepareStatus(ctx, t, p, query, 41, 1, [][]byte{column}, ready) {
							return
						}
						payload := binary.LittleEndian.AppendUint64(append(executeHeader(41), 0, 1, 8, 0), 7)
						if !readCommand(ctx, t, p, payload) {
							return
						}
					} else if !readQuery(ctx, t, p, query) {
						return
					}
					row := []byte{1, '7'}
					if prepared {
						row = []byte{0, 0, 7, 0, 0, 0}
					}
					if !sendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, ready, 0}, row, []byte{254, 0, 0, ready, 0}) {
						return
					}
					if prepared && !readCommand(ctx, t, p, stmtPacket(25, 41)) {
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
				e, err := Execute(context.Background(), pinned, query, args...)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = e.Close() }()
				s := e.Snapshot()
				if len(s.Results) != 1 || !reflect.DeepEqual(s.Results[0].Columns, []ResultColumn{want}) || s.Results[0].Complete {
					t.Fatal("missing initial columns", s)
				}
				assertExecutionColumnRow(t, e, want)
				if err = pinned.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func assertExecutionColumnRow(t *testing.T, e *Execution, want ResultColumn) {
	t.Helper()
	types, err := e.Rows().ColumnTypes()
	if err != nil || len(types) != 1 || types[0].Name() != want.Name {
		t.Fatal("standard column label changed", types, err)
	}
	if !e.Rows().Next() {
		t.Fatal(e.Rows().Err())
	}
	var value int
	if err = e.Rows().Scan(&value); err != nil || value != 7 {
		t.Fatal(value, err)
	}
	if err = e.Close(); err != nil || !e.Snapshot().Results[0].Complete || !reflect.DeepEqual(e.Snapshot().Results[0].Columns, []ResultColumn{want}) {
		t.Fatal("columns lost after close", err)
	}
}

func TestExecutionColumnJavaDSNRejected(t *testing.T) {
	for _, option := range []string{"useOldAliasMetadataBehavior", "blankTableNameMeta", "useCompatibleMetadata"} {
		if _, err := ParseDSN("oceanbase://app@localhost/?" + option + "=true"); err == nil {
			t.Fatal("Java metadata option accepted", option)
		}
	}
}

func TestExecutionColumnPrepareExecute(t *testing.T) {
	want := ResultColumn{Name: "combined_label", OriginalName: executionColumnOrigin}
	column := executionColumnPacket(t, want, "utf8mb4")
	db := optimizationDB(t, ModeOracle, ExecutionOptions{PrepareExecute: true}, func(ctx context.Context, p *wire.PacketIO) {
		if !readCommand(ctx, t, p, combinedQueryPacket(0, optimizedSelect, 7, false, false)) || !sendPackets(ctx, t, p,
			combinedHeader(7, 1, 1, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0},
			column, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 7, 0, 0, 0}, []byte{254, 0, 0, 6, 0}, commandPacket(0, 0, 6)) ||
			!readCommand(ctx, t, p, stmtPacket(25, 7)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	e, err := Execute(context.Background(), pinnedSession(t, db), optimizedSelect, 7)
	if err != nil {
		t.Fatal(err)
	}
	assertExecutionColumnRow(t, e, want)
}

func executionColumnPacket(t *testing.T, column ResultColumn, encoding string) []byte {
	t.Helper()
	var data []byte
	for _, value := range []string{column.Catalog, column.Schema, column.Table, column.OriginalTable, column.Name, column.OriginalName} {
		encoded, err := charset.Encode(encoding, value)
		if err != nil || len(encoded) > 250 {
			t.Fatal("invalid test column", err)
		}
		data = append(data, byte(len(encoded)))
		data = append(data, encoded...)
	}
	return append(data, 12, 28, 0, 11, 0, 0, 0, 3, 0, 0, 0, 0, 0)
}
