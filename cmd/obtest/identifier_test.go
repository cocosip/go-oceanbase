package main

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const identifierFixturePrefix = "OBT_OFFLINE"

type identifierFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep, mode oceanbase.Mode)
	wantOK bool
}

func identifierFixtureCases() []identifierFixtureCase {
	return []identifierFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "table-name", failAt: 5, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newIdentifierTableRows(mode)
			rows.values[0][2] = identifierFixtureTable(mode) + "X"
			(*body)[5].rows = rows
		}},
		{name: "long-column", failAt: 6, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newIdentifierColumnRows(mode)
			rows.values[1][3] = identifierColumn()[:identifierColumnLen-1]
			(*body)[6].rows = rows
		}},
		{name: "insert-value", failAt: 4, mutate: func(body *[]fixtureStep, _ oceanbase.Mode) {
			(*body)[4].values = [][]driver.Value{{int64(1), int64(43)}}
		}},
		{name: "rejected-table", failAt: 2, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			(*body)[1].err = nil
			extra := fixtureStep{query: "DROP TABLE " + identifierTooLong(identifierFixturePrefix, identifierLimit(mode))}
			*body = append((*body)[:2], append([]fixtureStep{extra}, (*body)[2:]...)...)
		}},
		{name: "rejected-column", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			(*body)[2].err = nil
			extra := fixtureStep{query: "ALTER TABLE " + identifierFixtureTable(mode) + " DROP COLUMN " + strings.Repeat("X", identifierColumnLen+1)}
			*body = append((*body)[:3], append([]fixtureStep{extra}, (*body)[3:]...)...)
		}},
	}
}

func TestIdentifierLengthWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, identifierCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range identifierFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := identifierSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || len(result.CleanupErrors) != 0 {
					t.Fatalf("identifier length workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
				for _, object := range result.Objects {
					if object.State != fixtureDroppedState {
						t.Fatalf("identifier object %s state=%s", object.Name, object.State)
					}
				}
			})
		}
	}
}

func identifierFixtureTable(mode oceanbase.Mode) string {
	limit := identifierLimit(mode)
	return identifierFixturePrefix + "_" + identifierSuffix(limit, identifierFixturePrefix)
}

func identifierLimit(mode oceanbase.Mode) int {
	if mode == oceanbase.ModeOracle {
		return identifierOracleTable
	}
	return identifierMySQLTable
}

func identifierSteps(t *testing.T, mode oceanbase.Mode, tc identifierFixtureCase) []fixtureStep {
	t.Helper()
	table := identifierFixtureTable(mode)
	column := identifierColumn()
	create := "CREATE TABLE " + table + " " + identifierDDL(column)
	if mode == oceanbase.ModeMySQL {
		create += parameterFixtureEngine
	}
	body := []fixtureStep{
		{query: create},
		{query: "CREATE TABLE " + identifierTooLong(identifierFixturePrefix, identifierLimit(mode)) + " (ID INTEGER PRIMARY KEY)", err: &oceanbase.Error{Code: identifierRejectionCode(mode)}},
		{query: "ALTER TABLE " + table + " ADD " + strings.Repeat("X", identifierColumnLen+1) + " INTEGER", err: &oceanbase.Error{Code: identifierRejectionCode(mode)}},
		{query: "INSERT INTO " + table + " (ID," + column + ") VALUES (?,?)", inspect: identifierArgs(t)},
		{query: "SELECT ID," + column + " FROM " + table, values: [][]driver.Value{{int64(1), int64(42)}}},
		{query: "", rows: newIdentifierTableRows(mode)},
		{query: "", rows: newIdentifierColumnRows(mode)},
	}
	if tc.mutate != nil {
		tc.mutate(&body, mode)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return append(body, fixtureStep{query: "DROP TABLE " + table})
}

func identifierRejectionCode(mode oceanbase.Mode) uint32 {
	if mode == oceanbase.ModeOracle {
		return identifierOracleCode
	}
	return identifierMySQLCode
}

func identifierArgs(t *testing.T) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fixtureArgText(args[0].Value) != "1" || fixtureArgText(args[1].Value) != "42" {
			t.Error("long-identifier insert bound unexpected values")
		}
	}
}

type identifierTableRows struct {
	fixtureRows
}

func newIdentifierTableRows(mode oceanbase.Mode) *identifierTableRows {
	return &identifierTableRows{fixtureRows: fixtureRows{width: 5, values: [][]driver.Value{
		{nil, "S", identifierFixtureTable(mode), viewTypeTable, nil},
	}}}
}

type identifierColumnRows struct {
	fixtureRows
}

func newIdentifierColumnRows(mode oceanbase.Mode) *identifierColumnRows {
	table := identifierFixtureTable(mode)
	return &identifierColumnRows{fixtureRows: fixtureRows{width: 15, values: [][]driver.Value{
		{nil, "S", table, "ID", int64(1), metadataFixtureNumber, int64(22), int64(38), int64(0), int64(0), nil, nil, nil, nil, int64(0)},
		{nil, "S", table, identifierColumn(), int64(2), metadataFixtureNumber, int64(22), int64(38), int64(0), int64(1), nil, nil, nil, nil, int64(0)},
	}}}
}
