package parameters

import (
	"database/sql/driver"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"math"
	"reflect"
	"testing"
)

func TestBindPlanArgumentMapping(t *testing.T) {
	const exactValue = "exact"
	for _, tc := range []struct {
		query  string
		mode   config.Mode
		args   []driver.NamedValue
		sql    string
		values []any
	}{
		{"SELECT ?, '?', `?`, \"?\" /* ? */ -- ?\n, ?", config.ModeMySQL, []driver.NamedValue{{Ordinal: 1, Value: int64(1)}, {Ordinal: 2, Value: "x"}}, "SELECT ?, '?', `?`, \"?\" /* ? */ -- ?\n, ?", []any{int64(1), "x"}},
		{"SELECT :value, :VALUE FROM dual", config.ModeOracle, []driver.NamedValue{{Name: "Value", Ordinal: 1, Value: exactValue}}, "SELECT ?, ? FROM dual", []any{exactValue, exactValue}},
		{"SELECT :2, :1, :2 FROM dual", config.ModeOracle, []driver.NamedValue{{Ordinal: 1, Value: "a"}, {Ordinal: 2, Value: "b"}}, "SELECT ?, ?, ? FROM dual", []any{"b", "a", "b"}},
		{"BEGIN :x := q'[?:hidden]'; :y := q'! :also_hidden !'; END;", config.ModeOracle, []driver.NamedValue{{Name: "x", Value: nil}, {Name: "y", Value: uint64(math.MaxUint64)}}, "BEGIN ? := q'[?:hidden]'; ? := q'! :also_hidden !'; END;", []any{nil, uint64(math.MaxUint64)}},
		{"SELECT ':a''?\\', :a FROM dual", config.ModeOracle, []driver.NamedValue{{Name: "a", Value: int64(2)}}, "SELECT ':a''?\\', ? FROM dual", []any{int64(2)}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			plan, err := sqltext.BuildBindPlan(tc.query, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if plan.SQL != tc.sql {
				t.Fatalf("got %q, want %q", plan.SQL, tc.sql)
			}
			values, err := Bind(plan, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(values, tc.values) {
				t.Fatal(values, tc.values)
			}
		})
	}
}

func TestBindInvalidArgumentMapping(t *testing.T) {
	for _, tc := range []struct {
		query string
		mode  config.Mode
		args  []driver.NamedValue
	}{
		{"SELECT ?", config.ModeMySQL, nil},
		{"SELECT TRUE", config.ModeMySQL, []driver.NamedValue{{Value: 1}}},
		{"SELECT ?", config.ModeMySQL, []driver.NamedValue{{Name: "x", Value: 1}}},
		{"SELECT :x, ? FROM dual", config.ModeOracle, []driver.NamedValue{{Name: "x", Value: 1}, {Value: 2}}},
		{"SELECT :x FROM dual", config.ModeOracle, []driver.NamedValue{{Name: "x", Value: 1}, {Name: "X", Value: 2}}},
		{"SELECT :x FROM dual", config.ModeOracle, []driver.NamedValue{{Name: "y", Value: 1}}},
		{"SELECT :1, ? FROM dual", config.ModeOracle, []driver.NamedValue{{Value: 1}, {Value: 2}}},
		{"SELECT :0 FROM dual", config.ModeOracle, nil},
		{"SELECT :999999999999999999999999 FROM dual", config.ModeOracle, nil},
		{"SELECT 'unterminated ?", config.ModeMySQL, nil},
		{"SELECT /* unterminated ?", config.ModeMySQL, nil},
		{"SELECT q'[unterminated' FROM dual", config.ModeOracle, nil},
		{"SELECT /*! ? */ 1", config.ModeMySQL, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			p, err := sqltext.BuildBindPlan(tc.query, tc.mode)
			if err == nil {
				_, err = Bind(p, tc.args)
			}
			if err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}
