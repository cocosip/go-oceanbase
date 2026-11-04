package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const floatsCase = "floats"

func modeNeutralScenario(t *testing.T, name string) caseDefinition {
	t.Helper()
	for _, definition := range caseDefinitions() {
		if definition.name == name {
			if definition.run == nil || definition.mysqlOnly || definition.oracleOnly {
				t.Fatalf("%s scenario is missing or unexpectedly mode gated", name)
			}
			return definition
		}
	}
	t.Fatalf("%s scenario is not registered", name)
	return caseDefinition{}
}

func floatCreateSQL(mode oceanbase.Mode) string {
	if mode == oceanbase.ModeOracle {
		return "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V BINARY_DOUBLE,LABEL VARCHAR(80))"
	}
	return "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V DOUBLE,LABEL VARCHAR(80)) ENGINE=InnoDB"
}

const (
	floatInsertSQL = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	floatSelectSQL = "SELECT V,LABEL FROM OBT_OFFLINE_T WHERE ID=?"
	floatCountSQL  = "SELECT COUNT(*) FROM OBT_OFFLINE_T"
	floatZeroSQL   = "INSERT INTO OBT_OFFLINE_T VALUES (9,TO_BINARY_DOUBLE('-0'),'literal-zero')"
	floatDumpSQL   = "SELECT DUMP(V) FROM OBT_OFFLINE_T WHERE ID=?"
	floatTextSQL   = "SELECT TO_CHAR(V) FROM OBT_OFFLINE_T WHERE ID=?"
	floatZeroDump  = "Typ=12 Len=8: 0,0,0,0,0,0,0,128"
	floatStopValue = "read-value"
	floatStopDump  = "dump"
	floatStopText  = "stored-text"
	floatStopCount = "row-count"
)

type floatCase struct {
	name   string
	mode   oceanbase.Mode
	value  func(id int, v driver.Value) driver.Value
	dump   func(id int, dump string) string
	text   func(id int, text string) string
	count  int
	stop   string
	failID int
	wantOK bool
}

func floatCases() []floatCase {
	return []floatCase{
		{name: "mysql-round-trip", mode: oceanbase.ModeMySQL, wantOK: true},
		{name: "mysql-positive-zero", mode: oceanbase.ModeMySQL, stop: floatStopValue, failID: 6, value: func(id int, v driver.Value) driver.Value {
			if id == 6 {
				return float64(0)
			}
			return v
		}},
		{name: "mysql-null-valid", mode: oceanbase.ModeMySQL, stop: floatStopValue, failID: 7, value: func(id int, v driver.Value) driver.Value {
			if id == 7 {
				return float64(1)
			}
			return v
		}},
		{name: "mysql-widened", mode: oceanbase.ModeMySQL, stop: floatStopValue, failID: 8, value: func(id int, v driver.Value) driver.Value {
			if id == 8 {
				return 1.1
			}
			return v
		}},
		{name: "mysql-count", mode: oceanbase.ModeMySQL, stop: floatStopCount, count: 7},
		{name: "oracle-round-trip", mode: oceanbase.ModeOracle, wantOK: true},
		{name: "oracle-normalized-zero", mode: oceanbase.ModeOracle, wantOK: true, value: func(id int, v driver.Value) driver.Value {
			if id == 6 {
				return float64(0)
			}
			return v
		}},
		{name: "oracle-zero-dump", mode: oceanbase.ModeOracle, stop: floatStopDump, dump: func(id int, dump string) string {
			if id == 9 {
				return "Typ=12 Len=8: 0,0,0,0,0,0,0,0"
			}
			return dump
		}},
		{name: "oracle-stored-text", mode: oceanbase.ModeOracle, stop: floatStopText, text: func(id int, text string) string {
			if id == 1 {
				return "2.5"
			}
			return text
		}},
		{name: "oracle-count", mode: oceanbase.ModeOracle, stop: floatStopCount, count: 8},
	}
}

func TestFloatsWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, floatsCase)
	for _, tc := range floatCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps, truncated := floatSteps(t, tc)
			if truncated != (tc.stop != "") {
				t.Fatalf("step plan truncated=%v but stop=%q", truncated, tc.stop)
			}
			s := fixtureScenario(t, steps)
			s.mode = tc.mode
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("floats workflow: %+v; want %s", result, want)
			}
		})
	}
}

func floatSteps(t *testing.T, tc floatCase) ([]fixtureStep, bool) {
	t.Helper()
	inputs := floatInputs()
	steps := []fixtureStep{{query: floatCreateSQL(tc.mode)}}
	for _, input := range inputs {
		want := []any{input.id, input.value, input.label}
		steps = append(steps, fixtureStep{query: floatInsertSQL, inspect: func(got []driver.NamedValue) {
			values := make([]any, len(got))
			for i, arg := range got {
				values[i] = arg.Value
			}
			if !reflect.DeepEqual(values, want) {
				t.Errorf("float insert %d bound unexpected arguments", input.id)
			}
		}})
	}
	for _, input := range inputs {
		value := driver.Value(input.want.Float64)
		if !input.want.Valid {
			value = nil
		}
		if tc.value != nil {
			value = tc.value(input.id, value)
		}
		steps = append(steps, fixtureStep{query: floatSelectSQL, values: [][]driver.Value{{value, input.label}}})
		if tc.stop == floatStopValue && input.id == tc.failID {
			return append(steps, fixtureStep{query: fixtureDropTableSQL}), true
		}
	}
	if tc.mode == oceanbase.ModeOracle {
		steps = append(steps, fixtureStep{query: floatZeroSQL})
		for _, id := range []int{6, 9} {
			dump := floatZeroDump
			if tc.dump != nil {
				dump = tc.dump(id, dump)
			}
			steps = append(steps, fixtureStep{query: floatDumpSQL, values: [][]driver.Value{{dump}}})
			if tc.stop == floatStopDump && id == 9 {
				return append(steps, fixtureStep{query: fixtureDropTableSQL}), true
			}
		}
		for _, id := range []int{1, 2} {
			text := map[int]string{1: "1.5", 2: "1.1"}[id]
			if tc.text != nil {
				text = tc.text(id, text)
			}
			steps = append(steps, fixtureStep{query: floatTextSQL, values: [][]driver.Value{{text}}})
			if tc.stop == floatStopText && id == 1 {
				return append(steps, fixtureStep{query: fixtureDropTableSQL}), true
			}
		}
	}
	total := len(inputs)
	if tc.mode == oceanbase.ModeOracle {
		total++
	}
	if tc.count != 0 {
		total = tc.count
	}
	steps = append(steps, fixtureStep{query: floatCountSQL, values: [][]driver.Value{{int64(total)}}})
	return append(steps, fixtureStep{query: fixtureDropTableSQL}), tc.stop == floatStopCount
}
