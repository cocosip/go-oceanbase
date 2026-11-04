package main

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"testing"

	"encoding/hex"

	"github.com/cocosip/go-oceanbase"
)

type charsetFixtureCase struct {
	name   string
	mutate func(r *charsetFixtureRows)
}

func charsetFixtureCases() []charsetFixtureCase {
	return []charsetFixtureCase{
		{name: caseRoundTrip},
		{name: "text-changed", mutate: func(r *charsetFixtureRows) { r.values[0][0] = "cafe" }},
		{name: "hex-changed", mutate: func(r *charsetFixtureRows) { r.values[0][1] = "63616683" }},
		{name: caseNullChanged, mutate: func(r *charsetFixtureRows) { r.values[1] = []driver.Value{"café", "63616682"} }},
		{name: "missing-row", mutate: func(r *charsetFixtureRows) { r.values = r.values[:2] }},
		{name: caseExtraRow, mutate: func(r *charsetFixtureRows) { r.values = append(r.values, r.values[0]) }},
	}
}

func TestMySQLCharsetWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, charsetCase, oceanbase.ModeMySQL)
	for _, tc := range charsetFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := charsetSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want, wantObjects := statusFail, 1
			if tc.mutate == nil {
				want, wantObjects = statusPass, len(charsetSeeds())
			}
			if result.Status != want || len(result.Objects) != wantObjects || len(result.CleanupErrors) != 0 {
				t.Fatalf("mysql charset workflow: %+v; want %s", result, want)
			}
			for _, object := range result.Objects {
				if object.State != fixtureDroppedState {
					t.Fatalf("mysql charset object %s state=%s", object.Name, object.State)
				}
			}
		})
	}
}

func charsetSteps(t *testing.T, tc charsetFixtureCase) []fixtureStep {
	t.Helper()
	seeds := charsetSeeds()
	if tc.mutate != nil {
		base := charsetSeedSteps(t, seeds[0])
		rows := newCharsetFixtureRows(seeds[0])
		tc.mutate(rows)
		steps := append(base[:5], fixtureStep{query: charsetSelect(seeds[0], false), rows: rows})
		return append(steps, fixtureStep{query: "DROP TABLE OBT_OFFLINE_CS_" + seeds[0].name})
	}
	steps := make([]fixtureStep, 0, len(seeds)*8+len(seeds))
	for _, seed := range seeds {
		steps = append(steps, charsetSeedSteps(t, seed)...)
	}
	return charsetCleanupSteps(steps)
}

func charsetCleanupSteps(steps []fixtureStep) []fixtureStep {
	seeds := charsetSeeds()
	for i := len(seeds) - 1; i >= 0; i-- {
		steps = append(steps, fixtureStep{query: "DROP TABLE OBT_OFFLINE_CS_" + seeds[i].name})
	}
	return steps
}

func charsetSeedSteps(t *testing.T, seed charsetSeed) []fixtureStep {
	t.Helper()
	encoded, err := hex.DecodeString(seed.raw)
	if err != nil {
		t.Fatal(err)
	}
	table := "OBT_OFFLINE_CS_" + seed.name
	return []fixtureStep{
		{query: fmt.Sprintf("CREATE TABLE %s (ID INTEGER PRIMARY KEY,V VARCHAR(200) CHARACTER SET %s COLLATE %s_bin) ENGINE=InnoDB", table, seed.name, seed.name)},
		{query: "INSERT INTO " + table + " VALUES (?,?)", inspect: func(args []driver.NamedValue) {
			if len(args) != 2 || fmt.Sprint(args[0].Value) != "1" || fixtureArgText(args[1].Value) != seed.text {
				t.Errorf("charset text insert for %s bound unexpected arguments", seed.name)
			}
		}},
		{query: "INSERT INTO " + table + " VALUES (?,?)", inspect: func(args []driver.NamedValue) {
			if len(args) != 2 || fmt.Sprint(args[0].Value) != "2" || args[1].Value != nil {
				t.Errorf("charset NULL insert for %s bound unexpected arguments", seed.name)
			}
		}},
		{query: "INSERT INTO " + table + " VALUES (3,CONVERT(CAST(? AS BINARY) USING " + seed.name + "))", inspect: func(args []driver.NamedValue) {
			got, ok := args[0].Value.([]byte)
			if len(args) != 1 || !ok || !bytes.Equal(got, encoded) {
				t.Errorf("charset byte insert for %s bound unexpected bytes", seed.name)
			}
		}},
		{query: "SET character_set_results=" + seed.name},
		{query: charsetSelect(seed, false), rows: newCharsetFixtureRows(seed)},
		{query: charsetSelect(seed, true), rows: newCharsetFixtureRows(seed)},
		{query: charsetResetDDL},
	}
}

func charsetSelect(seed charsetSeed, bound bool) string {
	table := "OBT_OFFLINE_CS_" + seed.name
	if bound {
		return "SELECT V,HEX(V) FROM " + table + " WHERE ID>=? ORDER BY ID"
	}
	return "SELECT V,HEX(V) FROM " + table + " WHERE ID>=1 ORDER BY ID"
}

type charsetFixtureRows struct {
	fixtureRows
}

func newCharsetFixtureRows(seed charsetSeed) *charsetFixtureRows {
	return &charsetFixtureRows{fixtureRows{width: 2, values: [][]driver.Value{
		{seed.text, seed.raw},
		{nil, nil},
		{seed.text, seed.raw},
	}}}
}
