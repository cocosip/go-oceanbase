package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

type metadataTypeInfoFixtureCase struct {
	name   string
	failAt int
	mutate func(rows *metadataTypeInfoFixtureRows, mode oceanbase.Mode)
	wantOK bool
}

func metadataTypeInfoFixtureCases() []metadataTypeInfoFixtureCase {
	return []metadataTypeInfoFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: caseExtraRow, failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, _ oceanbase.Mode) {
			rows.values = rows.values[:len(rows.values)-1]
		}},
		{name: "duplicate-type", failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, _ oceanbase.Mode) {
			rows.values = append(rows.values, rows.values[0])
		}},
		{name: "unknown-category", failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, _ oceanbase.Mode) {
			rows.values[0][1] = "spatial"
		}},
		{name: "unnamed-type", failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, _ oceanbase.Mode) {
			rows.values[0][0] = ""
		}},
		{name: "width", failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, mode oceanbase.Mode) {
			if mode == oceanbase.ModeOracle {
				rows.values[0][2] = int64(39)
				return
			}
			rows.values[3][2] = int64(9)
		}},
		{name: "auto-flag", failAt: 0, mutate: func(rows *metadataTypeInfoFixtureRows, mode oceanbase.Mode) {
			if mode == oceanbase.ModeOracle {
				rows.values[0][3] = int64(-85)
				return
			}
			rows.values[3][9] = int64(0)
		}},
	}
}

func TestMetadataTypeInfoWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, metadataTypeInfoCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range metadataTypeInfoFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				rows := newMetadataTypeInfoRows(mode)
				if tc.mutate != nil {
					tc.mutate(rows, mode)
				}
				body := []fixtureStep{{query: "", rows: rows}}
				if tc.failAt >= 0 {
					body = body[:tc.failAt+1]
				}
				s := fixtureScenario(t, body)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 0 || len(result.CleanupErrors) != 0 {
					t.Fatalf("metadata type info workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
			})
		}
	}
}

type metadataTypeInfoFixtureRows struct {
	fixtureRows
}

func newMetadataTypeInfoRows(mode oceanbase.Mode) *metadataTypeInfoFixtureRows {
	if mode == oceanbase.ModeOracle {
		return &metadataTypeInfoFixtureRows{fixtureRows: fixtureRows{width: 10, values: oracleTypeInfoValues()}}
	}
	return &metadataTypeInfoFixtureRows{fixtureRows: fixtureRows{width: 10, values: mysqlTypeInfoValues()}}
}

func mysqlTypeInfoValues() [][]driver.Value {
	values := make([][]driver.Value, 0, typeInfoMySQLCount)
	for _, integer := range []struct {
		name      string
		precision int64
	}{{"TINYINT", 3}, {"SMALLINT", 5}, {"MEDIUMINT", 7}, {"INT", 10}, {"BIGINT", 19}} {
		values = append(values, []driver.Value{integer.name, typeInfoNumeric, integer.precision, int64(0), int64(0), int64(10), nil, nil, nil, int64(1)})
	}
	values = append(values,
		[]driver.Value{"DECIMAL", typeInfoNumeric, int64(65), int64(0), int64(30), int64(10), nil, nil, "precision,scale", int64(0)},
		[]driver.Value{"FLOAT", typeInfoNumeric, int64(24), nil, nil, int64(2), nil, nil, nil, int64(0)},
		[]driver.Value{"DOUBLE", typeInfoNumeric, int64(53), nil, nil, int64(2), nil, nil, nil, int64(0)},
		[]driver.Value{typeInfoBit, typeInfoBinary, int64(64), nil, nil, int64(2), "b'", "'", typeInfoLength, int64(0)},
	)
	for _, text := range []struct {
		name   string
		params driver.Value
	}{{"CHAR", typeInfoLength}, {"VARCHAR", typeInfoLength}, {"TINYTEXT", nil}, {"TEXT", nil}, {"MEDIUMTEXT", nil},
		{"LONGTEXT", nil}, {"ENUM", "values"}, {"SET", "values"}, {"JSON", nil}} {
		values = append(values, []driver.Value{text.name, typeInfoCharacter, nil, nil, nil, nil, "'", "'", text.params, int64(0)})
	}
	for _, binary := range []struct {
		name   string
		params driver.Value
	}{{"BINARY", typeInfoLength}, {"VARBINARY", typeInfoLength}, {"TINYBLOB", nil}, {blobType, nil}, {"MEDIUMBLOB", nil}, {"LONGBLOB", nil}} {
		values = append(values, []driver.Value{binary.name, typeInfoBinary, nil, nil, nil, nil, "X'", "'", binary.params, int64(0)})
	}
	for _, temporal := range []struct {
		name   string
		low    driver.Value
		high   driver.Value
		params driver.Value
	}{{"DATE", nil, nil, nil}, {"TIME", int64(0), int64(6), typeInfoFractional},
		{"DATETIME", int64(0), int64(6), typeInfoFractional}, {"TIMESTAMP", int64(0), int64(6), typeInfoFractional},
		{"YEAR", nil, nil, nil}} {
		values = append(values, []driver.Value{temporal.name, typeInfoDatetime, nil, temporal.low, temporal.high, nil, "'", "'", temporal.params, int64(0)})
	}
	return values
}

