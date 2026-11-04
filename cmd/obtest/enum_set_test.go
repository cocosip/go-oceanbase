package main

import (
	"context"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	enumSetCreateFix = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,E ENUM('a','b','c'),S SET('x','y','z')) ENGINE=InnoDB"
	enumSetInsertFix = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	enumSetTextFix   = "SELECT E,S FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	enumSetBoundFix  = "SELECT E,S FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
	enumSetCountFix  = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID>=90"
)

type enumSetFixtureCase struct {
	name   string
	mutate func(r *enumSetRows)
	reject uint32
	wantOK bool
}

func enumSetFixtureCases() []enumSetFixtureCase {
	return []enumSetFixtureCase{
		{name: caseRoundTrip, wantOK: true},
		{name: "enum-text-changed", mutate: func(r *enumSetRows) { r.values[0][0] = "c" }},
		{name: "set-normalization-changed", mutate: func(r *enumSetRows) { r.values[3][1] = "z,y" }},
		{name: caseNullChanged, mutate: func(r *enumSetRows) { r.values[2] = []driver.Value{"b", "x"} }},
		{name: caseMetadataType, mutate: func(r *enumSetRows) { r.database = blobType }},
		{name: caseExtraRow, mutate: func(r *enumSetRows) { r.values = append(r.values, r.values[0]) }},
		{name: caseRejectionCode, reject: 1064},
	}
}

func TestMySQLEnumSetWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, enumSetCase, oceanbase.ModeMySQL)
	for _, tc := range enumSetFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := enumSetSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("mysql enum/set workflow: %+v; want %s", result, want)
			}
		})
	}
}

func enumSetSteps(t *testing.T, tc enumSetFixtureCase) []fixtureStep {
	t.Helper()
	body := []fixtureStep{{query: enumSetCreateFix}}
	for _, seed := range enumSetSeeds() {
		body = append(body, fixtureStep{query: enumSetInsertFix, inspect: enumSetInspect(t, seed)})
	}
	rows := newEnumSetRows()
	if tc.mutate != nil {
		tc.mutate(rows)
	}
	body = append(body, fixtureStep{query: enumSetTextFix, rows: rows})
	switch {
	case tc.mutate != nil:
	case tc.reject != 0:
		body = append(body,
			fixtureStep{query: enumSetBoundFix, rows: newEnumSetRows()},
			fixtureStep{query: enumSetInsertFix, err: &oceanbase.Error{Code: tc.reject}},
		)
	default:
		body = append(body,
			fixtureStep{query: enumSetBoundFix, rows: newEnumSetRows()},
			fixtureStep{query: enumSetInsertFix, err: &oceanbase.Error{Code: enumSetRejectCode}},
			fixtureStep{query: enumSetInsertFix, err: &oceanbase.Error{Code: enumSetRejectCode}},
			fixtureStep{query: enumSetCountFix, values: [][]driver.Value{{int64(0)}}},
		)
	}
	return append(body, fixtureStep{query: fixtureDropTableSQL})
}

func enumSetInspect(t *testing.T, seed enumSetSeed) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 3 || fixtureArgText(args[0].Value) != fmt.Sprint(seed.id) {
			t.Errorf("ENUM/SET insert %d bound unexpected arguments", seed.id)
			return
		}
		if fixtureArgText(args[1].Value) != fixtureArgText(seed.e) || fixtureArgText(args[2].Value) != fixtureArgText(seed.s) {
			t.Errorf("ENUM/SET insert %d bound unexpected values", seed.id)
		}
	}
}

type enumSetRows struct {
	fixtureRows
	database string
}

func newEnumSetRows() *enumSetRows {
	rows := &enumSetRows{database: enumSetTypeName}
	for _, seed := range enumSetSeeds() {
		e, s := any(nil), any(nil)
		if seed.e != nil {
			e = seed.eWant
		}
		if seed.s != nil {
			s = seed.sWant
		}
		rows.values = append(rows.values, []driver.Value{e, s})
	}
	rows.width = 2
	return rows
}

func (r *enumSetRows) ColumnTypeDatabaseTypeName(int) string { return r.database }
func (*enumSetRows) ColumnTypeScanType(int) reflect.Type     { return reflect.TypeFor[string]() }
