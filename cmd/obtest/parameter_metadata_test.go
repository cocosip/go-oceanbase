package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	parameterMetadataCase  = "metadata-parameters"
	parameterFixtureDDL    = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V VARCHAR(100))"
	parameterFixtureRead   = "SELECT V FROM OBT_OFFLINE_T WHERE ID=1"
	parameterFixtureEngine = " ENGINE=InnoDB"
	parameterFixtureKey    = "KEY"
	parameterSideEffect    = "side-effect"
)

type parameterMetadataTest struct {
	name       string
	part       int
	edit       func(*metadataFixtureRows)
	pass       bool
	oracleOnly bool
}

func TestParameterMetadataWorkflow(t *testing.T) {
	var definition caseDefinition
	for _, d := range caseDefinitions() {
		if d.name == parameterMetadataCase {
			definition = d
		}
	}
	if definition.run == nil || definition.mysqlOnly || definition.oracleOnly {
		t.Fatal("parameter metadata acceptance scenario is missing or mode-gated")
	}
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range []parameterMetadataTest{
			{name: "reported-description", pass: true},
			{name: "unknown-description", pass: true, edit: func(r *metadataFixtureRows) { r.values[0][4], r.values[0][6], r.values[0][9] = nil, nil, int64(6) }},
			{name: "reported-number", pass: true, edit: func(r *metadataFixtureRows) {
				typeName, scale := "NUMBER", int64(-2)
				if mode == oceanbase.ModeMySQL {
					typeName, scale = "DECIMAL", 2
				}
				r.values[0][4], r.values[0][6], r.values[0][7], r.values[0][8], r.values[0][9] = typeName, nil, int64(38), scale, int64(246)
			}},
			{name: "raw-direction", pass: true, edit: func(r *metadataFixtureRows) { r.values[0][10] = int64(255) }},
			{name: "physical-order", edit: func(r *metadataFixtureRows) { r.values[0][0] = int64(2) }},
			{name: "logical-order", edit: func(r *metadataFixtureRows) { r.values[1][1] = int64(1) }},
			{name: "positional-name", edit: func(r *metadataFixtureRows) { r.values[0][2] = "V" }},
			{name: "fabricated-direction", edit: func(r *metadataFixtureRows) { r.values[0][3] = "IN" }},
			{name: "fabricated-null-type", edit: func(r *metadataFixtureRows) { r.values[0][9] = int64(6) }},
			{name: "empty-type", edit: func(r *metadataFixtureRows) { r.values[0][4] = "" }},
			{name: "nullable-state", edit: func(r *metadataFixtureRows) { r.values[0][5] = int64(2) }},
			{name: "nullability-contract", edit: func(r *metadataFixtureRows) {
				if mode == oceanbase.ModeMySQL {
					r.values[0][5] = int64(1)
				} else {
					r.values[0][5] = nil
				}
			}},
			{name: "negative-length", edit: func(r *metadataFixtureRows) { r.values[0][6] = int64(-1) }},
			{name: "negative-precision", edit: func(r *metadataFixtureRows) { r.values[0][7], r.values[0][8] = int64(-1), int64(0) }},
			{name: "partial-precision", edit: func(r *metadataFixtureRows) { r.values[0][7] = int64(10) }},
			{name: "protocol-type", edit: func(r *metadataFixtureRows) { r.values[0][9] = int64(256) }},
			{name: "protocol-direction", edit: func(r *metadataFixtureRows) { r.values[0][10] = int64(-1) }},
			{name: "missing-row", edit: func(r *metadataFixtureRows) { r.values = r.values[:1] }},
			{name: "extra-row", edit: func(r *metadataFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "column-name", edit: func(r *metadataFixtureRows) { r.labels[1] = "wrong_logical_ordinal" }},
			{name: "read-error", edit: func(r *metadataFixtureRows) { r.readErr = errors.New("metadata read failed") }},
			{name: "close-error", edit: func(r *metadataFixtureRows) { r.closeErr = errors.New("metadata close failed") }},
			{name: parameterSideEffect},
			{name: "named-logical", part: 1, oracleOnly: true, edit: func(r *metadataFixtureRows) { r.values[2][1] = int64(3) }},
			{name: "named-normalization", part: 1, oracleOnly: true, edit: func(r *metadataFixtureRows) { r.values[2][2] = "key" }},
			{name: "named-identity", part: 1, oracleOnly: true, edit: func(r *metadataFixtureRows) { r.values[0][2] = parameterFixtureKey }},
			{name: "named-null", part: 1, oracleOnly: true, edit: func(r *metadataFixtureRows) { r.values[0][2] = nil }},
			{name: "named-physical", part: 1, oracleOnly: true, edit: func(r *metadataFixtureRows) { r.values[2][0] = int64(2) }},
			{name: "parameterless-extra-row", part: -1, edit: func(r *metadataFixtureRows) {
				r.values = [][]driver.Value{{int64(1), int64(1), nil, nil, nil, nil, nil, nil, nil, int64(6), int64(0)}}
			}},
		} {
			if tc.oracleOnly && mode != oceanbase.ModeOracle {
				continue
			}
			t.Run(fmt.Sprintf("%d/%s", mode, tc.name), func(t *testing.T) {
				testParameterMetadata(t, definition, mode, tc)
			})
		}
	}
}

