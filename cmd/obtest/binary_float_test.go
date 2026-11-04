package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	binaryFloatCreateSQL = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,F BINARY_FLOAT,G BINARY_DOUBLE)"
	binaryFloatInsertFix = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	binaryFloatTextFix   = "SELECT F,G FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	binaryFloatBoundFix  = "SELECT F,G FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
)

type binaryFloatFixtureCase struct {
	name   string
	mutate func(r *binaryFloatRows)
	wantOK bool
}

func binaryFloatFixtureCases() []binaryFloatFixtureCase {
	return []binaryFloatFixtureCase{
		{name: caseRoundTrip, wantOK: true},
		{name: "float32-precision", mutate: func(r *binaryFloatRows) { r.values[2][0] = 0.1 }},
		{name: "double-changed", mutate: func(r *binaryFloatRows) { r.values[2][1] = float64(float32(0.1)) }},
		{name: "nan-changed", mutate: func(r *binaryFloatRows) { r.values[3][0] = 1.0 }},
		{name: "infinity-changed", mutate: func(r *binaryFloatRows) { r.values[4][0] = 0.0 }},
		{name: caseNullChanged, mutate: func(r *binaryFloatRows) { r.values[7] = []driver.Value{0.0, 0.0} }},
		{name: caseExtraRow, mutate: func(r *binaryFloatRows) { r.values = append(r.values, r.values[0]) }},
	}
}

func TestOracleBinaryFloatWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, binaryFloatCase, oceanbase.ModeOracle)
	for _, tc := range binaryFloatFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := binaryFloatSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle binary float workflow: %+v; want %s", result, want)
			}
		})
	}
}

func binaryFloatSteps(t *testing.T, tc binaryFloatFixtureCase) []fixtureStep {
	t.Helper()
	steps := []fixtureStep{{query: binaryFloatCreateSQL}}
	for _, seed := range binaryFloatSeeds() {
		steps = append(steps, fixtureStep{query: binaryFloatInsertFix, inspect: binaryFloatInspect(t, seed)})
	}
	rows := newBinaryFloatRows()
	if tc.mutate != nil {
		tc.mutate(rows)
	}
	steps = append(steps, fixtureStep{query: binaryFloatTextFix, rows: rows})
	if tc.mutate != nil {
		return append(steps, fixtureStep{query: fixtureDropTableSQL})
	}
	return append(steps,
		fixtureStep{query: binaryFloatBoundFix, rows: newBinaryFloatRows()},
		fixtureStep{query: fixtureDropTableSQL},
	)
}

func binaryFloatInspect(t *testing.T, seed binaryFloatSeed) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 3 || fmt.Sprint(args[0].Value) != fmt.Sprint(seed.id) {
			t.Errorf("binary float insert %d bound unexpected arguments", seed.id)
			return
		}
		if !sameArgFloat(args[1].Value, seed.v) || !sameArgFloat(args[2].Value, seed.v) {
			t.Errorf("binary float insert %d bound unexpected value", seed.id)
		}
	}
}

func sameArgFloat(got, want any) bool {
	if want == nil {
		return got == nil
	}
	gotFloat, ok := got.(float64)
	if !ok {
		return false
	}
	wantFloat, ok := want.(float64)
	if !ok {
		return false
	}
	if math.IsNaN(wantFloat) {
		return math.IsNaN(gotFloat)
	}
	return gotFloat == wantFloat
}

func newBinaryFloatRows() *binaryFloatRows {
	rows := &binaryFloatRows{fixtureRows: fixtureRows{width: 2}}
	for _, seed := range binaryFloatSeeds() {
		rows.values = append(rows.values, []driver.Value{floatValue(seed.float), floatValue(seed.double)})
	}
	return rows
}

func floatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

type binaryFloatRows struct {
	fixtureRows
}

func (*binaryFloatRows) ColumnTypeDatabaseTypeName(index int) string {
	if index == 0 {
		return binaryFloatType
	}
	return binaryDoubleType
}

func (*binaryFloatRows) ColumnTypeScanType(int) reflect.Type { return reflect.TypeFor[float64]() }
