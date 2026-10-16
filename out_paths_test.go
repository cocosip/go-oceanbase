package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testutil"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	testOutLength       = "length"
	testOutSelect       = "select"
	testOutCleanup      = "cleanup"
	testOutInit         = "init"
	testOutCall         = "call"
	testOutCloseFailure = "close-failure"
	testOutBefore       = "before"
	testOutVersion      = "4.3.5.1"
)

func outputTestConfig() Config {
	return Config{Session: SessionOptions{Charset: charsetUTF8MB4, Location: time.UTC}, Limits: Limits{MaxMessageBytes: 4096, MaxLOBBytes: 4096, MaxResultItems: 16, CleanupTimeout: time.Second}}
}

func outputTestConn(t *testing.T, caps uint64, serve func(context.Context, *wire.PacketIO)) *conn {
	t.Helper()
	raw, err := peerDial(t, serve)(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: raw, conn: raw, packets: wire.NewPacketIO(raw, 4096), capabilities: wire.ClientProtocol41 | caps, mode: ModeOracle, obVersion: testOutVersion}}
}

func TestOutPreSendValidation(t *testing.T) {
	var scalar int64
	var n Number
	for _, tc := range []struct {
		name, version string
		mode          Mode
		value         any
		want          error
	}{
		{"old-version", "2.2.75", ModeOracle, sql.Out{Dest: &scalar}, ErrUnsupported},
		{"unknown-version", "", ModeOracle, sql.Out{Dest: &scalar}, ErrUnsupported},
		{"invalid-destination", testOutVersion, ModeOracle, sql.Out{}, ErrOut},
		{"oracle-precision", testOutVersion, ModeOracle, Out{Dest: &n, Precision: 39}, ErrOut},
		{"mysql-scale", testOutVersion, ModeMySQL, Out{Dest: &n, Precision: 2, Scale: 3}, ErrOut},
		{"mysql-native-type", testOutVersion, ModeMySQL, Out{Dest: &scalar, Type: "ROWID"}, ErrUnsupported},
		{"numeric-length", testOutVersion, ModeOracle, Out{Dest: &scalar, Length: 5}, ErrOut},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			fault := testutil.NewFaultConn(client, testutil.Faults{WriteFailAt: 1})
			c := &conn{mu: make(chan struct{}, 1), cfg: outputTestConfig(), session: &handshakeSession{raw: fault, conn: fault, packets: wire.NewPacketIO(fault, 4096), mode: tc.mode, obVersion: tc.version}}
			_, err := c.ExecContext(context.Background(), "CALL p(?)", []driver.NamedValue{{Ordinal: 1, Value: tc.value}})
			_, writes := fault.Counts()
			if !errors.Is(err, tc.want) || writes != 0 || !c.IsValid() || len(c.mu) != 0 {
				t.Fatalf("err=%v writes=%d valid=%t gate=%d", err, writes, c.IsValid(), len(c.mu))
			}
		})
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
				if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(9, 2, 2, 4), textColumn("p", 8, 63, 0), textColumn("p", 8, 63, 0)) {
					return
				}
				if !deprecate && !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}) {
					return
				}
				if !sendPackets(ctx, t, p, textColumn("a", 8, 63, 0), textColumn("b", 8, 63, 0)) {
					return
				}
				if !deprecate && !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 16}) {
					return
				}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
				row = binary.LittleEndian.AppendUint64(row, 42)
				end := []byte{254, 0, 0, 6, 0}
				if deprecate {
					end = []byte{254, 0, 0, 6, 0, 0, 0}
				}
				if !sendPackets(ctx, t, p, row, end, []byte{0, 0, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = c.Close() }()
			value := int64(41)
			if _, err := c.ExecContext(context.Background(), "BEGIN :x:=:x+1; END;", []driver.NamedValue{{Name: "x", Ordinal: 1, Value: sql.Out{Dest: &value, In: true}}}); err != nil || value != 42 {
				t.Fatal("repeated output was not assigned once", value, err)
			}
		})
	}
}

