package main

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	urowidCase       = "oracle-urowid"
	urowidCreateSQL  = "CREATE TABLE OBT_OFFLINE_T (ID VARCHAR2(1000) PRIMARY KEY,U UROWID(4000))"
	urowidSourceSQL  = "INSERT INTO OBT_OFFLINE_T VALUES (?,NULL)"
	urowidRowidSQL   = "SELECT ROWID FROM OBT_OFFLINE_T"
	urowidInsertSQL  = "INSERT INTO OBT_OFFLINE_T VALUES (?,?)"
	urowidReadSQL    = "SELECT U FROM OBT_OFFLINE_T WHERE ID=?"
	urowidINOUTSQL   = "BEGIN ? := ?; END;"
	urowidCountSQL   = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID LIKE 'row-%'"
	urowidFixtureLen = 300
	urowidStopRead   = "read"
	urowidStopRowid  = "rowid"
)

var urowidFixture = strings.Repeat("A", urowidFixtureLen)

type urowidWorkflowCase struct {
	name   string
	rowid  string
	read   func(i int, v driver.Value) driver.Value
	count  int
	stop   string
	wantOK bool
}

func urowidCases() []urowidWorkflowCase {
	return []urowidWorkflowCase{
		{name: "long-rowid-round-trip", wantOK: true},
		{name: "short-rowid", rowid: strings.Repeat("A", 40), stop: urowidStopRowid},
		{name: "null-valid", stop: urowidStopRead, read: func(i int, v driver.Value) driver.Value {
			if i == 1 {
				return nil
			}
			return v
		}},
		{name: "value-changed", stop: urowidStopRead, read: func(i int, v driver.Value) driver.Value {
			if i == 1 {
				return strings.Repeat("B", urowidFixtureLen)
			}
			return v
		}},
		{name: "row-count", stop: "count", count: 2},
	}
}

func TestOracleURowIDWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, urowidCase, oceanbase.ModeOracle)
	for _, tc := range urowidCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps, truncated := urowidSteps(t, tc)
			if truncated != (tc.stop != "") {
				t.Fatalf("step plan truncated=%v but stop=%q", truncated, tc.stop)
			}
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle urowid workflow: %+v; want %s", result, want)
			}
		})
	}
}

func urowidSteps(t *testing.T, tc urowidWorkflowCase) ([]fixtureStep, bool) {
	t.Helper()
	rowid := tc.rowid
	if rowid == "" {
		rowid = urowidFixture
	}
	steps := []fixtureStep{
		{query: urowidCreateSQL},
		{query: urowidSourceSQL, inspect: func(args []driver.NamedValue) {
			if len(args) != 1 || args[0].Value != urowidSource {
				t.Errorf("UROWID source bound unexpected key length")
			}
		}},
		{query: urowidRowidSQL, values: [][]driver.Value{{rowid}}},
	}
	if tc.stop == urowidStopRowid {
		return append(steps, fixtureStep{query: fixtureDropTableSQL}), true
	}
	drop := fixtureStep{query: fixtureDropTableSQL}
	for i, want := range []driver.Value{nil, rowid, nil} {
		steps = append(steps, fixtureStep{query: urowidInsertSQL, inspect: urowidInsertInspect(t, i, want)})
		got := want
		if tc.read != nil {
			got = tc.read(i, want)
		}
		steps = append(steps, fixtureStep{query: urowidReadSQL, values: [][]driver.Value{{got}}})
		if tc.stop == urowidStopRead && i == 1 {
			return append(steps, drop), true
		}
		steps = append(steps, fixtureStep{query: urowidINOUTSQL})
	}
	count := int64(3)
	if tc.count != 0 {
		count = int64(tc.count)
	}
	steps = append(steps, fixtureStep{query: urowidCountSQL, values: [][]driver.Value{{count}}})
	return append(steps, drop), tc.stop == "count"
}

func urowidInsertInspect(t *testing.T, index int, want driver.Value) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || args[0].Value == nil {
			t.Errorf("UROWID insert %d bound unexpected arity", index)
			return
		}
		if got := fixtureArgText(args[1].Value); got != fmt.Sprint(want) {
			t.Errorf("UROWID insert %d bound %q want %q", index, got, fmt.Sprint(want))
		}
	}
}
