package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	numberScaleCreateSQL = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V NUMBER(10,-2),W NUMBER(5,8))"
	numberScaleInsertFix = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	numberScaleTextFix   = "SELECT V,W FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	numberScaleBoundFix  = "SELECT V,W FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
	numberScaleCountFix  = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID>=90"
)

type numberScaleFixtureCase struct {
	name   string
	mutate func(r *numberScaleRows)
	reject int
	wantOK bool
}

func numberScaleFixtureCases() []numberScaleFixtureCase {
	return []numberScaleFixtureCase{
		{name: caseRoundTrip, wantOK: true},
		{name: "precision-changed", mutate: func(r *numberScaleRows) { r.sizes[0][0] = 11 }},
		{name: "scale-changed", mutate: func(r *numberScaleRows) { r.sizes[0][1] = -3 }},
		{name: "rounding-changed", mutate: func(r *numberScaleRows) { r.values[0][0] = "12345" }},
		{name: caseNullChanged, mutate: func(r *numberScaleRows) { r.values[4] = []driver.Value{"0", "0"} }},
		{name: caseExtraRow, mutate: func(r *numberScaleRows) { r.values = append(r.values, r.values[0]) }},
		{name: caseRejectionCode, reject: 1064},
	}
}

func TestOracleNumberScaleWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, numberScaleCase, oceanbase.ModeOracle)
	for _, tc := range numberScaleFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps, expected := numberScaleSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle number scale workflow: %+v; want %s (%s)", result, want, expected)
			}
		})
	}
}

func numberScaleSteps(t *testing.T, tc numberScaleFixtureCase) ([]fixtureStep, string) {
	t.Helper()
	steps := []fixtureStep{{query: numberScaleCreateSQL}}
	for _, seed := range numberScaleSeeds() {
		steps = append(steps, fixtureStep{query: numberScaleInsertFix, inspect: numberScaleInspect(t, seed)})
	}
	rows := newNumberScaleRows()
	if tc.mutate != nil {
		tc.mutate(rows)
	}
	steps = append(steps, fixtureStep{query: numberScaleTextFix, rows: rows})
	drop := fixtureStep{query: fixtureDropTableSQL}
	if tc.mutate != nil && tc.reject == 0 {
		return append(steps, drop), tc.name
	}
	steps = append(steps, fixtureStep{query: numberScaleBoundFix, rows: newNumberScaleRows()})
	if tc.reject != 0 {
		steps = append(steps,
			fixtureStep{query: numberScaleInsertFix, err: &oceanbase.Error{Code: uint32(tc.reject)}},
			drop,
		)
		return steps, tc.name
	}
	steps = append(steps,
		fixtureStep{query: numberScaleInsertFix, err: &oceanbase.Error{Code: numberScaleOverflowErr}},
		fixtureStep{query: numberScaleInsertFix, err: &oceanbase.Error{Code: numberScaleOverflowErr}},
		fixtureStep{query: numberScaleCountFix, values: [][]driver.Value{{int64(0)}}},
		drop,
	)
	return steps, tc.name
}

func numberScaleInspect(t *testing.T, seed numberScaleSeed) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 3 || fmt.Sprint(args[0].Value) != fmt.Sprint(seed.id) {
			t.Errorf("NUMBER insert bound unexpected arguments")
			return
		}
		if fixtureArgText(args[1].Value) != fixtureArgText(seed.v) || fixtureArgText(args[2].Value) != fixtureArgText(seed.w) {
			t.Errorf("NUMBER insert %d bound unexpected values", seed.id)
		}
	}
}

type numberScaleRows struct {
	fixtureRows
	sizes [][2]int64
}

func newNumberScaleRows() *numberScaleRows {
	rows := &numberScaleRows{sizes: [][2]int64{{10, -2}, {5, 8}}}
	for _, seed := range numberScaleSeeds() {
		rows.values = append(rows.values, []driver.Value{nullableValue(seed.vWant), nullableValue(seed.wWant)})
	}
	rows.width = 2
	return rows
}

func (*numberScaleRows) ColumnTypeDatabaseTypeName(int) string { return metadataFixtureNumber }
func (*numberScaleRows) ColumnTypeScanType(int) reflect.Type   { return reflect.TypeFor[string]() }
func (r *numberScaleRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	return r.sizes[index][0], r.sizes[index][1], true
}

func nullableValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
