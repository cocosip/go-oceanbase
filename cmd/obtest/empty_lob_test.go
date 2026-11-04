package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	emptyLOBCreateFix = "CREATE TABLE OBT_OFFLINE_T "
	emptyLOBInsertFix = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	emptyLOBSelectFix = "SELECT B,T FROM OBT_OFFLINE_T ORDER BY ID"
	emptyLOBRawOneFix = "SELECT B FROM OBT_OFFLINE_T WHERE ID=1"
	emptyLOBRawTwoFix = "SELECT B FROM OBT_OFFLINE_T WHERE ID=2"
	emptyLOBNullFix   = "SELECT CASE WHEN B IS NULL THEN 1 ELSE 0 END,CASE WHEN T IS NULL THEN 1 ELSE 0 END FROM OBT_OFFLINE_T ORDER BY ID"
)

type emptyLOBFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep, profile emptyLOBProfile)
	wantOK bool
}

func emptyLOBFixtureCases() []emptyLOBFixtureCase {
	flip := func(profile emptyLOBProfile) []driver.Value {
		if profile.boundEmpty {
			return []driver.Value{nil, nil}
		}
		return []driver.Value{[]byte{}, ""}
	}
	return []emptyLOBFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "empty-lost", failAt: 4, mutate: func(body *[]fixtureStep, _ emptyLOBProfile) {
			rows := newEmptyLOBRows(emptyLOBProfile{}, false, false)
			rows.values[0] = []driver.Value{nil, nil}
			(*body)[4].rows = rows
		}},
		{name: caseNullChanged, failAt: 4, mutate: func(body *[]fixtureStep, _ emptyLOBProfile) {
			rows := newEmptyLOBRows(emptyLOBProfile{}, false, false)
			rows.values[1] = []driver.Value{[]byte{}, ""}
			(*body)[4].rows = rows
		}},
		{name: "bound-empty", failAt: 4, mutate: func(body *[]fixtureStep, profile emptyLOBProfile) {
			rows := newEmptyLOBRows(profile, false, false)
			rows.values[2] = flip(profile)
			(*body)[4].rows = rows
		}},
		{name: "raw-content", failAt: 5, mutate: func(body *[]fixtureStep, _ emptyLOBProfile) {
			(*body)[5].values = [][]driver.Value{{"x"}}
		}},
		{name: "raw-null-changed", failAt: 6, mutate: func(body *[]fixtureStep, _ emptyLOBProfile) {
			(*body)[6].values = [][]driver.Value{{"x"}}
		}},
		{name: "null-flags", failAt: 7, mutate: func(body *[]fixtureStep, profile emptyLOBProfile) {
			rows := newEmptyLOBNullRows(profile)
			rows.values[0] = []driver.Value{int64(1), int64(1)}
			(*body)[7].rows = rows
		}},
	}
}

func TestEmptyLOBWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, emptyLOBCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		profile := emptyLOBProfileFor(mode)
		for _, tc := range emptyLOBFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := emptyLOBSteps(t, mode, profile, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
					t.Fatalf("empty LOB workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
			})
		}
	}
}

func emptyLOBSteps(t *testing.T, mode oceanbase.Mode, profile emptyLOBProfile, tc emptyLOBFixtureCase) []fixtureStep {
	t.Helper()
	ddl := emptyLOBCreateFix + profile.ddl
	if mode == oceanbase.ModeMySQL {
		ddl += parameterFixtureEngine
	}
	body := []fixtureStep{
		{query: ddl},
		{query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'','')"},
		{query: emptyLOBInsertFix, inspect: emptyLOBInsertInspect(t, 2, nil, nil)},
		{query: emptyLOBInsertFix, inspect: emptyLOBInsertInspect(t, 3, []byte{}, "")},
		{query: emptyLOBSelectFix, rows: newEmptyLOBRows(profile, true, true)},
		{query: emptyLOBRawOneFix, values: [][]driver.Value{{[]byte{}}}},
		{query: emptyLOBRawTwoFix, values: [][]driver.Value{{nil}}},
		{query: emptyLOBNullFix, rows: newEmptyLOBNullRows(profile)},
		{query: fixtureDropTableSQL},
	}
	if mode == oceanbase.ModeOracle {
		body[1].query = "INSERT INTO OBT_OFFLINE_T VALUES (1,EMPTY_BLOB(),EMPTY_CLOB())"
	}
	if tc.mutate != nil {
		tc.mutate(&body, profile)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
		body = append(body, fixtureStep{query: fixtureDropTableSQL})
	}
	return body
}

func emptyLOBInsertInspect(t *testing.T, id int, binary, text any) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 3 || fixtureArgText(args[0].Value) != fixtureArgText(id) {
			t.Errorf("empty LOB insert %d bound unexpected arguments", id)
			return
		}
		if fixtureArgText(args[1].Value) != fixtureArgText(binary) || fixtureArgText(args[2].Value) != fixtureArgText(text) {
			t.Errorf("empty LOB insert %d bound unexpected values", id)
		}
	}
}

type emptyLOBRows struct {
	fixtureRows
}

func newEmptyLOBRows(profile emptyLOBProfile, _, _ bool) *emptyLOBRows {
	boundEmpty := []driver.Value{nil, nil}
	if profile.boundEmpty {
		boundEmpty = []driver.Value{[]byte{}, ""}
	}
	return &emptyLOBRows{fixtureRows: fixtureRows{width: 2, values: [][]driver.Value{
		{[]byte{}, ""},
		{nil, nil},
		boundEmpty,
	}}}
}

type emptyLOBNullRows struct {
	fixtureRows
}

func newEmptyLOBNullRows(profile emptyLOBProfile) *emptyLOBNullRows {
	bound := int64(0)
	if !profile.boundEmpty {
		bound = 1
	}
	return &emptyLOBNullRows{fixtureRows: fixtureRows{width: 2, values: [][]driver.Value{
		{int64(0), int64(0)},
		{int64(1), int64(1)},
		{bound, bound},
	}}}
}
