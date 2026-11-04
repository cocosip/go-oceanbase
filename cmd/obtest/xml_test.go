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
	xmlCase       = "oracle-xml"
	xmlCreateTest = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,X XMLTYPE)"
	xmlInsertOne  = "INSERT INTO OBT_OFFLINE_T VALUES (1,XMLTYPE('<x>患者😀</x>'))"
	xmlInsertNull = "INSERT INTO OBT_OFFLINE_T VALUES (?,?)"
	xmlTextRead   = "SELECT X FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	xmlBoundRead  = "SELECT X FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
)

type xmlCaseDef struct {
	name     string
	database string
	scan     reflect.Type
	values   []driver.Value
	wantOK   bool
}

func xmlCases() []xmlCaseDef {
	return []xmlCaseDef{
		{name: caseRoundTrip, database: xmlTypeName, scan: reflect.TypeFor[string](), wantOK: true},
		{name: "missing-serialization-newline", database: xmlTypeName, scan: reflect.TypeFor[string](), values: []driver.Value{xmlText, nil}},
		{name: "database-type", database: caseVarchar2, scan: reflect.TypeFor[string](), values: []driver.Value{xmlStored, nil}},
		{name: "scan-type", database: xmlTypeName, scan: reflect.TypeFor[[]byte](), values: []driver.Value{xmlStored, nil}},
		{name: caseNullChanged, database: xmlTypeName, scan: reflect.TypeFor[string](), values: []driver.Value{xmlStored, xmlStored}},
		{name: caseMissingRow, database: xmlTypeName, scan: reflect.TypeFor[string](), values: []driver.Value{xmlStored}},
		{name: caseExtraRow, database: xmlTypeName, scan: reflect.TypeFor[string](), values: []driver.Value{xmlStored, nil, xmlStored}},
	}
}

func TestOracleXMLWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, xmlCase, oceanbase.ModeOracle)
	for _, tc := range xmlCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := []fixtureStep{
				{query: xmlCreateTest},
				{query: xmlInsertOne},
				{query: xmlInsertNull, inspect: func(args []driver.NamedValue) {
					if len(args) != 2 || fmt.Sprint(args[0].Value) != "2" || args[1].Value != nil {
						t.Errorf("XML NULL insert bound unexpected arguments")
					}
				}},
				{query: xmlTextRead, rows: xmlFixtureRows(tc)},
			}
			if tc.wantOK {
				steps = append(steps,
					fixtureStep{query: xmlBoundRead, rows: xmlFixtureRows(tc)},
					fixtureStep{query: fixtureDropTableSQL},
				)
			} else {
				steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
			}
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle xml workflow: %+v; want %s", result, want)
			}
		})
	}
}

func xmlFixtureRows(tc xmlCaseDef) *xmlRows {
	values := tc.values
	if values == nil {
		values = []driver.Value{xmlStored, nil}
	}
	rows := make([][]driver.Value, len(values))
	for i, value := range values {
		rows[i] = []driver.Value{value}
	}
	return &xmlRows{fixtureRows: fixtureRows{width: 1, values: rows}, database: tc.database, scan: tc.scan}
}

type xmlRows struct {
	fixtureRows
	database string
	scan     reflect.Type
}

func (r *xmlRows) ColumnTypeDatabaseTypeName(int) string { return r.database }
func (r *xmlRows) ColumnTypeScanType(int) reflect.Type   { return r.scan }