func TestOutNativeStreaming(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !readCommand(ctx, t, p, want) {
			return
		}
		if !sendPackets(ctx, t, p, combinedHeader(9, 1, 1, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("ordinary", 8, 63, 0), []byte{254, 0, 0, 6, 0}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 9), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 14, 0, 0, 0}, []byte{1}, textColumn("output", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 9)) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = c.Close() }()
	value := int64(7)
	e := &Execution{}
	ctx := context.WithValue(context.Background(), executionContextKey{}, e)
	r, err := c.queryOutputs(ctx, "CALL p(?)", []driver.NamedValue{{Value: sql.Out{Dest: &value}}}, nil)
	if err != nil || value != 7 || len(r.Columns()) != 1 || r.Columns()[0] != "ordinary" || e.Snapshot().Done {
		t.Fatal(value, err, e.Snapshot())
	}
	if err = r.Close(); err != nil || value != 42 {
		t.Fatal(value, err)
	}
	if s := e.Snapshot(); !s.Done || len(s.Results) != 2 || s.Results[0].Kind != ResultRows || !s.Results[0].Complete {
		t.Fatal("output control result leaked", s)
	}
}

func TestOutNativeInvalidOutputs(t *testing.T) {
	for _, scenario := range []string{"zero-rows", "two-rows", "conflict", testOutCloseFailure} {
		t.Run(scenario, func(t *testing.T) {
			c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
				want, err := wire.EncodePrepareExecute(0, []byte("BEGIN ?:=?+1; END;"), []wire.Parameter{{Type: 6}, {Type: 6}}, 4096)
				if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(9, 2, 2, 4), textColumn("p", 8, 63, 0), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("a", 8, 63, 0), textColumn("b", 8, 63, 0), []byte{254, 0, 0, 6, 16}) {
					return
				}
				row := binary.LittleEndian.AppendUint64([]byte{0, 0}, 42)
				other := uint64(42)
				if scenario == "conflict" {
					other = 43
				}
				row = binary.LittleEndian.AppendUint64(row, other)
				if scenario != "zero-rows" && !sendPackets(ctx, t, p, row) {
					return
				}
				if scenario == "two-rows" && !sendPackets(ctx, t, p, row) {
					return
				}
				if !sendPackets(ctx, t, p, []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
					return
				}
				p.ResetSequence()
				if scenario == testOutCloseFailure {
					if _, err = p.ReadMessage(ctx); err == nil {
						t.Error("close failure was hidden")
					}
					return
				}
				if !readCommand(ctx, t, p, stmtPacket(25, 9)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			if scenario == testOutCloseFailure {
				c.session.conn = closeFailureConn{c.session.raw}
				c.session.packets = wire.NewPacketIO(c.session.conn, 4096)
			}
			defer func() { _ = c.Close() }()
			value := int64(7)
			_, err := c.ExecContext(context.Background(), "BEGIN :x:=:x+1; END;", []driver.NamedValue{{Name: "x", Value: sql.Out{Dest: &value}}})
			if err == nil || value != 7 || (scenario == testOutCloseFailure) == c.IsValid() {
				t.Fatal(scenario, value, err, c.IsValid())
			}
		})
	}
}

func TestOutPrecisionAndNullableTime(t *testing.T) {
	c := &conn{cfg: outputTestConfig()}
	for _, tc := range []struct {
		precision, scale int64
		value            string
		valid            bool
	}{{2, 4, "0.0012", true}, {2, 4, "0.12", false}, {2, 4, "0", true}, {2, -2, "1200", true}, {2, -2, "1230", false}, {2, -2, "12300", false}, {3, 2, "9.99", true}, {3, 2, "10", false}} {
		b := &outBinding{Out: Out{Type: typeNameNumber, Precision: tc.precision, Scale: tc.scale}}
		if err := c.validateOutputValue(b, tc.value); (err == nil) != tc.valid {
			t.Fatal(tc, err)
		}
	}
	var timestamp sql.Null[time.Time]
	b, err := makeOutput(Out{Dest: &timestamp})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.assignOutputs([]*outBinding{b}, []driver.Value{"2026-01-02 03:04:05"}); err != nil || !timestamp.Valid || timestamp.V.Year() != 2026 {
		t.Fatal(timestamp, err)
	}
}

