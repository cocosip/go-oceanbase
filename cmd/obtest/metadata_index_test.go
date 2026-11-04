package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataIndexCreateFix  = "CREATE TABLE OBT_OFFLINE_IDX (ID INTEGER PRIMARY KEY,V VARCHAR(20),W INTEGER)"
	metadataIndexCreateOra  = "CREATE TABLE OBT_OFFLINE_IDX (ID INTEGER PRIMARY KEY,V VARCHAR2(20),W INTEGER)"
	metadataIndexUniqueFix  = "CREATE UNIQUE INDEX OBT_OFFLINE_UX_IDX ON OBT_OFFLINE_IDX (W)"
	metadataIndexPlainFix   = "CREATE INDEX OBT_OFFLINE_IX_IDX ON OBT_OFFLINE_IDX (V,W)"
	metadataIndexDropIX     = "DROP INDEX OBT_OFFLINE_IX_IDX"
	metadataIndexDropUX     = "DROP INDEX OBT_OFFLINE_UX_IDX"
	metadataIndexServerMode = "MYSQL"
	indexTypeBTREE          = "BTREE"
	indexFixtureTable       = "OBT_OFFLINE_IDX"
)

type metadataIndexFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep, mode oceanbase.Mode)
	wantOK bool
}

func metadataIndexFixtureCases() []metadataIndexFixtureCase {
	return []metadataIndexFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "unique-flagged", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataIndexRows(mode)
			rows.values[0][4] = int64(1)
			(*body)[3].rows = rows
		}},
		{name: "unique-column", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataIndexRows(mode)
			rows.values[0][5] = "V"
			(*body)[3].rows = rows
		}},
		{name: "composite-order", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataIndexRows(mode)
			rows.values[1][5] = "W"
			rows.values[2][5] = "V"
			(*body)[3].rows = rows
		}},
		{name: "composite-missing", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataIndexRows(mode)
			rows.values = rows.values[:2]
			(*body)[3].rows = rows
		}},
		{name: "no-primary", failAt: 3, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataIndexRows(mode)
			rows.values = rows.values[:3]
			(*body)[3].rows = rows
		}},
		{name: "server-mode", failAt: 4, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataServerInfoRows(mode)
			rows.values[0][1] = "BOGUS"
			(*body)[4].rows = rows
		}},
		{name: "server-version", failAt: 4, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataServerInfoRows(mode)
			rows.values[0][0] = "MariaDB 10.5"
			(*body)[4].rows = rows
		}},
		{name: "server-extra-row", failAt: 4, mutate: func(body *[]fixtureStep, mode oceanbase.Mode) {
			rows := newMetadataServerInfoRows(mode)
			rows.values = append(rows.values, rows.values[0])
			(*body)[4].rows = rows
		}},
	}
}

func TestMetadataIndexWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, metadataIndexCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range metadataIndexFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := metadataIndexSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 3 || len(result.CleanupErrors) != 0 {
					t.Fatalf("metadata index workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
				for _, object := range result.Objects {
					if object.State != fixtureDroppedState {
						t.Fatalf("metadata index object %s state=%s", object.Name, object.State)
					}
				}
			})
		}
	}
}

func metadataIndexSteps(t *testing.T, mode oceanbase.Mode, tc metadataIndexFixtureCase) []fixtureStep {
	t.Helper()
	create := metadataIndexCreateFix
	if mode == oceanbase.ModeOracle {
		create = metadataIndexCreateOra
	} else {
		create += parameterFixtureEngine
	}
	body := []fixtureStep{
		{query: create},
		{query: metadataIndexUniqueFix},
		{query: metadataIndexPlainFix},
		{query: "", rows: newMetadataIndexRows(mode)},
		{query: "", rows: newMetadataServerInfoRows(mode)},
	}
	if tc.mutate != nil {
		tc.mutate(&body, mode)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	drops := []fixtureStep{{query: metadataIndexDropIX + " ON OBT_OFFLINE_IDX"}, {query: metadataIndexDropUX + " ON OBT_OFFLINE_IDX"}}
	if mode == oceanbase.ModeOracle {
		drops = []fixtureStep{{query: metadataIndexDropIX}, {query: metadataIndexDropUX}}
	}
	return append(append(body, drops...), fixtureStep{query: "DROP TABLE OBT_OFFLINE_IDX"})
}

type metadataIndexFixtureRows struct {
	fixtureRows
}

func newMetadataIndexRows(mode oceanbase.Mode) *metadataIndexFixtureRows {
	primary := "PRIMARY"
	if mode == oceanbase.ModeOracle {
		primary = "OBT_OFFLINE_IDX_PK"
	}
	return &metadataIndexFixtureRows{fixtureRows: fixtureRows{width: 10, values: [][]driver.Value{
		{nil, "S", indexFixtureTable, "OBT_OFFLINE_UX_IDX", int64(0), "W", int64(1), "A", int64(0), indexTypeBTREE},
		{nil, "S", indexFixtureTable, "OBT_OFFLINE_IX_IDX", int64(1), "V", int64(1), "A", int64(0), indexTypeBTREE},
		{nil, "S", indexFixtureTable, "OBT_OFFLINE_IX_IDX", int64(1), "W", int64(2), "A", int64(0), indexTypeBTREE},
		{nil, "S", indexFixtureTable, primary, int64(0), "ID", int64(1), "A", int64(0), indexTypeBTREE},
	}}}
}

type metadataServerInfoRows struct {
	fixtureRows
}

func newMetadataServerInfoRows(mode oceanbase.Mode) *metadataServerInfoRows {
	reported := metadataIndexServerMode
	flag := int64(0)
	if mode == oceanbase.ModeOracle {
		reported = "ORACLE"
		flag = 1
	}
	return &metadataServerInfoRows{fixtureRows: fixtureRows{width: 4, values: [][]driver.Value{
		{"OceanBase 4.4.2.2", reported, "1", flag},
	}}}
}
