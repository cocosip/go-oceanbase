package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataPatternCreatePrefix = "CREATE TABLE "
	metadataPatternFixtureQuery = ""
)

type metadataPatternFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep)
	wantOK bool
}

func metadataPatternFixtureCases() []metadataPatternFixtureCase {
	return []metadataPatternFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "exact-matched-wildcard", failAt: 3, mutate: func(body *[]fixtureStep) {
			(*body)[3].rows = newMetadataPatternRows("OBT_OFFLINE_WILDA1")
		}},
		{name: "exact-missing", failAt: 3, mutate: func(body *[]fixtureStep) {
			(*body)[3].rows = newMetadataPatternRows()
		}},
		{name: "wildcard-tight", failAt: 4, mutate: func(body *[]fixtureStep) {
			(*body)[4].rows = newMetadataPatternRows("OBT_OFFLINE_WILD_1")
		}},
		{name: "escaped-wildcard-name", failAt: 5, mutate: func(body *[]fixtureStep) {
			(*body)[5].rows = newMetadataPatternRows("OBT_OFFLINE_WILD_1")
		}},
		{name: "percent-name", failAt: 6, mutate: func(body *[]fixtureStep) {
			(*body)[6].rows = newMetadataPatternRows("OBT_OFFLINE_WILD_1")
		}},
		{name: "percent-wildcard", failAt: 7, mutate: func(body *[]fixtureStep) {
			(*body)[7].rows = newMetadataPatternRows("OBT_OFFLINE_WILDA1")
		}},
	}
}

func TestMetadataPatternWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, metadataPatternCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range metadataPatternFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := metadataPatternSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 3 || len(result.CleanupErrors) != 0 {
					t.Fatalf("metadata pattern workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
				for _, object := range result.Objects {
					if object.State != fixtureDroppedState {
						t.Fatalf("metadata pattern object %s state=%s", object.Name, object.State)
					}
				}
			})
		}
	}
}

func metadataPatternSteps(t *testing.T, mode oceanbase.Mode, tc metadataPatternFixtureCase) []fixtureStep {
	t.Helper()
	engine := ""
	if mode == oceanbase.ModeMySQL {
		engine = parameterFixtureEngine
	}
	body := []fixtureStep{
		{query: metadataPatternCreatePrefix + "OBT_OFFLINE_WILD_1 (ID INTEGER PRIMARY KEY)" + engine},
		{query: metadataPatternCreatePrefix + "OBT_OFFLINE_WILDA1 (ID INTEGER PRIMARY KEY)" + engine},
		{query: metadataPatternCreatePrefix + "OBT_OFFLINE_PCTX1 (ID INTEGER PRIMARY KEY)" + engine},
		{query: metadataPatternFixtureQuery, rows: newMetadataPatternRows("OBT_OFFLINE_WILD_1")},
		{query: metadataPatternFixtureQuery, rows: newMetadataPatternRows("OBT_OFFLINE_WILD_1", "OBT_OFFLINE_WILDA1")},
		{query: metadataPatternFixtureQuery, rows: newMetadataPatternRows("OBT_OFFLINE_WILDA1")},
		{query: metadataPatternFixtureQuery, rows: newMetadataPatternRows("OBT_OFFLINE_PCTX1")},
		{query: metadataPatternFixtureQuery, rows: newMetadataPatternRows("OBT_OFFLINE_PCTX1")},
	}
	if tc.mutate != nil {
		tc.mutate(&body)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return append(body,
		fixtureStep{query: "DROP TABLE OBT_OFFLINE_PCTX1"},
		fixtureStep{query: "DROP TABLE OBT_OFFLINE_WILDA1"},
		fixtureStep{query: "DROP TABLE OBT_OFFLINE_WILD_1"},
	)
}

type metadataPatternRows struct {
	fixtureRows
}

func newMetadataPatternRows(names ...string) *metadataPatternRows {
	rows := &metadataPatternRows{fixtureRows: fixtureRows{width: 5}}
	for _, name := range names {
		rows.values = append(rows.values, []driver.Value{nil, "S", name, "TABLE", nil})
	}
	return rows
}
