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
	generatedCreateFix = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,A INTEGER,B INTEGER AS (A*2) STORED,V INTEGER AS (A+1) VIRTUAL) ENGINE=InnoDB"
	generatedInsertFix = "INSERT INTO OBT_OFFLINE_T (ID,A) VALUES (?,?)"
	generatedUpdateFix = "UPDATE OBT_OFFLINE_T SET A=? WHERE ID=?"
	generatedTextFix   = "SELECT A,B,V FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	generatedBoundFix  = "SELECT A,B,V FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
	generatedRejectFix = "INSERT INTO OBT_OFFLINE_T (ID,A,B) VALUES (?,?,?)"
	generatedCountFix  = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID>=90"
)

type generatedFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep)
	reject uint32
	wantOK bool
}

func generatedFixtureCases() []generatedFixtureCase {
	return []generatedFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "computed-changed", failAt: 4, mutate: func(body *[]fixtureStep) {
			(*body)[4].rows = newGeneratedRows([][]driver.Value{{int64(21), int64(41), int64(22)}, {nil, nil, nil}, {int64(0), int64(0), int64(1)}})
		}},
		{name: "virtual-changed", failAt: 4, mutate: func(body *[]fixtureStep) {
			(*body)[4].rows = newGeneratedRows([][]driver.Value{{int64(21), int64(42), int64(23)}, {nil, nil, nil}, {int64(0), int64(0), int64(1)}})
		}},
		{name: caseNullChanged, failAt: 4, mutate: func(body *[]fixtureStep) {
			(*body)[4].rows = newGeneratedRows([][]driver.Value{{int64(21), int64(42), int64(22)}, {int64(0), int64(0), int64(0)}, {int64(0), int64(0), int64(1)}})
		}},
		{name: caseMetadataType, failAt: 4, mutate: func(body *[]fixtureStep) {
			rows := newGeneratedRows(generatedInitialValues())
			rows.database = blobType
			(*body)[4].rows = rows
		}},
		{name: caseExtraRow, failAt: 4, mutate: func(body *[]fixtureStep) {
			rows := newGeneratedRows(generatedInitialValues())
			rows.values = append(rows.values, rows.values[0])
			(*body)[4].rows = rows
		}},
		{name: "recomputed-changed", failAt: 6, mutate: func(body *[]fixtureStep) {
			(*body)[6].rows = newGeneratedRows([][]driver.Value{{int64(10), int64(21), int64(11)}, {nil, nil, nil}, {int64(0), int64(0), int64(1)}})
		}},
		{name: caseRejectionCode, failAt: 7, reject: 1064},
	}
}

func TestMySQLGeneratedColumnsWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, generatedCase, oceanbase.ModeMySQL)
	for _, tc := range generatedFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := generatedSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("mysql generated columns workflow: %+v; want %s", result, want)
			}
		})
	}
}

func generatedSteps(t *testing.T, tc generatedFixtureCase) []fixtureStep {
	t.Helper()
	body := []fixtureStep{{query: generatedCreateFix}}
	for id, a := range []any{21, nil, 0} {
		body = append(body, fixtureStep{query: generatedInsertFix, inspect: generatedInsertInspect(t, id+1, a)})
	}
	body = append(body, fixtureStep{query: generatedTextFix, rows: newGeneratedRows(generatedInitialValues())})
	body = append(body, fixtureStep{query: generatedUpdateFix, inspect: generatedUpdateInspect(t)})
	body = append(body, fixtureStep{query: generatedBoundFix, rows: newGeneratedRows(generatedUpdatedValues())})
	if tc.failAt < 0 {
		body = append(body,
			fixtureStep{query: generatedRejectFix, err: &oceanbase.Error{Code: generatedRejected}},
			fixtureStep{query: generatedCountFix, values: [][]driver.Value{{int64(0)}}},
		)
	}
	if tc.mutate != nil {
		tc.mutate(&body)
	}
	if tc.failAt >= 0 {
		if tc.reject != 0 {
			body = append(body, fixtureStep{query: generatedRejectFix, err: &oceanbase.Error{Code: tc.reject}})
		}
		body = body[:tc.failAt+1]
	}
	return append(body, fixtureStep{query: fixtureDropTableSQL})
}

func generatedInsertInspect(t *testing.T, id int, a any) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(id) || fixtureArgText(args[1].Value) != fixtureArgText(a) {
			t.Errorf("generated column insert %d bound unexpected arguments", id)
		}
	}
}

func generatedUpdateInspect(t *testing.T) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fmt.Sprint(args[0].Value) != "10" || fmt.Sprint(args[1].Value) != "1" {
			t.Error("generated column update bound unexpected arguments")
		}
	}
}

type generatedRows struct {
	fixtureRows
	database string
}

func newGeneratedRows(values [][]driver.Value) *generatedRows {
	return &generatedRows{database: generatedTypeName, fixtureRows: fixtureRows{width: 3, values: values}}
}

func generatedInitialValues() [][]driver.Value {
	return [][]driver.Value{
		{int64(21), int64(42), int64(22)},
		{nil, nil, nil},
		{int64(0), int64(0), int64(1)},
	}
}

func generatedUpdatedValues() [][]driver.Value {
	return [][]driver.Value{
		{int64(10), int64(20), int64(11)},
		{nil, nil, nil},
		{int64(0), int64(0), int64(1)},
	}
}

func (r *generatedRows) ColumnTypeDatabaseTypeName(int) string { return r.database }
func (*generatedRows) ColumnTypeScanType(int) reflect.Type     { return reflect.TypeFor[int64]() }