func oracleTypeInfoValues() [][]driver.Value {
	return [][]driver.Value{
		{"NUMBER", typeInfoNumeric, int64(38), int64(-84), int64(127), int64(10), nil, nil, "precision,scale", nil},
		{"FLOAT", typeInfoNumeric, int64(126), nil, nil, int64(2), nil, nil, "binary_precision", nil},
		{"BINARY_FLOAT", typeInfoNumeric, int64(24), nil, nil, int64(2), nil, nil, nil, nil},
		{"BINARY_DOUBLE", typeInfoNumeric, int64(53), nil, nil, int64(2), nil, nil, nil, nil},
		{"CHAR", typeInfoCharacter, nil, nil, nil, nil, "'", "'", typeInfoLength, nil},
		{typeNameVarchar2, typeInfoCharacter, nil, nil, nil, nil, "'", "'", typeInfoLength, nil},
		{"NCHAR", typeInfoCharacter, nil, nil, nil, nil, "'", "'", typeInfoLength, nil},
		{"NVARCHAR2", typeInfoCharacter, nil, nil, nil, nil, "'", "'", typeInfoLength, nil},
		{"RAW", typeInfoBinary, nil, nil, nil, nil, "'", "'", typeInfoLength, nil},
		{"ROWID", typeInfoRowID, nil, nil, nil, nil, "'", "'", nil, nil},
		{"INTERVAL YEAR TO MONTH", typeInfoInterval, int64(9), nil, nil, int64(10), "INTERVAL '", "' YEAR TO MONTH", "leading_precision", nil},
		{"INTERVAL DAY TO SECOND", typeInfoInterval, int64(9), int64(0), int64(9), int64(10), "INTERVAL '", "' DAY TO SECOND", "leading_precision,fractional_seconds_precision", nil},
		{"DATE", typeInfoDatetime, nil, nil, nil, nil, "DATE '", "'", nil, nil},
		{typeInfoTimestamp, typeInfoDatetime, nil, int64(0), int64(9), nil, typeInfoTimestampPrefix, "'", typeInfoFractional, nil},
		{"TIMESTAMP WITH TIME ZONE", typeInfoDatetime, nil, int64(0), int64(9), nil, typeInfoTimestampPrefix, "'", typeInfoFractional, nil},
		{"TIMESTAMP WITH LOCAL TIME ZONE", typeInfoDatetime, nil, int64(0), int64(9), nil, typeInfoTimestampPrefix, "'", typeInfoFractional, nil},
		{"BLOB", typeInfoLOB, nil, nil, nil, nil, nil, nil, nil, nil},
		{"CLOB", typeInfoLOB, nil, nil, nil, nil, nil, nil, nil, nil},
		{"NCLOB", typeInfoLOB, nil, nil, nil, nil, nil, nil, nil, nil},
	}
}
