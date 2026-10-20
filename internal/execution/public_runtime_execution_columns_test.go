package execution_test

import (
	context "context"
	sql "database/sql"
	binary "encoding/binary"
	errors "errors"
	reflect "reflect"
	strings "strings"
	sync "sync"
	testing "testing"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	execution "github.com/cocosip/go-oceanbase/internal/execution"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	rowset "github.com/cocosip/go-oceanbase/internal/rowset"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestExecutionColumnMetadata(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, prepared := range []bool{false, true} {
			t.Run(map[oceanbase.Mode]string{oceanbase.ModeMySQL: "mysql-columns", oceanbase.ModeOracle: "oracle-columns"}[mode]+"/"+map[bool]string{false: "text", true: "binary"}[prepared], func(t *testing.T) {
				ready := byte(2)
				if mode == oceanbase.ModeOracle {
					ready = 6
				}
				want := oceanbase.ResultColumn{Catalog: executionColumnCatalog, Schema: "业务", Table: "t", OriginalTable: "原表", Name: "别名", OriginalName: "原列"}
				column := executionColumnPacket(t, want, localFilenameCharset)
				query := executionColumnQuery
				var args []any
				if prepared {
					query += " WHERE t.id=?"
					args = []any{int64(7)}
				}
				connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Mode: mode, Session: oceanbase.SessionOptions{Charset: localFilenameCharset}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
					if !testpeer.ServeConnect(ctx, t, p, ready) || !testpeer.ServeOK(ctx, t, p, []byte("\x03SET NAMES gbk"), ready) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+testAutocommitOn), ready) || !testpeer.ServeVersion(ctx, t, p, ready, false) || !testpeer.ServeOK(ctx, t, p, []byte("\x03"+transactionUTCZone), ready) {
						return
					}
					ctx = context.Background()
					if prepared {
						if !publictest.ServePrepareStatus(ctx, t, p, query, 41, 1, [][]byte{column}, ready) {
							return
						}
						payload := binary.LittleEndian.AppendUint64(append(publictest.ExecuteHeader(41), 0, 1, 8, 0), 7)
						if !testpeer.ReadCommand(ctx, t, p, payload) {
							return
						}
					} else if !testpeer.ReadQuery(ctx, t, p, query) {
						return
					}
					row := []byte{1, '7'}
					if prepared {
						row = []byte{0, 0, 7, 0, 0, 0}
					}
					if !testpeer.SendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, ready, 0}, row, []byte{254, 0, 0, ready, 0}) {
						return
					}
					if prepared && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 41)) {
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
				e, err := oceanbase.Execute(context.Background(), pinned, query, args...)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = e.Close() }()
				s := e.Snapshot()
				if len(s.Results) != 1 || !reflect.DeepEqual(s.Results[0].Columns, []oceanbase.ResultColumn{want}) || s.Results[0].Complete {
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

func TestExecutionColumnBudget(t *testing.T) {
	columns := []wire.Column{{Name: "v", OriginalName: "source"}}
	size := rowset.MetadataSize(columns)
	e := &execution.Recorder{}
	if err := e.Rowset(columns, size*2-1); err != nil {
		t.Fatal(err)
	}
	if err := e.Rowset(columns, size*2-1); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal("cumulative metadata budget bypassed", err)
	}
	if len(e.Snapshot().Results) != 1 {
		t.Fatal("rejected columns published")
	}
	if err := e.Rowset(columns, size*2); err != nil || len(e.Snapshot().Results) != 2 {
		t.Fatal("exact metadata budget rejected", err)
	}
}

func TestExecutionColumnSnapshot(t *testing.T) {
	columns := []wire.Column{{Catalog: executionColumnCatalog, Schema: "s", Table: "t", OriginalTable: "source_table", Name: "visible", OriginalName: executionColumnOrigin}, {Name: "expression"}}
	e := &execution.Recorder{}
	if err := e.Rowset(columns, 4096); err != nil {
		t.Fatal(err)
	}
	columns[0].OriginalName = "mutated source"
	want := []execution.ResultColumn{{Catalog: executionColumnCatalog, Schema: "s", Table: "t", OriginalTable: "source_table", Name: "visible", OriginalName: executionColumnOrigin}, {Name: "expression"}}
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
	if err := (*execution.Recorder)(nil).Rowset(columns, 0); err != nil {
		t.Fatal("ordinary Rows allocated an execution snapshot", err)
	}
}

func TestExecutionColumnWireBudget(t *testing.T) {
	column := executionColumnPacket(t, oceanbase.ResultColumn{Name: "v", OriginalName: strings.Repeat("n", 80)}, "utf8mb4")
	connector, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Limits: oceanbase.Limits{MaxMessageBytes: 512}, DialContext: testpeer.PeerDial(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ServeConnect(ctx, t, p, 2) || !testpeer.ServeBaseline(ctx, t, p, 2) {
			return
		}
		ctx = context.Background()
		if !testpeer.ReadQuery(ctx, t, p, executionColumnQuery) {
			return
		}
		for i := range 3 {
			if !testpeer.SendPackets(ctx, t, p, []byte{1}, column, []byte{254, 0, 0, 10, 0}) {
				return
			}
			if i < 2 && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 10, 0}) {
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
	e, err := oceanbase.Execute(context.Background(), publictest.PinnedSession(t, db), executionColumnQuery)
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

func TestExecutionColumnInvalidMetadata(t *testing.T) {
	for _, eof := range []bool{false, true} {
		t.Run(map[bool]string{false: "column", true: "terminator"}[eof], func(t *testing.T) {
			db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
				if !testpeer.ReadQuery(ctx, t, p, executionColumnQuery) {
					return
				}
				packets := [][]byte{{2}, testpeer.TextColumn("v", 3, 63, 0), {1}}
				if eof {
					packets = [][]byte{{1}, testpeer.TextColumn("v", 3, 63, 0), {254}}
				}
				if !testpeer.SendPackets(ctx, t, p, packets...) {
					return
				}
				if _, err := p.ReadMessage(ctx); err == nil {
					t.Error("invalid metadata connection reused")
				}
			})
			defer func() { _ = db.Close() }()
			e, err := oceanbase.Execute(context.Background(), publictest.PinnedSession(t, db), executionColumnQuery)
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

func TestExecutionColumnResults(t *testing.T) {
	want := []oceanbase.ResultColumn{{Name: "first_result", OriginalName: "a"}, {Name: "second_result", OriginalName: "b"}}
	first := executionColumnPacket(t, want[0], "utf8mb4")
	second := executionColumnPacket(t, want[1], "utf8mb4")
	db := publictest.QueryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !testpeer.ReadQuery(ctx, t, p, executionColumnQuery) || !testpeer.SendPackets(ctx, t, p,
			[]byte{1}, first, []byte{254, 0, 0, 10, 0}, []byte{254, 0, 0, 10, 0},
			publictest.CommandPacket(1, 0, 10), []byte{1}, second, []byte{254, 0, 0, 2, 0}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	e, err := oceanbase.Execute(context.Background(), publictest.PinnedSession(t, db), executionColumnQuery)
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
	for i, result := range []oceanbase.ResultInfo{s.Results[0], s.Results[2]} {
		if !result.Complete || !reflect.DeepEqual(result.Columns, []oceanbase.ResultColumn{want[i]}) {
			t.Fatal("rowset columns overwritten", s)
		}
	}
	if len(before.Results) != 1 || before.Results[0].Complete || !reflect.DeepEqual(before.Results[0].Columns, []oceanbase.ResultColumn{want[0]}) {
		t.Fatal("previous snapshot changed", before)
	}
}
