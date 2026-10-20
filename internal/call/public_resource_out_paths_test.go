package call_test

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	binary "encoding/binary"
	errors "errors"
	fmt "fmt"
	math "math"
	net "net"
	regexp "regexp"
	testing "testing"
	time "time"
	_ "time/tzdata"

	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
	call "github.com/cocosip/go-oceanbase/internal/call"
	execution "github.com/cocosip/go-oceanbase/internal/execution"
	parameters "github.com/cocosip/go-oceanbase/internal/parameters"
	publictest "github.com/cocosip/go-oceanbase/internal/publictest"
	session "github.com/cocosip/go-oceanbase/internal/session"
	sqltext "github.com/cocosip/go-oceanbase/internal/sqltext"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	testutil "github.com/cocosip/go-oceanbase/internal/testutil"
	valueformat "github.com/cocosip/go-oceanbase/internal/valueformat"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestOutNativeHeaderErrorSummary(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	value := int64(7)
	e, err := oceanbase.Execute(context.Background(), pinned, "CALL p(?)", sql.Out{Dest: &value})
	if e == nil {
		t.Fatal("missing execution after native server failure", err)
	}
	s := e.Snapshot()
	if err == nil || value != 7 || !s.Done || len(s.Results) != 1 || s.Results[0].Kind != oceanbase.ResultFailure || !s.Results[0].Complete {
		t.Fatal("initial native server failure missing", value, err, s)
	}
}

func TestOutPreparedReuse(t *testing.T) {
	for _, prepare := range []config.PrepareMode{config.PrepareServer, config.PrepareClient} {
		t.Run(map[config.PrepareMode]string{config.PrepareServer: "server", config.PrepareClient: "client"}[prepare], func(t *testing.T) {
			db := publictest.OutOracleDB(t, prepare, func(ctx context.Context, p *wire.PacketIO) {
				id := uint32(0)
				if prepare == config.PrepareServer {
					id = 7
					if !publictest.ServePrepare(ctx, t, p, "CALL p(?)", id, 1, nil) {
						return
					}
				}
				for value := uint64(41); value <= 42; value++ {
					want, err := wire.EncodePrepareExecute(id, []byte("CALL p(?)"), []wire.Parameter{{Type: 246, Data: []byte(fmt.Sprint(value)), LengthEncoded: true}}, 4096)
					if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(7, 1, 1, 4), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, value+1), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
						return
					}
					if prepare == config.PrepareClient && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
						return
					}
				}
				if prepare == config.PrepareServer && !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			s, err := db.Prepare("CALL p(?)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			value := oceanbase.Number("41")
			for _, want := range []oceanbase.Number{"42", "43"} {
				if _, err = s.Exec(sql.Out{Dest: &value, In: true}); err != nil || value != want {
					t.Fatal(value, err)
				}
			}
		})
	}
}

