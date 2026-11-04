package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	viewTableFix    = "OBT_OFFLINE_VIEWT"
	viewViewFix     = "OBT_OFFLINE_VIEWV"
	viewCreateMY    = "CREATE TABLE OBT_OFFLINE_VIEWT (ID INTEGER PRIMARY KEY,V VARCHAR(10))"
	viewCreateOR    = "CREATE TABLE OBT_OFFLINE_VIEWT (ID INTEGER PRIMARY KEY,V VARCHAR2(10))"
	viewCreateSQL   = "CREATE VIEW OBT_OFFLINE_VIEWV AS SELECT ID,V FROM OBT_OFFLINE_VIEWT WHERE ID>=1"
	viewDropSQL     = "DROP VIEW OBT_OFFLINE_VIEWV"
	viewTypeView    = "VIEW"
	viewTypeTable   = "TABLE"
	viewRowCountFix = 2
)

type viewFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep)
	wantOK bool
}

func viewFixtureCases() []viewFixtureCase {
	return []viewFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "base-rows", failAt: 5, mutate: func(body *[]fixtureStep) {
			(*body)[5].values = [][]driver.Value{{"0", "z"}}
		}},
		{name: "predicate", failAt: 6, mutate: func(body *[]fixtureStep) {
			(*body)[6].values = [][]driver.Value{{"0", "z"}, {"2", "c"}}
		}},
		{name: "delete-left", failAt: 8, mutate: func(body *[]fixtureStep) {
			(*body)[8].values = [][]driver.Value{{int64(2)}}
		}},
		{name: "view-kind", failAt: 9, mutate: func(body *[]fixtureStep) {
			(*body)[9].rows = newViewRows(viewTypeTable)
		}},
		{name: "view-missing", failAt: 9, mutate: func(body *[]fixtureStep) {
			(*body)[9].rows = newViewRows("")
		}},
		{name: "column-count", failAt: 10, mutate: func(body *[]fixtureStep) {
			rows := newViewColumnRows()
			rows.values = rows.values[:1]
			(*body)[10].rows = rows
		}},
	}
}

func TestViewWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, viewCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range viewFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := viewSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 2 || len(result.CleanupErrors) != 0 {
					t.Fatalf("view workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
				for _, object := range result.Objects {
					if object.State != fixtureDroppedState {
						t.Fatalf("view object %s state=%s", object.Name, object.State)
					}
				}
			})
		}
	}
}

func viewSteps(t *testing.T, mode oceanbase.Mode, tc viewFixtureCase) []fixtureStep {
	t.Helper()
	create := viewCreateMY + parameterFixtureEngine
	if mode == oceanbase.ModeOracle {
		create = viewCreateOR
	}
	body := []fixtureStep{
		{query: create},
		{query: viewCreateSQL},
		{query: "INSERT INTO " + viewViewFix + " (ID,V) VALUES (?,?)", inspect: viewArgs(t, "2", "b")},
		{query: "UPDATE " + viewViewFix + " SET V=? WHERE ID=?", inspect: viewArgs(t, "c", "2")},
		{query: "INSERT INTO " + viewViewFix + " (ID,V) VALUES (?,?)", inspect: viewArgs(t, "0", "z")},
		{query: "SELECT ID,V FROM " + viewTableFix + " ORDER BY ID", values: [][]driver.Value{{"0", "z"}, {"2", "c"}}},
		{query: "SELECT ID,V FROM " + viewViewFix + " ORDER BY ID", values: [][]driver.Value{{"2", "c"}}},
		{query: "DELETE FROM " + viewViewFix + " WHERE ID=?", inspect: viewArgs(t, "2")},
		{query: "SELECT COUNT(*) FROM " + viewTableFix, values: [][]driver.Value{{int64(1)}}},
		{query: "", rows: newViewRows(viewTypeView)},
		{query: "", rows: newViewColumnRows()},
	}
	if tc.mutate != nil {
		tc.mutate(&body)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return append(body,
		fixtureStep{query: viewDropSQL},
		fixtureStep{query: "DROP TABLE " + viewTableFix},
	)
}

func viewArgs(t *testing.T, want ...string) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != len(want) {
			t.Error("view statement bound unexpected argument count")
			return
		}
		for i := range want {
			if fixtureArgText(args[i].Value) != want[i] {
				t.Error("view statement bound unexpected argument")
				return
			}
		}
	}
}

type viewRows struct {
	fixtureRows
}

func newViewRows(kind string) *viewRows {
	if kind == "" {
		return &viewRows{fixtureRows: fixtureRows{width: 5}}
	}
	return &viewRows{fixtureRows: fixtureRows{width: 5, values: [][]driver.Value{
		{nil, "S", viewViewFix, kind, nil},
	}}}
}

type viewColumnRows struct {
	fixtureRows
}

func newViewColumnRows() *viewColumnRows {
	return &viewColumnRows{fixtureRows: fixtureRows{width: 15, values: [][]driver.Value{
		{nil, "S", viewViewFix, "ID", int64(1), metadataFixtureNumber, int64(22), int64(38), int64(0), int64(0), nil, nil, nil, nil, int64(0)},
		{nil, "S", viewViewFix, "V", int64(2), typeNameVarchar2, int64(10), nil, nil, int64(1), nil, nil, nil, nil, int64(0)},
	}}}
}
