package sqltext

import (
	"database/sql/driver"
	"github.com/cocosip/go-oceanbase/config"
	"math"
	"testing"
)

func TestBindPlan(t *testing.T) {
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
			plan, err := BuildBindPlan(tc.query, tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if plan.SQL != tc.sql {
				t.Fatalf("got %q, want %q", plan.SQL, tc.sql)
			}
			if plan.NumInput != len(tc.args) || len(plan.Slots) != len(tc.values) {
				t.Fatal(plan, tc.args, tc.values)
			}
		})
	}
}

func TestBindInvalid(t *testing.T) {
	for _, tc := range []struct {
		query string
		mode  config.Mode
	}{
		{"SELECT :x, ? FROM dual", config.ModeOracle},
		{"SELECT :1, ? FROM dual", config.ModeOracle},
		{"SELECT :0 FROM dual", config.ModeOracle},
		{"SELECT :999999999999999999999999 FROM dual", config.ModeOracle},
		{"SELECT 'unterminated ?", config.ModeMySQL},
		{"SELECT /* unterminated ?", config.ModeMySQL},
		{"SELECT q'[unterminated' FROM dual", config.ModeOracle},
		{"SELECT /*! ? */ 1", config.ModeMySQL},
	} {
		t.Run(tc.query, func(t *testing.T) {
			if _, err := BuildBindPlan(tc.query, tc.mode); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}
