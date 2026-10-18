package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const metadataFixtureTable = "OBT_OFFLINE_T"
const metadataFixtureMismatch = "UNEXPECTED_METADATA"
const metadataFixtureNumber = "NUMBER"

func TestMetadataContentWorkflow(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range []struct {
			name   string
			part   int
			change func(*metadataFixtureRows)
		}{
			{name: "dictionary-values", part: -1},
			{name: "table-schema", part: 0, change: func(r *metadataFixtureRows) { r.values[0][1] = metadataFixtureMismatch }},
			{name: "table-name", part: 0, change: func(r *metadataFixtureRows) { r.values[0][2] = metadataFixtureMismatch }},
			{name: "table-kind", part: 0, change: func(r *metadataFixtureRows) { r.values[0][3] = "VIEW" }},
			{name: "table-catalog", part: 0, change: func(r *metadataFixtureRows) { r.values[0][0] = metadataFixtureMismatch }},
			{name: "column-schema", part: 1, change: func(r *metadataFixtureRows) { r.values[0][1] = metadataFixtureMismatch }},
			{name: "column-table", part: 1, change: func(r *metadataFixtureRows) { r.values[0][2] = metadataFixtureMismatch }},
			{name: "column-name", part: 1, change: func(r *metadataFixtureRows) { r.values[1][3] = "BAD_METADATA_COLUMN" }},
			{name: "column-order", part: 1, change: func(r *metadataFixtureRows) { r.values[0], r.values[1] = r.values[1], r.values[0] }},
			{name: "column-position", part: 1, change: func(r *metadataFixtureRows) { r.values[1][4] = int64(1) }},
			{name: "column-type", part: 1, change: func(r *metadataFixtureRows) { r.values[1][5] = "BLOB" }},
			{name: "integer-dictionary-type", part: 1, change: func(r *metadataFixtureRows) { r.values[0][5] = "FLOAT" }},
			{name: "character-length", part: 1, change: func(r *metadataFixtureRows) { r.values[1][6] = int64(99) }},
			{name: "integer-precision", part: 1, change: func(r *metadataFixtureRows) { r.values[0][7] = int64(1) }},
			{name: "integer-scale", part: 1, change: func(r *metadataFixtureRows) { r.values[0][8] = int64(1) }},
			{name: "key-nullability", part: 1, change: func(r *metadataFixtureRows) { r.values[0][9] = int64(1) }},
			{name: "nullable-column", part: 1, change: func(r *metadataFixtureRows) { r.values[1][9] = int64(0) }},
			{name: "unknown-length", part: 1, change: func(r *metadataFixtureRows) { r.values[1][6] = nil }},
			{name: "unknown-nullability", part: 1, change: func(r *metadataFixtureRows) { r.values[1][9] = nil }},
			{name: "unknown-precision", part: 1, change: func(r *metadataFixtureRows) { r.values[0][7] = nil }},
			{name: "unknown-scale", part: 1, change: func(r *metadataFixtureRows) { r.values[0][8] = nil }},
			{name: "column-catalog", part: 1, change: func(r *metadataFixtureRows) { r.values[0][0] = metadataFixtureMismatch }},
			{name: "key-table", part: 2, change: func(r *metadataFixtureRows) { r.values[0][2] = metadataFixtureMismatch }},
			{name: "key-schema", part: 2, change: func(r *metadataFixtureRows) { r.values[0][1] = metadataFixtureMismatch }},
			{name: "key-column", part: 2, change: func(r *metadataFixtureRows) { r.values[0][4] = "V" }},
			{name: "key-position", part: 2, change: func(r *metadataFixtureRows) { r.values[0][5] = int64(2) }},
			{name: "key-name", part: 2, change: func(r *metadataFixtureRows) { r.values[0][3] = "" }},
			{name: "key-catalog", part: 2, change: func(r *metadataFixtureRows) { r.values[0][0] = metadataFixtureMismatch }},
			{name: "missing-table", part: 0, change: func(r *metadataFixtureRows) { r.values = nil }},
			{name: "extra-table", part: 0, change: func(r *metadataFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "missing-column", part: 1, change: func(r *metadataFixtureRows) { r.values = r.values[:1] }},
			{name: "extra-column", part: 1, change: func(r *metadataFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "missing-key", part: 2, change: func(r *metadataFixtureRows) { r.values = nil }},
			{name: "extra-key", part: 2, change: func(r *metadataFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "result-column-label", part: 3, change: func(r *metadataFixtureRows) { r.labels[1] = "BAD_METADATA_COLUMN" }},
			{name: "result-column-type", part: 3, change: func(r *metadataFixtureRows) { r.types[1] = "BLOB" }},
			{name: "result-integer-type", part: 3, change: func(r *metadataFixtureRows) { r.types[0] = "FLOAT" }},
			{name: "dictionary-read-error", part: 1, change: func(r *metadataFixtureRows) { r.readErr = errors.New("fixture dictionary read") }},
			{name: "dictionary-close-error", part: 1, change: func(r *metadataFixtureRows) { r.closeErr = errors.New("fixture dictionary close") }},
		} {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				metadataWorkflow(t, mode, tc.part, tc.change)
			})
		}
	}
}

