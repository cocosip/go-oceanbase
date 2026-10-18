package main

import (
	"context"
	"database/sql/driver"
	"math"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const mysqlScalarCase = "mysql-scalars"
const mysqlScalarSelect = "SELECT TM,Y,U,S,T,B1,B9,VB,BF FROM OBT_OFFLINE_T WHERE ID"
const mysqlFixtureZeroTime = "00:00:00"

func TestMySQLScalarsWorkflow(t *testing.T) {
	var definition caseDefinition
	for _, d := range caseDefinitions() {
		if d.name == mysqlScalarCase {
			definition = d
		}
	}
	if definition.run == nil || definition.configure == nil || !definition.mysqlOnly || definition.oracleOnly {
		t.Fatal("MySQL scalar acceptance scenario is missing or not mode-gated")
	}
	for _, parameterized := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(*mysqlScalarFixtureRows)
			wantOK bool
		}{
			{name: "all-boundaries", wantOK: true},
			{name: "maximum-time-fraction", wantOK: true, change: func(r *mysqlScalarFixtureRows) { r.values[1][0] = "838:59:59.000000" }},
			{name: "maximum-time-overflow", change: func(r *mysqlScalarFixtureRows) { r.values[1][0] = "838:59:59.000001" }},
			{name: "zero-time-fraction", wantOK: true, change: func(r *mysqlScalarFixtureRows) { r.values[2][0] = "00:00:00.000000" }},
			{name: "unsigned-precision", change: func(r *mysqlScalarFixtureRows) { r.values[0][2] = "18446744073709551614" }},
			{name: "unsigned-overflow", change: func(r *mysqlScalarFixtureRows) { r.values[0][2] = "18446744073709551616" }},
			{name: "signed-minimum", change: func(r *mysqlScalarFixtureRows) { r.values[0][3] = int64(math.MinInt64 + 1) }},
			{name: "tinyint-is-not-bool", change: func(r *mysqlScalarFixtureRows) { r.values[0][4] = int64(1) }},
			{name: "time-extra-suffix", change: func(r *mysqlScalarFixtureRows) { r.values[1][0] = "838:59:59.999999junk" }},
			{name: "year-zero-is-not-null", change: func(r *mysqlScalarFixtureRows) { r.values[1][1] = nil }},
			{name: "bit-one", change: func(r *mysqlScalarFixtureRows) { r.values[0][5] = []byte{0} }},
			{name: "bit-nine", change: func(r *mysqlScalarFixtureRows) { r.values[0][6] = []byte{1} }},
			{name: "empty-is-not-null", change: func(r *mysqlScalarFixtureRows) { r.values[1][7] = nil }},
			{name: "fixed-padding", change: func(r *mysqlScalarFixtureRows) { r.values[0][8] = []byte{0, 255} }},
			{name: "missing-null-row", change: func(r *mysqlScalarFixtureRows) { r.values = r.values[:3] }},
			{name: "extra-row", change: func(r *mysqlScalarFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "null-unsigned", change: func(r *mysqlScalarFixtureRows) { r.values[3][2] = "0" }},
			{name: "null-time", change: func(r *mysqlScalarFixtureRows) { r.values[3][0] = mysqlFixtureZeroTime }},
			{name: "unsigned-metadata", change: func(r *mysqlScalarFixtureRows) { r.types[2] = reflect.TypeFor[int64]() }},
			{name: "binary-metadata", change: func(r *mysqlScalarFixtureRows) { r.types[7] = reflect.TypeFor[string]() }},
			{name: "fixed-length", change: func(r *mysqlScalarFixtureRows) { r.fixedLength = 3 }},
			{name: "variable-type", change: func(r *mysqlScalarFixtureRows) { r.names[7] = "VARCHAR" }},
		} {
			t.Run(map[bool]string{false: "text-protocol", true: "server-prepared"}[parameterized]+"/"+tc.name, func(t *testing.T) {
				steps := mysqlScalarSeed(t)
				var opened []*mysqlScalarFixtureRows
				for _, queryParameterized := range []bool{false, true} {
					r := newMySQLScalarFixtureRows()
					selected := queryParameterized == parameterized
					if selected && tc.change != nil {
						tc.change(r)
					}
					query := mysqlScalarSelect + ">=1 ORDER BY ID"
					if queryParameterized {
						query = mysqlScalarSelect + ">=? ORDER BY ID"
					}
					steps = append(steps, fixtureStep{query: query, rows: r, inspect: func(args []driver.NamedValue) {
						if !queryParameterized && len(args) != 0 || queryParameterized && (len(args) != 1 || args[0].Value != 1) {
							t.Error("scalar read did not bind the expected row range")
						}
					}})
					opened = append(opened, r)
					if selected && !tc.wantOK {
						break
					}
				}
				steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
				s := fixtureScenario(t, steps)
				s.mode = oceanbase.ModeMySQL
				s.cfg.Session.ParseTime = true
				definition.configure(&s.cfg)
				if s.cfg.PrepareMode != oceanbase.PrepareServer || s.cfg.Session.ParseTime {
					t.Fatal("scalar scenario did not select binary prepare and text time values")
				}
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
					t.Fatalf("MySQL scalar workflow: %+v; want %s and cleanup", result, want)
				}
				for _, r := range opened {
					if !r.closed {
						t.Error("scalar rows were not closed before cleanup")
					}
				}
			})
		}
	}
}

