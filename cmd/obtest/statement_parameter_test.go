package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

type statementParameterFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep, mode oceanbase.Mode)
	wantOK bool
}

func statementParameterFixtureCases() []statementParameterFixtureCase {
	return []statementParameterFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "bind-wrong", failAt: 0, mutate: func(body *[]fixtureStep, _ oceanbase.Mode) {
			(*body)[0].values = [][]driver.Value{{int64(8)}}
		}},
		{name: "logical-ordinal", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values[1][1] = "3"
			(*body)[1].rows = rows
		}},
		{name: "physical-order", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values[0][0] = "2"
			(*body)[1].rows = rows
		}},
		{name: "missing-parameter", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values = rows.values[:1]
			(*body)[1].rows = rows
		}},
		{name: "direction", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values[0][10] = "1"
			(*body)[1].rows = rows
		}},
		{name: "data-type", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values[0][4] = ""
			(*body)[1].rows = rows
		}},
		{name: "column-count", failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values[0][9] = nil
			(*body)[1].rows = rows
		}},
		{name: caseExtraRow, failAt: 1, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newStatementParameterRows(mode)
			rows.values = append(rows.values, rows.values[1])
			(*body)[1].rows = rows
		}},
	}
}

func TestStatementParameterWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, statementParameterCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range statementParameterFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := statementParameterSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 0 || len(result.CleanupErrors) != 0 {
					t.Fatalf("statement parameter workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
			})
		}
	}
}

func statementParameterSteps(t *testing.T, mode oceanbase.Mode, tc statementParameterFixtureCase) []fixtureStep {
	t.Helper()
	query := statementParameterMySQL
	if mode == oceanbase.ModeOracle {
		query = statementParameterOra
	}
	body := []fixtureStep{
		{query: query, values: [][]driver.Value{{int64(7)}}, inspect: statementParameterInspect(t)},
		{query: "", rows: newStatementParameterRows(mode)},
	}
	if tc.mutate != nil {
		tc.mutate(&body, mode)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return body
}

func statementParameterInspect(t *testing.T) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fixtureArgText(args[0].Value) != "3" || fixtureArgText(args[1].Value) != "4" {
			t.Error("statement parameters bound unexpected values")
		}
	}
}

type statementParameterRows struct {
	fixtureRows
}

func newStatementParameterRows(mode oceanbase.Mode) *statementParameterRows {
	nullable := driver.Value(nil)
	if mode == oceanbase.ModeOracle {
		nullable = "1"
	}
	return &statementParameterRows{fixtureRows: fixtureRows{width: 11, values: [][]driver.Value{
		{"1", "1", nil, nil, statementParameterType, nullable, nil, nil, nil, "8", "0"},
		{"2", "2", nil, nil, statementParameterType, nullable, nil, nil, nil, "8", "0"},
	}}}
}