func TestMetadataSchemaFilters(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for i, kind := range []oceanbase.MetadataKind{oceanbase.MetadataTables, oceanbase.MetadataColumns, oceanbase.MetadataPrimaryKeys} {
			for _, wrongSchema := range []bool{false, true} {
				t.Run(modeName(mode)+"/"+string(rune('A'+i))+"/"+map[bool]string{false: "escaped-schema", true: "schema-case-changed"}[wrongSchema], func(t *testing.T) {
					r := metadataFixtureParts(mode)[i]
					for _, row := range r.values {
						row[1] = "App_%/"
						if wrongSchema {
							row[1] = "app_%/"
						}
					}
					s := fixtureScenario(t, []fixtureStep{{query: "", rows: r, inspect: func(args []driver.NamedValue) {
						if len(args) != 1 {
							t.Error("missing schema filter")
							return
						}
						filter := reflect.ValueOf(args[0].Value).FieldByName("filter")
						if filter.FieldByName("SchemaPattern").String() != "App/_/%//" {
							t.Error("schema pattern was not escaped exactly")
						}
					}}})
					c, err := s.db.Conn(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					err = checkDictionaryMetadata(context.Background(), c, mode, "App_%/", metadataFixtureTable, kind)
					closeErr := c.Close()
					if (err != nil) != wrongSchema || closeErr != nil || !r.closed {
						t.Fatalf("metadata schema: err=%v close=%v closed=%t", err, closeErr, r.closed)
					}
				})
			}
		}
	}
}

func metadataWorkflow(t *testing.T, mode oceanbase.Mode, failPart int, change func(*metadataFixtureRows)) {
	t.Helper()
	ddl := "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V VARCHAR(100))"
	if mode == oceanbase.ModeMySQL {
		ddl += " ENGINE=InnoDB"
	}
	steps := []fixtureStep{{query: ddl}, {query: ""}} // CurrentSchema uses an empty-command extension.
	parts := metadataFixtureParts(mode)
	for i, r := range parts {
		if i == failPart && change != nil {
			change(r)
		}
		query := ""
		inspect := func([]driver.NamedValue) { r.opened = true }
		if i == 3 {
			query = "SELECT ID,V FROM OBT_OFFLINE_T"
		} else {
			kind := []oceanbase.MetadataKind{oceanbase.MetadataTables, oceanbase.MetadataColumns, oceanbase.MetadataPrimaryKeys}[i]
			inspect = func(args []driver.NamedValue) {
				r.opened = true
				if len(args) != 1 {
					t.Error("missing metadata filter")
					return
				}
				filter := reflect.ValueOf(args[0].Value).FieldByName("filter")
				if filter.FieldByName("Kind").Uint() != uint64(kind) || filter.FieldByName("NamePattern").String() != "OBT/_OFFLINE/_T" {
					t.Error("metadata selected a different object or dictionary")
				}
			}
		}
		steps = append(steps, fixtureStep{query: query, rows: r, inspect: inspect, optional: failPart >= 0 && i > failPart})
	}
	steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
	s := fixtureScenario(t, steps)
	s.mode = mode
	result := runScenario(context.Background(), s, runMetadata)
	want := statusFail
	if failPart < 0 {
		want = statusPass
	}
	if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
		t.Fatalf("metadata workflow: %+v; want %s with cleanup", result, want)
	}
	for _, r := range parts {
		if r.opened && !r.closed {
			t.Error("metadata results not closed before cleanup")
		}
	}
}

type metadataFixtureRows struct {
	fixtureRows
	labels, types     []string
	opened, closed    bool
	readErr, closeErr error
}

func metadataFixtureParts(mode oceanbase.Mode) []*metadataFixtureRows {
	var catalog driver.Value
	idType, textType, precision := metadataFixtureNumber, "VARCHAR2", int64(38)
	resultTypes := []string{metadataFixtureNumber, "VARCHAR2"}
	if mode == oceanbase.ModeMySQL {
		catalog, idType, textType, precision = "def", "int", "varchar", 10
		resultTypes = []string{"INTEGER", "VARCHAR"}
	}
	return []*metadataFixtureRows{
		{fixtureRows: fixtureRows{width: 5, values: [][]driver.Value{{catalog, "", metadataFixtureTable, "TABLE", nil}}}},
		{fixtureRows: fixtureRows{width: 13, values: [][]driver.Value{
			{catalog, "", metadataFixtureTable, "ID", int64(1), idType, nil, precision, int64(0), int64(0), nil, nil, nil},
			{catalog, "", metadataFixtureTable, "V", int64(2), textType, int64(100), nil, nil, int64(1), nil, nil, nil},
		}}},
		{fixtureRows: fixtureRows{width: 6, values: [][]driver.Value{{catalog, "", metadataFixtureTable, "PK_AUTOGENERATED", "ID", int64(1)}}}},
		{fixtureRows: fixtureRows{width: 2}, labels: []string{"ID", "V"}, types: resultTypes},
	}
}

func (r *metadataFixtureRows) Columns() []string {
	if r.labels != nil {
		return r.labels
	}
	return r.fixtureRows.Columns()
}
func (r *metadataFixtureRows) ColumnTypeDatabaseTypeName(i int) string {
	if r.types != nil {
		return r.types[i]
	}
	return ""
}
func (r *metadataFixtureRows) Close() error { r.closed = true; return r.closeErr }
func (r *metadataFixtureRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 && r.readErr != nil {
		return r.readErr
	}
	return r.fixtureRows.Next(dest)
}
