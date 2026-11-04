package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

type utf16leFixtureCase struct {
	name   string
	mutate func(r *utf16leRows)
	wantOK bool
}

func utf16leFixtureCases() []utf16leFixtureCase {
	return []utf16leFixtureCase{
		{name: caseRoundTrip, wantOK: true},
		{name: "text-changed", mutate: func(r *utf16leRows) { r.values[0][0] = "x" }},
		{name: "bytes-changed", mutate: func(r *utf16leRows) { r.values[0][1] = "2D4E3DD800DF" }},
		{name: "scan-type-changed", mutate: func(r *utf16leRows) { r.scan = reflect.TypeFor[[]byte]() }},
		{name: "database-type-changed", mutate: func(r *utf16leRows) { r.database = blobType }},
		{name: caseMissingRow, mutate: func(r *utf16leRows) { r.values = nil }},
		{name: caseExtraRow, mutate: func(r *utf16leRows) { r.values = append(r.values, r.values[0]) }},
	}
}

func TestMySQLUTF16LEWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, utf16leCase, oceanbase.ModeMySQL)
	for _, tc := range utf16leFixtureCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := utf16leSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 0 || len(result.CleanupErrors) != 0 {
				t.Fatalf("mysql utf16le workflow: %+v; want %s", result, want)
			}
		})
	}
}

func utf16leSteps(t *testing.T, tc utf16leFixtureCase) []fixtureStep {
	t.Helper()
	steps := make([]fixtureStep, 0, len(utf16leSeeds())+1)
	for _, seed := range utf16leSeeds() {
		steps = append(steps, fixtureStep{
			query:  utf16leBoundSQL,
			values: [][]driver.Value{{seed.text, seed.hex}},
			inspect: func(args []driver.NamedValue) {
				if len(args) != 2 || args[0].Value != seed.text || args[1].Value != seed.text {
					t.Errorf("UTF-16LE conversion bound unexpected arguments")
				}
			},
		})
	}
	rows := newUTF16LERows()
	if tc.mutate != nil {
		tc.mutate(rows)
	}
	return append(steps, fixtureStep{query: utf16leLiteralSQL, rows: rows})
}

type utf16leRows struct {
	fixtureRows
	database string
	scan     reflect.Type
}

func newUTF16LERows() *utf16leRows {
	seed := utf16leSeeds()[0]
	return &utf16leRows{
		fixtureRows: fixtureRows{width: 2, values: [][]driver.Value{{seed.text, seed.hex}}},
		database:    typeNameVarchar,
		scan:        reflect.TypeFor[string](),
	}
}

func (r *utf16leRows) ColumnTypeDatabaseTypeName(int) string { return r.database }
func (r *utf16leRows) ColumnTypeScanType(int) reflect.Type   { return r.scan }