func TestOutMySQLErrors(t *testing.T) {
	for _, failure := range []string{testOutInit, testOutCall, testOutSelect, testOutCleanup, testOutLength} {
		t.Run(failure, func(t *testing.T) {
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				p.ResetSequence()
				init, err := p.ReadMessage(ctx)
				matches := regexp.MustCompile(`^\x03SET (@_obgo_[0-9a-f]{32}_0)=NULL$`).FindStringSubmatch(string(init))
				if err != nil || len(matches) != 2 {
					t.Error("initialization", string(init), err)
					return
				}
				variable := matches[1]
				ok := []byte{0, 0, 0, 2, 0, 0, 0}
				rejected := []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}
				initial := ok
				if failure == testOutInit {
					initial = rejected
				}
				if !testpeer.SendPackets(ctx, t, p, initial) {
					return
				}
				if failure != testOutInit {
					if !testpeer.ReadQuery(ctx, t, p, "CALL p( "+variable+" )") {
						return
					}
					call := ok
					if failure == testOutCall {
						call = rejected
					}
					if !testpeer.SendPackets(ctx, t, p, call) {
						return
					}
				}
				if failure != testOutInit && failure != testOutCall {
					if !testpeer.ReadQuery(ctx, t, p, "SELECT "+variable) {
						return
					}
					if failure == testOutSelect {
						if !testpeer.SendPackets(ctx, t, p, rejected) {
							return
						}
					} else if !testpeer.SendPackets(ctx, t, p, []byte{1}, testpeer.TextColumn("out", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{3, 0xe4, 0xb8, 0xad}, []byte{254, 0, 0, 2, 0}) {
						return
					}
				}
				if !testpeer.ReadQuery(ctx, t, p, "SET "+variable+"=NULL") {
					return
				}
				cleanup := ok
				if failure == testOutCleanup {
					cleanup = rejected
				}
				if !testpeer.SendPackets(ctx, t, p, cleanup) {
					return
				}
				p.ResetSequence()
				if failure == testOutCleanup {
					if _, err = p.ReadMessage(ctx); err == nil {
						t.Error("cleanup failure did not discard session")
					}
				} else {
					testpeer.ServeQuit(t, p)
				}
			}, func(cfg *config.Config) { cfg.Mode = config.ModeMySQL })
			defer func() { _ = c.Close() }()
			value := []byte(testOutBefore)
			out := oceanbase.Out{Dest: &value, Type: valueformat.TypeNameVarbinary}
			if failure == testOutLength {
				out.Length = 2
			}
			_, err := c.ExecContext(context.Background(), "CALL p(?)", []driver.NamedValue{{Value: out}})
			if err == nil || string(value) != testOutBefore || (failure == testOutCleanup) == c.IsValid() {
				t.Fatal(failure, string(value), err, c.IsValid())
			}
			if failure == testOutLength && !errors.Is(err, oceanbase.ErrOut) {
				t.Fatal(err)
			}
		})
	}
}

func TestOutCancellationDuringFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := publictest.WrappedOutputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(9, 1, 1, 4), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	}, func(raw net.Conn) net.Conn { return cancelOutputCloseConn{Conn: raw, cancel: cancel} })
	defer func() { _ = c.Close() }()
	value := int64(7)
	_, err := c.ExecContext(ctx, "CALL p(?)", []driver.NamedValue{{Value: sql.Out{Dest: &value}}})
	if !errors.Is(err, context.Canceled) || value != 7 {
		t.Fatal("cancellation published outputs", value, err)
	}
}

