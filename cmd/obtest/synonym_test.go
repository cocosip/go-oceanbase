package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	synonymCreateFix     = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V VARCHAR2(20))"
	synonymInsertFix     = "INSERT INTO OBT_OFFLINE_T VALUES (?,?)"
	synonymViewFix       = "CREATE VIEW OBT_OFFLINE_V1 AS SELECT ID,V FROM OBT_OFFLINE_T"
	synonymViewRowsFix   = "SELECT V FROM OBT_OFFLINE_S1 WHERE ID=?"
	synonymTextRowsFix   = "SELECT V FROM OBT_OFFLINE_S2 WHERE ID>=1"
	synonymTableRowsFix  = "SELECT V FROM OBT_OFFLINE_T WHERE ID=?"
	synonymFixtureSchema = "APP"
)

type synonymFixtureCase struct {
	name     string
	failStep int
	mutate   func(body *[]fixtureStep)
}

func synonymFixtureCases() []synonymFixtureCase {
	return []synonymFixtureCase{
		{name: caseRoundTrip, failStep: -1},
		{name: "view-read-changed", failStep: 5, mutate: func(body *[]fixtureStep) {
			(*body)[5].rows = synonymRows(synonymValue + "x")
		}},
		{name: "synonym-read-changed", failStep: 6, mutate: func(body *[]fixtureStep) {
			(*body)[6].rows = synonymRows(synonymValue + "x")
		}},
		{name: "synonym-write-lost", failStep: 8, mutate: func(body *[]fixtureStep) {
			(*body)[8].rows = synonymRows(synonymValue)
		}},
		{name: "metadata-target", failStep: 9, mutate: func(body *[]fixtureStep) {
			(*body)[9].rows = synonymMetadataRows(synonymValue, nil)
		}},
		{name: "metadata-db-link", failStep: 9, mutate: func(body *[]fixtureStep) {
			(*body)[9].rows = synonymMetadataRows("", "REMOTE")
		}},
		{name: "metadata-missing", failStep: 9, mutate: func(body *[]fixtureStep) {
			(*body)[9].rows = &fixtureRows{width: 6}
		}},
	}
}

func TestOracleSynonymWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, synonymCase, oceanbase.ModeOracle)
	for _, tc := range synonymFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := synonymSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.failStep < 0 {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 4 || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle synonym workflow: %+v; want %s", result, want)
			}
			for _, object := range result.Objects {
				if object.State != fixtureDroppedState {
					t.Fatalf("oracle synonym object %s state=%s", object.Name, object.State)
				}
			}
		})
	}
}

func synonymSteps(t *testing.T, tc synonymFixtureCase) []fixtureStep {
	t.Helper()
	body := []fixtureStep{
		{query: synonymCreateFix},
		{query: synonymInsertFix, inspect: synonymInsertInspect(t, 1, synonymValue)},
		{query: synonymViewFix},
		{query: "CREATE SYNONYM OBT_OFFLINE_S1 FOR OBT_OFFLINE_V1"},
		{query: "CREATE SYNONYM OBT_OFFLINE_S2 FOR OBT_OFFLINE_T"},
		{query: synonymViewRowsFix, rows: synonymRows(synonymValue)},
		{query: synonymTextRowsFix, rows: synonymRows(synonymValue)},
		{query: "INSERT INTO OBT_OFFLINE_S2 VALUES (?,?)", inspect: synonymInsertInspect(t, 2, synonymWriteValue)},
		{query: synonymTableRowsFix, rows: synonymRows(synonymWriteValue)},
		{query: "", rows: synonymMetadataRows("", nil), inspect: synonymFilterInspect(t)},
	}
	if tc.mutate != nil {
		tc.mutate(&body)
	}
	if tc.failStep >= 0 {
		body = body[:tc.failStep+1]
	}
	return append(body, synonymDropSteps()...)
}

func synonymDropSteps() []fixtureStep {
	return []fixtureStep{
		{query: "DROP SYNONYM OBT_OFFLINE_S2"},
		{query: "DROP SYNONYM OBT_OFFLINE_S1"},
		{query: "DROP VIEW OBT_OFFLINE_V1"},
		{query: fixtureDropTableSQL},
	}
}

func synonymRows(value string) *fixtureRows {
	return &fixtureRows{width: 1, values: [][]driver.Value{{value}}}
}

func synonymMetadataRows(tableName string, dbLink any) *fixtureRows {
	if tableName == "" {
		tableName = "OBT_OFFLINE_V1"
	}
	if dbLink == nil {
		return &fixtureRows{width: 6, values: [][]driver.Value{{
			nil, synonymFixtureSchema, "OBT_OFFLINE_S1", synonymFixtureSchema, tableName, nil,
		}}}
	}
	return &fixtureRows{width: 6, values: [][]driver.Value{{
		nil, synonymFixtureSchema, "OBT_OFFLINE_S1", synonymFixtureSchema, "OBT_OFFLINE_V1", dbLink,
	}}}
}

func synonymInsertInspect(t *testing.T, id int, value string) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fixtureArgText(args[0].Value) != fixtureArgText(id) || fixtureArgText(args[1].Value) != value {
			t.Errorf("synonym insert bound unexpected arguments")
		}
	}
}

func synonymFilterInspect(t *testing.T) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 1 {
			t.Error("missing synonym metadata filter")
			return
		}
		filter := reflect.ValueOf(args[0].Value).FieldByName("filter")
		if filter.FieldByName("Kind").Uint() != uint64(oceanbase.MetadataSynonyms) || filter.FieldByName("NamePattern").String() != "OBT/_OFFLINE/_S1" {
			t.Error("synonym metadata selected a different object or dictionary")
		}
	}
}
