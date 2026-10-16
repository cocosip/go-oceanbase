package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const selectBindSQL = "SELECT ?"

func TestBindPlan(t *testing.T) {
	const exactValue = "exact"
	for _, tc := range []struct {
		query  string
		mode   Mode
		args   []driver.NamedValue
		sql    string
		values []any
	}{
		{"SELECT ?, '?', `?`, \"?\" /* ? */ -- ?\n, ?", ModeMySQL, []driver.NamedValue{{Ordinal: 1, Value: int64(1)}, {Ordinal: 2, Value: "x"}}, "SELECT ?, '?', `?`, \"?\" /* ? */ -- ?\n, ?", []any{int64(1), "x"}},
		{"SELECT :value, :VALUE FROM dual", ModeOracle, []driver.NamedValue{{Name: "Value", Ordinal: 1, Value: exactValue}}, "SELECT ?, ? FROM dual", []any{exactValue, exactValue}},
		{"SELECT :2, :1, :2 FROM dual", ModeOracle, []driver.NamedValue{{Ordinal: 1, Value: "a"}, {Ordinal: 2, Value: "b"}}, "SELECT ?, ?, ? FROM dual", []any{"b", "a", "b"}},
		{"BEGIN :x := q'[?:hidden]'; :y := q'! :also_hidden !'; END;", ModeOracle, []driver.NamedValue{{Name: "x", Value: nil}, {Name: "y", Value: uint64(math.MaxUint64)}}, "BEGIN ? := q'[?:hidden]'; ? := q'! :also_hidden !'; END;", []any{nil, uint64(math.MaxUint64)}},
		{"SELECT ':a''?\\', :a FROM dual", ModeOracle, []driver.NamedValue{{Name: "a", Value: int64(2)}}, "SELECT ':a''?\\', ? FROM dual", []any{int64(2)}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			plan, err := buildBindPlan(tc.query, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if plan.sql != tc.sql {
				t.Fatalf("got %q, want %q", plan.sql, tc.sql)
			}
			bound, err := bindArguments(plan, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			values := make([]any, len(bound))
			for i, b := range bound {
				values[i] = b.value
			}
			if !reflect.DeepEqual(values, tc.values) {
				t.Fatal(values, tc.values)
			}
		})
	}
}

func TestBindAdjacentHashComment(t *testing.T) {
	for _, query := range []string{"SELECT 1# ?\n", "SELECT 1# ; SELECT 2\n"} {
		p, err := buildBindPlan(query, ModeMySQL)
		if err != nil || p.numInput != 0 {
			t.Fatalf("comment interpreted as SQL: %#v %v", p, err)
		}
	}
}

func TestBindInvalid(t *testing.T) {
	for _, tc := range []struct {
		query string
		mode  Mode
		args  []driver.NamedValue
	}{
		{selectBindSQL, ModeMySQL, nil},
		{"SELECT TRUE", ModeMySQL, []driver.NamedValue{{Value: 1}}},
		{selectBindSQL, ModeMySQL, []driver.NamedValue{{Name: "x", Value: 1}}},
		{"SELECT :x, ? FROM dual", ModeOracle, []driver.NamedValue{{Name: "x", Value: 1}, {Value: 2}}},
		{"SELECT :x FROM dual", ModeOracle, []driver.NamedValue{{Name: "x", Value: 1}, {Name: "X", Value: 2}}},
		{"SELECT :x FROM dual", ModeOracle, []driver.NamedValue{{Name: "y", Value: 1}}},
		{"SELECT :1, ? FROM dual", ModeOracle, []driver.NamedValue{{Value: 1}, {Value: 2}}},
		{"SELECT :0 FROM dual", ModeOracle, nil},
		{"SELECT :999999999999999999999999 FROM dual", ModeOracle, nil},
		{"SELECT 'unterminated ?", ModeMySQL, nil},
		{"SELECT /* unterminated ?", ModeMySQL, nil},
		{"SELECT q'[unterminated' FROM dual", ModeOracle, nil},
		{"SELECT /*! ? */ 1", ModeMySQL, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			p, err := buildBindPlan(tc.query, tc.mode)
			if err == nil {
				_, err = bindArguments(p, tc.args)
			}
			if err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

type failingValuer struct{ calls *int }

var errValue = errors.New("valuer rejected")

func (v failingValuer) Value() (driver.Value, error) { *v.calls++; return nil, errValue }

func TestNamedValueValidation(t *testing.T) {
	var checker any = &conn{}
	c, ok := checker.(driver.NamedValueChecker)
	if !ok {
		t.Fatal("NamedValueChecker missing")
	}
	nv := driver.NamedValue{Value: uint64(math.MaxUint64)}
	if err := c.CheckNamedValue(&nv); err != nil || nv.Value != uint64(math.MaxUint64) {
		t.Fatal(nv.Value, err)
	}
	var valueCalls int
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), selectBindSQL, failingValuer{&valueCalls}); !errors.Is(err, errValue) || valueCalls != 1 {
		t.Fatal(valueCalls, err)
	}
}

func TestBindEscapeModes(t *testing.T) {
	query := "SELECT 'a\\', ?"
	if _, err := buildBindPlanEscapes(query, ModeMySQL, false); err == nil {
		t.Fatal("backslash escaping ignored")
	}
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		p, err := buildBindPlanEscapes(query, mode, true)
		if err != nil || p.numInput != 1 {
			t.Fatal(p, err)
		}
	}
	p, err := buildBindPlan("SELECT 1--2, ?", ModeMySQL)
	if err != nil || p.numInput != 1 {
		t.Fatal(p, err)
	}
	p, err = buildBindPlan("SELECT nq'¡:hidden ?¡', :x FROM dual", ModeOracle)
	if err != nil || p.numInput != 1 {
		t.Fatal(p, err)
	}
}

func TestBindRequestRejectsBeforeSend(t *testing.T) {
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !serveOK(ctx, t, p, []byte{14}, 2) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{selectBindSQL, "SELECT TRUE"} {
		if _, err := db.ExecContext(context.Background(), query, sql.Named("unexpected", 1)); err == nil {
			t.Fatal("invalid command sent")
		}
	}
}

func TestBindANSIQuotesSession(t *testing.T) {
	query := "SELECT 1 AS \"a\\\""
	db := queryDB(t, func(ctx context.Context, p *wire.PacketIO) {
		if !readQuery(ctx, t, p, query) {
			return
		}
		if !sendPackets(ctx, t, p, []byte{1}, textColumn("a\\", 3, 63, 0), []byte{254, 0, 0, 2, 0}, []byte{1, '1'}, []byte{254, 0, 0, 2, 0}) {
			return
		}
		p.ResetSequence()
		serveQuit(t, p)
	})
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(context.Background(), query).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestBindPreservesNativeSQL(t *testing.T) {
	for _, tc := range []struct {
		mode   Mode
		status byte
		query  string
	}{
		{ModeMySQL, 2, "CREATE PROCEDURE p() BEGIN SELECT 1; END"},
		{ModeOracle, 6, "CREATE OR REPLACE PROCEDURE p AS BEGIN NULL; END;"},
		{ModeOracle, 6, "DECLARE PROCEDURE p IS BEGIN NULL; END; BEGIN p; END;"},
		{ModeMySQL, 2, "SELECT /*! SQL_NO_CACHE */ 1"},
		{ModeMySQL, 2, `SELECT ROW_COUNT(), '"\a'`},
	} {
		t.Run(tc.query, func(t *testing.T) {
			connector, err := NewConnector(Config{User: testUser, Mode: tc.mode, DialContext: peerDial(t, func(ctx context.Context, p *wire.PacketIO) {
				if !serveConnect(ctx, t, p, tc.status) || !serveBaseline(ctx, t, p, tc.status) {
					return
				}
				ctx = context.Background()
				if !readQuery(ctx, t, p, tc.query) {
					return
				}
				if !sendPackets(ctx, t, p, []byte{0, 0, 0, tc.status, 0, 0, 0}) {
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
			if _, err = db.ExecContext(context.Background(), tc.query); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBindAmbiguousQuotes(t *testing.T) {
	for _, query := range []string{`SELECT 1 AS "a\", ?`, `SELECT "a\" ? ", ?`} {
		if _, err := buildBindPlan(query, ModeMySQL); !errors.Is(err, ErrBind) {
			t.Fatalf("ambiguous interpolation spans accepted: %q %v", query, err)
		}
	}
}

func TestMultiStatementsCapability(t *testing.T) {
	for _, allow := range []bool{false, true} {
		c := &connector{cfg: Config{AllowMultiStatements: allow}}
		flags, err := c.clientCapabilities(wire.ClientProtocol41 | wire.ClientSecureConnection | wire.ClientMultiStatements)
		if err != nil || (flags&wire.ClientMultiStatements != 0) != allow {
			t.Fatalf("allow=%v flags=%x err=%v", allow, flags, err)
		}
	}
}

type countingValuer struct{ calls *int }

func (v countingValuer) Value() (driver.Value, error) { *v.calls++; return int64(42), nil }
func TestBindConversionOnce(t *testing.T) {
	p, err := buildBindPlan("SELECT :a,:A FROM dual", ModeOracle)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	v, err := bindArguments(p, []driver.NamedValue{{Name: "a", Value: countingValuer{&calls}}})
	if err != nil || calls != 1 || len(v) != 2 || v[0].value != int64(42) || v[1].value != int64(42) {
		t.Fatal(v, calls, err)
	}
	var nilValuer *countingValuer
	if v, err := convertArgument(nilValuer); err != nil || v != nil {
		t.Fatal(v, err)
	}
	n := uint64(math.MaxUint64)
	if v, err := convertArgument(&n); err != nil || v != n {
		t.Fatal(v, err)
	}
}