func TestOutExtendedINOUTValidation(t *testing.T) {
	c := &conn{cfg: outputTestConfig()}
	n := Number("41")
	nullable := sql.Null[Number]{V: n, Valid: true}
	raw := Raw{0, 255}
	text := NString("中")
	rowid := RowID("AAAA")
	unsigned := sql.Null[uint64]{V: math.MaxUint64, Valid: true}
	if _, err := makeOutput(Out{Dest: &unsigned, In: true}); err != nil {
		t.Fatal("nullable unsigned INOUT rejected", err)
	}
	for _, out := range []Out{{Dest: &n, In: true}, {Dest: &nullable, In: true}, {Dest: &raw, In: true, Length: 2}, {Dest: &text, In: true, Length: 1}, {Dest: &rowid, In: true, Length: 4}} {
		b, err := makeOutput(out)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.validateOutputValue(b, b.input); err != nil {
			t.Fatalf("INOUT %T: %v", out.Dest, err)
		}
	}
}

func TestOutLogicalParameterError(t *testing.T) {
	c := &conn{cfg: outputTestConfig()}
	plan, err := buildBindPlan("BEGIN :2:=1; :1:=2; END;", ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	var first int8
	var second int64
	values, err := bindArguments(plan, []driver.NamedValue{{Value: sql.Out{Dest: &first}}, {Value: sql.Out{Dest: &second}}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := c.outputBindings(plan, values)
	if err != nil {
		t.Fatal(err)
	}
	err = c.assignOutputs(state.bindings, []driver.Value{int64(1), int64(128)})
	var output *OutError
	if !errors.As(err, &output) || output.Parameter != 1 {
		t.Fatal("reported physical output ordinal instead of logical parameter", err)
	}
}

type cancelOutputCloseConn struct {
	net.Conn
	cancel context.CancelFunc
}

func (c cancelOutputCloseConn) Write(data []byte) (int, error) {
	if len(data) == 5 && data[0] == 25 {
		c.cancel()
	}
	return c.Conn.Write(data)
}

func TestOutCancellationDuringFinalization(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(9, 1, 1, 4), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, 42), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) || !readCommand(ctx, t, p, stmtPacket(25, 9)) {
			return
		}
		p.ResetSequence()
		_, _ = p.ReadMessage(ctx)
	})
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.session.conn = cancelOutputCloseConn{Conn: c.session.raw, cancel: cancel}
	c.session.packets = wire.NewPacketIO(c.session.conn, 4096)
	value := int64(7)
	_, err := c.ExecContext(ctx, "CALL p(?)", []driver.NamedValue{{Value: sql.Out{Dest: &value}}})
	if !errors.Is(err, context.Canceled) || value != 7 {
		t.Fatal("cancellation published outputs", value, err)
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
				if !sendPackets(ctx, t, p, initial) {
					return
				}
				if failure != testOutInit {
					if !readQuery(ctx, t, p, "CALL p( "+variable+" )") {
						return
					}
					call := ok
					if failure == testOutCall {
						call = rejected
					}
					if !sendPackets(ctx, t, p, call) {
						return
					}
				}
				if failure != testOutInit && failure != testOutCall {
					if !readQuery(ctx, t, p, "SELECT "+variable) {
						return
					}
					if failure == testOutSelect {
						if !sendPackets(ctx, t, p, rejected) {
							return
						}
					} else if !sendPackets(ctx, t, p, []byte{1}, textColumn("out", 253, 45, 0), []byte{254, 0, 0, 2, 0}, []byte{3, 0xe4, 0xb8, 0xad}, []byte{254, 0, 0, 2, 0}) {
						return
					}
				}
				if !readQuery(ctx, t, p, "SET "+variable+"=NULL") {
					return
				}
				cleanup := ok
				if failure == testOutCleanup {
					cleanup = rejected
				}
				if !sendPackets(ctx, t, p, cleanup) {
					return
				}
				p.ResetSequence()
				if failure == testOutCleanup {
					if _, err = p.ReadMessage(ctx); err == nil {
						t.Error("cleanup failure did not discard session")
					}
				} else {
					serveQuit(t, p)
				}
			})
			c.session.mode = ModeMySQL
			defer func() { _ = c.Close() }()
			value := []byte(testOutBefore)
			out := Out{Dest: &value, Type: typeNameVarbinary}
			if failure == testOutLength {
				out.Length = 2
			}
			_, err := c.ExecContext(context.Background(), "CALL p(?)", []driver.NamedValue{{Value: out}})
			if err == nil || string(value) != testOutBefore || (failure == testOutCleanup) == c.IsValid() {
				t.Fatal(failure, string(value), err, c.IsValid())
			}
			if failure == testOutLength && !errors.Is(err, ErrOut) {
				t.Fatal(err)
			}
		})
	}
}