func TestOutLogicalParameterError(t *testing.T) {
	op := outputOperation(t)
	plan, err := sqltext.BuildBindPlan("BEGIN :2:=1; :1:=2; END;", config.ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	var first int8
	var second int64
	args := []driver.NamedValue{{Ordinal: 1, Value: sql.Out{Dest: &first}}, {Ordinal: 2, Value: sql.Out{Dest: &second}}}
	for i := range args {
		args[i].Value, err = normalizeArgument(t, args[i].Value)
		if err != nil {
			t.Fatal(err)
		}
	}
	values, err := parameters.Bind(plan, args)
	if err != nil {
		t.Fatal(err)
	}
	state, err := call.NewState(op, plan, values)
	if err != nil {
		t.Fatal(err)
	}
	err = call.Assign(context.Background(), op, nil, state.Bindings(), []driver.Value{int64(1), int64(128)})
	var output *oceanbase.OutError
	if !errors.As(err, &output) || output.Parameter != 1 {
		t.Fatal("reported physical output ordinal instead of logical parameter", err)
	}
}

func TestOutExtendedINOUTValidation(t *testing.T) {
	op := outputOperation(t)
	n := oceanbase.Number("41")
	nullable := sql.Null[oceanbase.Number]{V: n, Valid: true}
	raw := oceanbase.Raw{0, 255}
	text := oceanbase.NString("中")
	rowid := oceanbase.RowID("AAAA")
	unsigned := sql.Null[uint64]{V: math.MaxUint64, Valid: true}
	if _, err := normalizeArgument(t, oceanbase.Out{Dest: &unsigned, In: true}); err != nil {
		t.Fatal("nullable unsigned INOUT rejected", err)
	}
	for _, out := range []oceanbase.Out{{Dest: &n, In: true}, {Dest: &nullable, In: true}, {Dest: &raw, In: true, Length: 2}, {Dest: &text, In: true, Length: 1}, {Dest: &rowid, In: true, Length: 4}} {
		normalized, err := normalizeArgument(t, out)
		if err != nil {
			t.Fatal(err)
		}
		b := normalized.(*call.Binding)
		if err = call.ValidateValue(op, b, b.Input()); err != nil {
			t.Fatalf("INOUT %T: %v", out.Dest, err)
		}
	}
}

func TestOutNativeInvalidOutputs(t *testing.T) {
	for _, scenario := range []string{"zero-rows", "two-rows", "conflict", testOutCloseFailure} {
		t.Run(scenario, func(t *testing.T) {
			c := publictest.WrappedOutputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				want, err := wire.EncodePrepareExecute(0, []byte("BEGIN ?:=?+1; END;"), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
				if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(9, 2, 2, 4), testpeer.TextColumn("p", 8, 63, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("a", 8, 63, 0), testpeer.TextColumn("b", 8, 63, 0), []byte{254, 0, 0, 6, 16}) {
					return
				}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
				other := uint64(42)
				if scenario == "conflict" {
					other = 43
				}
				row = binary.LittleEndian.AppendUint64(row, other)
				if scenario != "zero-rows" && !testpeer.SendPackets(ctx, t, p, row) {
					return
				}
				if scenario == "two-rows" && !testpeer.SendPackets(ctx, t, p, row) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				if scenario == testOutCloseFailure {
					if _, err = p.ReadMessage(ctx); err == nil {
						t.Error("close failure was hidden")
					}
					return
				}
				if !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			}, func(raw net.Conn) net.Conn {
				if scenario == testOutCloseFailure {
					return closeFailureConn{raw}
				}
				return raw
			})
			defer func() { _ = c.Close() }()
			value := int64(7)
			_, err := c.ExecContext(context.Background(), "BEGIN :x:=:x+1; END;", []driver.NamedValue{{Name: "x", Value: sql.Out{Dest: &value}}})
			if err == nil || value != 7 || (scenario == testOutCloseFailure) == c.IsValid() {
				t.Fatal(scenario, value, err, c.IsValid())
			}
		})
	}
}

func TestOutNativeStreaming(t *testing.T) {
	db := publictest.OutOracleDB(t, config.PrepareServer, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !testpeer.ReadCommand(ctx, t, p, want) {
			return
		}
		if !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(9, 1, 1, 0), testpeer.TextColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, testpeer.TextColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 9), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, testpeer.TextColumn("output", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
			return
		}
		p.ResetSequence()
		testpeer.ServeQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	pinned := publictest.PinnedSession(t, db)
	defer func() { _ = pinned.Close() }()
	value := int64(7)
	e, err := oceanbase.Execute(context.Background(), pinned, "CALL p(?)", sql.Out{Dest: &value})
	if err != nil || e == nil {
		t.Fatal(value, err)
	}
	r := e.Rows()
	columns, columnErr := r.Columns()
	if columnErr != nil || value != 7 || len(columns) != 1 || columns[0] != "ordinary" || e.Snapshot().Done {
		t.Fatal(value, err, e.Snapshot())
	}
	if err = r.Close(); err != nil || value != 42 {
		t.Fatal(value, err)
	}
	if s := e.Snapshot(); !s.Done || len(s.Results) != 2 || s.Results[0].Kind != oceanbase.ResultRows || !s.Results[0].Complete {
		t.Fatal("output control result leaked", s)
	}
}