func testParameterMetadata(t *testing.T, definition caseDefinition, mode oceanbase.Mode, tc parameterMetadataTest) {
	t.Helper()
	queries, parts := parameterMetadataFixtures(mode)
	part := tc.part
	if part < 0 {
		part = len(parts) - 1
	}
	if tc.edit != nil {
		tc.edit(parts[part])
	}
	steps := []fixtureStep{{query: parameterFixtureDDL}, {query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'before')", affected: 1}}
	if mode == oceanbase.ModeMySQL {
		steps[0].query += parameterFixtureEngine
	}
	for i, query := range queries {
		r := parts[i]
		steps = append(steps, fixtureStep{rows: r, inspect: func(args []driver.NamedValue) {
			r.opened = true
			if len(args) != 1 {
				t.Fatal("missing statement metadata request")
			}
			f := reflect.ValueOf(args[0].Value).FieldByName("filter")
			if f.FieldByName("Kind").Uint() != uint64(oceanbase.MetadataStatementParameters) || f.FieldByName("Query").String() != query {
				t.Error("statement metadata query or kind changed")
			}
		}})
		if !tc.pass && tc.name != parameterSideEffect && i == part {
			break
		}
		value := "before"
		if tc.name == parameterSideEffect {
			value = "modified"
		}
		steps = append(steps, fixtureStep{query: parameterFixtureRead, values: [][]driver.Value{{value}}})
		if tc.name == parameterSideEffect {
			break
		}
	}
	if tc.pass {
		steps = append(steps, fixtureStep{query: "SELECT COUNT(*) FROM " + countFixtureTable, values: [][]driver.Value{{int64(1)}}})
	}
	steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
	s := fixtureScenario(t, steps)
	s.mode = mode
	result := runScenario(context.Background(), s, definition.run)
	want := statusFail
	if tc.pass {
		want = statusPass
	}
	if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState {
		t.Fatalf("statement metadata workflow: %+v", result)
	}
	for _, r := range parts {
		if r.opened && !r.closed {
			t.Error("statement metadata rows not closed")
		}
	}
}

func parameterMetadataFixtures(mode oceanbase.Mode) ([]string, []*metadataFixtureRows) {
	queries := []string{"UPDATE OBT_OFFLINE_T SET V=? WHERE ID=?"}
	identities := [][]int64{{1, 2}}
	if mode == oceanbase.ModeOracle {
		queries = append(queries, "UPDATE OBT_OFFLINE_T SET V=:text WHERE ID=:key OR ID=:KEY")
		identities = append(identities, []int64{1, 2, 2})
	}
	queries = append(queries, "SELECT ID,V FROM OBT_OFFLINE_T WHERE ID=1")
	identities = append(identities, nil)
	var parts []*metadataFixtureRows
	for i, slots := range identities {
		r := &metadataFixtureRows{fixtureRows: fixtureRows{width: 11}, labels: strings.Fields("physical_ordinal logical_ordinal name direction data_type nullable length precision scale protocol_type protocol_direction")}
		for j, slot := range slots {
			var name, nullable driver.Value
			if mode == oceanbase.ModeOracle {
				nullable = int64(1)
				if i == 1 {
					name = []string{"TEXT", parameterFixtureKey, parameterFixtureKey}[j]
				}
			}
			r.values = append(r.values, []driver.Value{int64(j + 1), slot, name, nil, routineFixtureVarchar, nullable, int64(100), nil, nil, int64(253), int64(0)})
		}
		parts = append(parts, r)
	}
	return queries, parts
}