func TestOutPreparedReuse(t *testing.T) {
	for _, prepare := range []PrepareMode{PrepareServer, PrepareClient} {
		t.Run(map[PrepareMode]string{PrepareServer: "server", PrepareClient: "client"}[prepare], func(t *testing.T) {
			db := outOracleDB(t, prepare, func(ctx context.Context, p *wire.PacketIO) {
				id := uint32(0)
				if prepare == PrepareServer {
					id = 7
					if !servePrepare(ctx, t, p, "CALL p(?)", id, 1, nil) {
						return
					}
				}
				for value := uint64(41); value <= 42; value++ {
					want, err := wire.EncodePrepareExecute(id, []byte("CALL p(?)"), []wire.Parameter{{Type: 246, Data: []byte(fmt.Sprint(value)), LengthEncoded: true}}, 4096)
					if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, combinedHeader(7, 1, 1, 4), textColumn("p", 8, 63, 0), []byte{254, 0, 0, 6, 0}, textColumn("out", 8, 63, 0), []byte{254, 0, 0, 6, 16}, binary.LittleEndian.AppendUint64([]byte{0, 0}, value+1), []byte{254, 0, 0, 6, 0}, []byte{0, 0, 0, 6, 0, 0, 0}) {
						return
					}
					if prepare == PrepareClient && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
						return
					}
				}
				if prepare == PrepareServer && !readCommand(ctx, t, p, stmtPacket(25, 7)) {
					return
				}
				p.ResetSequence()
				serveQuit(t, p)
			})
			defer func() { _ = db.Close() }()
			s, err := db.Prepare("CALL p(?)")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			value := Number("41")
			for _, want := range []Number{"42", "43"} {
				if _, err = s.Exec(sql.Out{Dest: &value, In: true}); err != nil || value != want {
					t.Fatal(value, err)
				}
			}
		})
	}
}

func TestOutNativeHeaderErrorSummary(t *testing.T) {
	c := outputTestConn(t, 0, func(ctx context.Context, p *wire.PacketIO) {
		want, err := wire.EncodePrepareExecute(0, []byte("CALL p(?)"), []wire.Parameter{{Type: 6}}, 4096)
		if err != nil || !readCommand(ctx, t, p, want) || !sendPackets(ctx, t, p, []byte{255, 1, 0, '#', 'H', 'Y', '0', '0', '0', 'x'}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = c.Close() }()
	value := int64(7)
	e := &Execution{}
	ctx := context.WithValue(context.Background(), executionContextKey{}, e)
	_, err := c.queryOutputs(ctx, "CALL p(?)", []driver.NamedValue{{Value: sql.Out{Dest: &value}}}, nil)
	s := e.Snapshot()
	if err == nil || value != 7 || !s.Done || len(s.Results) != 1 || s.Results[0].Kind != ResultFailure || !s.Results[0].Complete {
		t.Fatal("initial native server failure missing", value, err, s)
	}
}
