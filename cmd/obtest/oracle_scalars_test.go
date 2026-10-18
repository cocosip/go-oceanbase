package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

const oracleScalarCase = "oracle-scalars"
const oracleScalarTZ = "2026-01-02 03:04:05.123456789 -00:30"
const oracleScalarLTZ = "2026-01-02 03:04:05.123456789 +00:00"
const oracleScalarYM = "-999999999-11"
const oracleScalarDS = "-999999999 23:59:59.123456789"
const oracleScalarRowID = "AAAAAAABBAAAAAcAAA"

func TestOracleScalarsWorkflow(t *testing.T) {
	var definition caseDefinition
	for _, d := range caseDefinitions() {
		if d.name == oracleScalarCase {
			definition = d
		}
	}
	if definition.run == nil || definition.configure == nil || !definition.oracleOnly || definition.mysqlOnly {
		t.Fatal("Oracle scalar acceptance scenario is missing or not mode-gated")
	}
	for _, parameterized := range []bool{false, true} {
		for _, tc := range []struct {
			name       string
			column     int
			value      driver.Value
			nullColumn int
			lookup     int64
			wantOK     bool
		}{
			{name: "complete", wantOK: true},
			{name: "equivalent-ltz-offset", column: 2, value: "2026-01-02 08:34:05.123456789 +05:30", wantOK: true},
			{name: "national-text-changed", column: 0, value: "wrong"},
			{name: "tz-offset-lost", column: 1, value: "2026-01-02 03:34:05.123456789 +00:00"},
			{name: "tz-precision-lost", column: 1, value: "2026-01-02 03:04:05.123456 -00:30"},
			{name: "tz-invalid", column: 1, value: "invalid"},
			{name: "ltz-instant-changed", column: 2, value: "2026-01-02 03:04:05.123456789 +00:30"},
			{name: "year-month-changed", column: 3, value: "-999999998-11"},
			{name: "day-second-changed", column: 4, value: "-999999999 23:59:59.123456788"},
			{name: "rowid-invalid", column: 5, value: "bad rowid"},
			{name: "rowid-wrong-row", lookup: 2},
			{name: "national-null-lost", nullColumn: 1},
			{name: "tz-null-lost", nullColumn: 2},
			{name: "ltz-null-lost", nullColumn: 3},
			{name: "year-month-null-lost", nullColumn: 4},
			{name: "day-second-null-lost", nullColumn: 5},
		} {
			t.Run(map[bool]string{false: "text", true: "server-prepared"}[parameterized]+"/"+tc.name, func(t *testing.T) {
				steps := oracleScalarSeed(t)
				for _, queryParameterized := range []bool{false, true} {
					selected := queryParameterized == parameterized
					values := []driver.Value{unicodeValue, oracleScalarTZ, oracleScalarLTZ, oracleScalarYM, oracleScalarDS, oracleScalarRowID}
					if selected && tc.value != nil {
						values[tc.column] = tc.value
					}
					query := "SELECT NV,TZ,LTZ,YM,DS,ROWID FROM OBT_OFFLINE_T WHERE ID=1"
					if queryParameterized {
						query = "SELECT NV,TZ,LTZ,YM,DS,ROWID FROM OBT_OFFLINE_T WHERE ID=?"
					}
					steps = append(steps, fixtureStep{query: query, values: [][]driver.Value{values}})
					if selected && tc.value != nil && !tc.wantOK {
						break
					}
					id := int64(1)
					if selected && tc.lookup != 0 {
						id = tc.lookup
					}
					steps = append(steps, fixtureStep{query: "SELECT ID FROM OBT_OFFLINE_T WHERE ROWID=?", values: [][]driver.Value{{id}}, inspect: func(args []driver.NamedValue) {
						if len(args) != 1 || args[0].Value != oceanbase.RowID(oracleScalarRowID) {
							t.Fatal("ROWID not rebound as a typed value")
						}
					}})
					if selected && tc.lookup != 0 {
						break
					}
					query = "SELECT NV,TZ,LTZ,YM,DS FROM OBT_OFFLINE_T WHERE ID=2"
					if queryParameterized {
						query = "SELECT NV,TZ,LTZ,YM,DS FROM OBT_OFFLINE_T WHERE ID=?"
					}
					nulls := make([]driver.Value, 5)
					if selected && tc.nullColumn != 0 {
						nulls[tc.nullColumn-1] = values[tc.nullColumn-1]
					}
					steps = append(steps, fixtureStep{query: query, values: [][]driver.Value{nulls}})
					if selected && tc.nullColumn != 0 {
						break
					}
				}
				steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
				checkOracleScalarWorkflow(t, definition, steps, tc.wantOK)
			})
		}
	}
}

func oracleScalarSeed(t *testing.T) []fixtureStep {
	t.Helper()
	return []fixtureStep{
		{query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,NV NVARCHAR2(100),TZ TIMESTAMP(9) WITH TIME ZONE,LTZ TIMESTAMP(9) WITH LOCAL TIME ZONE,YM INTERVAL YEAR(9) TO MONTH,DS INTERVAL DAY(9) TO SECOND(9))"},
		{query: "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?,?,?,?)", affected: 1, inspect: func(args []driver.NamedValue) {
			var got []any
			for _, a := range args {
				got = append(got, a.Value)
			}
			want := []any{1, oceanbase.NString(unicodeValue), oceanbase.TimestampTZ(oracleScalarTZ), oceanbase.TimestampLTZ(oracleScalarLTZ), oceanbase.IntervalYM(oracleScalarYM), oceanbase.IntervalDS(oracleScalarDS)}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Oracle typed input changed: %#v", got)
			}
		}},
		{query: "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?,?,?,?)", affected: 1, inspect: func(args []driver.NamedValue) {
			if len(args) != 6 || args[0].Value != 2 {
				t.Fatal("NULL seed missing")
			}
			for _, a := range args[1:] {
				if a.Value != nil {
					t.Fatal("NULL seed is non-NULL")
				}
			}
		}},
	}
}

func checkOracleScalarWorkflow(t *testing.T, definition caseDefinition, steps []fixtureStep, valid bool) {
	t.Helper()
	s := fixtureScenario(t, steps)
	s.cfg.Session.Location = time.FixedZone("fixture", 19800)
	s.cfg.Session.ParseTime = true
	definition.configure(&s.cfg)
	if s.cfg.PrepareMode != oceanbase.PrepareServer || s.cfg.Session.NCharset != "utf16" || s.cfg.Session.ParseTime || s.cfg.Session.Location.String() != "fixture" {
		t.Fatal("Oracle scalar scenario configuration changed")
	}
	result := runScenario(context.Background(), s, definition.run)
	want := statusFail
	if valid {
		want = statusPass
	}
	if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
		t.Fatalf("Oracle scalar workflow: %+v; want %s and cleanup", result, want)
	}
}