func TestOutNativeResponseBoundaries(t *testing.T) {
	for _, deprecate := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "OK"}[deprecate], func(t *testing.T) {
			var caps uint64
			if deprecate {
				caps = wire.ClientDeprecateEOF
			}
			c := outputTestConn(t, caps, func(ctx context.Context, p *wire.PacketIO) {
				want, err := wire.EncodePrepareExecute(0, []byte("BEGIN ?:=?+1; END;"), []wire.Parameter{{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 41)}, {Type: 8, Data: binary.LittleEndian.AppendUint64(nil, 41)}}, 4096)
				if err != nil || !testpeer.ReadCommand(ctx, t, p, want) || !testpeer.SendPackets(ctx, t, p, publictest.CombinedHeader(9, 2, 2, 4), testpeer.TextColumn("p", 8, 63, 0), testpeer.TextColumn("p", 8, 63, 0)) {
					return
				}
				if !deprecate && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
					return
				}
				if !testpeer.SendPackets(ctx, t, p, testpeer.TextColumn("a", 8, 63, 0), testpeer.TextColumn("b", 8, 63, 0)) {
					return
				}
				if !deprecate && !testpeer.SendPackets(ctx, t, p, []byte{254, 0, 0, 6, 16}) {
					return
				}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
				row = binary.LittleEndian.AppendUint64(row, 42)
				end := []byte{254, 0, 0, 6, 0}
				if deprecate {
					end = []byte{254, 0, 0, 6, 0, 0, 0}
				}
				if !testpeer.SendPackets(ctx, t, p, row, end, []byte{0, 0, 0, 6, 0, 0, 0}) || !testpeer.ReadCommand(ctx, t, p, publictest.StmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				testpeer.ServeQuit(t, p)
			})
			defer func() { _ = c.Close() }()
			value := int64(41)
			if _, err := c.ExecContext(context.Background(), "BEGIN :x:=:x+1; END;", []driver.NamedValue{{Name: "x", Ordinal: 1, Value: sql.Out{Dest: &value, In: true}}}); err != nil || value != 42 {
				t.Fatal("repeated output was not assigned once", value, err)
			}
		})
	}
}

func TestOutPreSendValidation(t *testing.T) {
	var scalar int64
	var n oceanbase.Number
	for _, tc := range []struct {
		name, version string
		mode          config.Mode
		value         any
		want          error
	}{
		{"old-version", "2.2.75", config.ModeOracle, sql.Out{Dest: &scalar}, dberrors.ErrUnsupported},
		{"unknown-version", "", config.ModeOracle, sql.Out{Dest: &scalar}, dberrors.ErrUnsupported},
		{"invalid-destination", testOutVersion, config.ModeOracle, sql.Out{}, oceanbase.ErrOut},
		{"oracle-precision", testOutVersion, config.ModeOracle, oceanbase.Out{Dest: &n, Precision: 39}, oceanbase.ErrOut},
		{"mysql-scale", testOutVersion, config.ModeMySQL, oceanbase.Out{Dest: &n, Precision: 2, Scale: 3}, oceanbase.ErrOut},
		{"mysql-native-type", testOutVersion, config.ModeMySQL, oceanbase.Out{Dest: &scalar, Type: "ROWID"}, dberrors.ErrUnsupported},
		{"numeric-length", testOutVersion, config.ModeOracle, oceanbase.Out{Dest: &scalar, Length: 5}, oceanbase.ErrOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{WriteFailAt: 1})
			physical := session.New(outputTestConfig(), &session.Negotiated{Raw: fault, Conn: fault, Packets: wire.NewPacketIO(fault, 4096), Mode: tc.mode, OBVersion: tc.version})
			engine := execution.New(nil, physical)
			normalized, err := normalizeArgument(t, tc.value)
			if err == nil {
				_, err = engine.Exec(context.Background(), "CALL p(?)", []driver.NamedValue{{Ordinal: 1, Value: normalized}}, execution.Options{Outputs: true})
			}
			_, writes := fault.Counts()
			gateCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			lease, gateErr := physical.Acquire(gateCtx)
			if lease != nil {
				lease.Release()
			}
			if !errors.Is(err, tc.want) || writes != 0 || !engine.Valid() || gateErr != nil {
				t.Fatalf("err=%v writes=%d valid=%t gate=%v", err, writes, engine.Valid(), gateErr)
			}
		})
	}
}