func mysqlScalarSeed(t *testing.T) []fixtureStep {
	t.Helper()
	steps := []fixtureStep{{query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,TM TIME(6),Y YEAR,U BIGINT UNSIGNED,S BIGINT,T TINYINT(1),B1 BIT(1),B9 BIT(9),VB VARBINARY(100),BF BINARY(4)) ENGINE=InnoDB"}}
	for _, want := range [][]any{
		{1, "-838:59:58.999999", int64(2026), uint64(math.MaxUint64), int64(math.MinInt64), int64(2), int64(1), int64(257), []byte{0, 255}, []byte{0, 255}},
		{2, "838:59:59", int64(0), uint64(0), int64(math.MaxInt64), int64(-128), int64(0), int64(0), []byte{}, []byte{}},
		{3, mysqlFixtureZeroTime, int64(2155), uint64(1 << 63), int64(-1), int64(127), int64(1), int64(511), []byte{0, 0}, []byte{0, 1, 2, 3}},
		{4, nil, nil, nil, nil, nil, nil, nil, nil, nil},
	} {
		steps = append(steps, fixtureStep{query: "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?,?,?,?,?,?,?,?)", affected: 1, inspect: func(args []driver.NamedValue) {
			got := make([]any, len(args))
			for i, a := range args {
				got[i] = a.Value
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("MySQL seed input changed: %#v; want %#v", got, want)
			}
		}})
	}
	return steps
}

type mysqlScalarFixtureRows struct {
	fixtureRows
	names       []string
	types       []reflect.Type
	fixedLength int64
	closed      bool
}

func newMySQLScalarFixtureRows() *mysqlScalarFixtureRows {
	return &mysqlScalarFixtureRows{
		fixtureRows: fixtureRows{width: 9, values: [][]driver.Value{
			{"-838:59:58.999999", int64(2026), "18446744073709551615", int64(math.MinInt64), int64(2), []byte{1}, []byte{1, 1}, []byte{0, 255}, []byte{0, 255, 0, 0}},
			{"838:59:59", int64(0), int64(0), int64(math.MaxInt64), int64(-128), []byte{0}, []byte{0, 0}, []byte{}, []byte{0, 0, 0, 0}},
			{mysqlFixtureZeroTime, int64(2155), "9223372036854775808", int64(-1), int64(127), []byte{1}, []byte{1, 255}, []byte{0, 0}, []byte{0, 1, 2, 3}},
			{nil, nil, nil, nil, nil, nil, nil, nil, nil},
		}},
		names:       []string{"TIME", "YEAR", "BIGINT", "BIGINT", "TINYINT", "BIT", "BIT", "VARBINARY", "BINARY"},
		types:       []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[int64](), reflect.TypeFor[uint64](), reflect.TypeFor[int64](), reflect.TypeFor[int64](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte]()},
		fixedLength: 4,
	}
}

func (r *mysqlScalarFixtureRows) Close() error                            { r.closed = true; return nil }
func (r *mysqlScalarFixtureRows) ColumnTypeDatabaseTypeName(i int) string { return r.names[i] }
func (r *mysqlScalarFixtureRows) ColumnTypeScanType(i int) reflect.Type   { return r.types[i] }
func (r *mysqlScalarFixtureRows) ColumnTypeLength(i int) (int64, bool) {
	if i == 7 {
		return 100, true
	}
	return r.fixedLength, i == 8
}
