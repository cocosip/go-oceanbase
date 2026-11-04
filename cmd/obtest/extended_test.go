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
	extendedTypesCase         = "extended-types"
	geometryCase              = "geometry"
	extendedCreateSQL         = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V1 VECTOR(3),V2 SPARSEVECTOR,V3 ARRAY(INT),V4 ROARINGBITMAP,V5 JSON) ENGINE=InnoDB"
	extendedInsertSQL         = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?,?,rb_from_string(?),?)"
	extendedTextSelect        = "SELECT V1,V2,V3,V4,V5 FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	extendedBoundSelect       = "SELECT V1,V2,V3,V4,V5 FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
	extendedBitmapSQL         = "SELECT rb_to_string(V4) FROM OBT_OFFLINE_T WHERE ID=?"
	extendedCountSQL          = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID=?"
	extendedVectorSQL         = "SELECT V1 FROM OBT_OFFLINE_T WHERE ID=?"
	extendedJSONSQL           = "SELECT JSON_UNQUOTE(JSON_EXTRACT(V5,'$.name')) FROM OBT_OFFLINE_T WHERE ID=1"
	extendedJSONBoundSQL      = "SELECT JSON_UNQUOTE(JSON_EXTRACT(V5,'$.name')) FROM OBT_OFFLINE_T WHERE ID=?"
	geometryCreateSQL         = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,G GEOMETRY) ENGINE=InnoDB"
	geometryInsertSQL         = "INSERT INTO OBT_OFFLINE_T VALUES (?,ST_GeomFromText(?))"
	geometryReadSQL           = "SELECT G,ST_AsText(G),ST_SRID(G) FROM OBT_OFFLINE_T WHERE ID=?"
	geometryRebindSQL         = "INSERT INTO OBT_OFFLINE_T VALUES (?,?)"
	geometryRawSQL            = "SELECT G FROM OBT_OFFLINE_T WHERE ID=?"
	geometrySRIDSQL           = "INSERT INTO OBT_OFFLINE_T VALUES (?,ST_GeomFromText(?,?))"
	geometryLiteralSQL        = "INSERT INTO OBT_OFFLINE_T VALUES (99,ST_GeomFromText('LINESTRING(0 0,1 1)',4326))"
	geometryCountSQL          = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID>=90"
	missingSRSCode            = uint32(3548)
	caseUnclassifiedRejection = "unclassified-rejection"
	malformedVectorCode       = uint32(7600)
)

var geometryBytes = []byte{0, 0, 0, 0, 1, 2, 3}

func scenarioDefinition(t *testing.T, name string, mode oceanbase.Mode) caseDefinition {
	t.Helper()
	for _, definition := range caseDefinitions() {
		if definition.name == name {
			if definition.run == nil || definition.mysqlOnly == definition.oracleOnly || (mode == oceanbase.ModeMySQL) != definition.mysqlOnly {
				t.Fatalf("%s scenario is missing or not %s-mode gated", name, map[oceanbase.Mode]string{oceanbase.ModeMySQL: "MySQL", oceanbase.ModeOracle: "Oracle"}[mode])
			}
			return definition
		}
	}
	t.Fatalf("%s scenario is not registered", name)
	return caseDefinition{}
}

type extendedTypeCase struct {
	name       string
	mutate     func(*extendedTypeFixtureRows)
	insertCode uint32
	normalized string
	wantOK     bool
}

func (c extendedTypeCase) rejectionCode() uint32 {
	if c.insertCode != 0 {
		return c.insertCode
	}
	return malformedVectorCode
}

func (c extendedTypeCase) normalizationText() string {
	if c.normalized != "" {
		return c.normalized
	}
	return "[0,0,0]"
}

func extendedTypeCases() []extendedTypeCase {
	return []extendedTypeCase{
		{name: "canonical-text", wantOK: true},
		{name: "vector-text", mutate: func(r *extendedTypeFixtureRows) { r.values[0][0] = "[1,2,4]" }},
		{name: "sparse-text", mutate: func(r *extendedTypeFixtureRows) { r.values[0][1] = "{1:2,3:2}" }},
		{name: "array-text", mutate: func(r *extendedTypeFixtureRows) { r.values[0][2] = "[1,2]" }},
		{name: "bitmap-bytes", mutate: func(r *extendedTypeFixtureRows) { r.values[0][3] = []byte{} }},
		{name: "null-row", mutate: func(r *extendedTypeFixtureRows) { r.values[1][0] = extendedVector }},
		{name: "missing-null-row", mutate: func(r *extendedTypeFixtureRows) { r.values = r.values[:1] }},
		{name: "unexpected-row", mutate: func(r *extendedTypeFixtureRows) { r.values = append(r.values, r.values[0]) }},
		{name: "char-metadata", mutate: func(r *extendedTypeFixtureRows) { r.names[1] = routineFixtureVarchar }},
		{name: "blob-metadata", mutate: func(r *extendedTypeFixtureRows) { r.names[3] = routineFixtureVarchar }},
		{name: "bitmap-scan-type", mutate: func(r *extendedTypeFixtureRows) { r.types[3] = reflect.TypeFor[string]() }},
		{name: "json-text", mutate: func(r *extendedTypeFixtureRows) { r.values[0][4] = `{"other":1}` }},
		{name: caseUnclassifiedRejection, insertCode: 1064},
		{name: "normalization-changed", normalized: "[0,0,1]"},
	}
}

func TestExtendedTypesWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, extendedTypesCase, oceanbase.ModeMySQL)
	for _, tc := range extendedTypeCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := extendedTypeSeed(t)
			rows := newExtendedTypeFixtureRows()
			if tc.mutate != nil {
				tc.mutate(rows)
			}
			steps = append(steps, fixtureStep{query: extendedTextSelect, rows: rows})
			if tc.mutate == nil {
				steps = append(steps,
					fixtureStep{query: extendedBoundSelect, rows: newExtendedTypeFixtureRows()},
					fixtureStep{query: extendedBitmapSQL, values: [][]driver.Value{{bitmapMembers}}},
					fixtureStep{query: extendedJSONSQL, values: [][]driver.Value{{jsonName}}},
					fixtureStep{query: extendedJSONBoundSQL, values: [][]driver.Value{{jsonName}}},
					fixtureStep{query: extendedInsertSQL, err: &oceanbase.Error{Code: tc.rejectionCode()}},
				)
				if tc.rejectionCode() == malformedVectorCode {
					steps = append(steps,
						fixtureStep{query: extendedCountSQL, values: [][]driver.Value{{int64(0)}}},
						fixtureStep{query: extendedInsertSQL},
						fixtureStep{query: extendedVectorSQL, values: [][]driver.Value{{tc.normalizationText()}}},
					)
				}
			}
			steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("extended type workflow: %+v; want %s", result, want)
			}
			if !rows.closed {
				t.Error("extended type rows were not closed")
			}
		})
	}
}

func extendedTypeSeed(t *testing.T) []fixtureStep {
	t.Helper()
	steps := []fixtureStep{{query: extendedCreateSQL}}
	for _, seeded := range []struct {
		id                                      int
		vector, sparse, array, bitmap, document any
	}{{1, extendedVector, extendedSparse, extendedArray, bitmapMembers, jsonDocument}, {2, nil, nil, nil, nil, nil}} {
		want := []any{seeded.id, seeded.vector, seeded.sparse, seeded.array, seeded.bitmap, seeded.document}
		steps = append(steps, fixtureStep{query: extendedInsertSQL, inspect: func(got []driver.NamedValue) {
			values := make([]any, len(got))
			for i, arg := range got {
				values[i] = arg.Value
			}
			if !reflect.DeepEqual(values, want) {
				t.Errorf("extended type seed input changed: %#v; want %#v", values, want)
			}
		}})
	}
	return steps
}

type extendedTypeFixtureRows struct {
	fixtureRows
	names  []string
	types  []reflect.Type
	closed bool
}

func newExtendedTypeFixtureRows() *extendedTypeFixtureRows {
	return &extendedTypeFixtureRows{
		fixtureRows: fixtureRows{width: 5, values: [][]driver.Value{
			{extendedVector, extendedSparse, extendedArray, []byte{1, 2}, jsonDocument},
			{nil, nil, nil, nil, nil},
		}},
		names: []string{extendedCharType, extendedCharType, extendedCharType, blobType, "JSON"},
		types: []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[string](), reflect.TypeFor[string](), reflect.TypeFor[[]byte](), reflect.TypeFor[string]()},
	}
}

func (r *extendedTypeFixtureRows) Close() error                            { r.closed = true; return nil }
func (r *extendedTypeFixtureRows) ColumnTypeDatabaseTypeName(i int) string { return r.names[i] }
func (r *extendedTypeFixtureRows) ColumnTypeScanType(i int) reflect.Type   { return r.types[i] }

type geometryWorkflowCase struct {
	name   string
	mutate func(*geometryFixtureRows)
	rebind []byte
	code   uint32
	wantOK bool
}

func geometryCases() []geometryWorkflowCase {
	return []geometryWorkflowCase{
		{name: "srid-zero-round-trip", wantOK: true},
		{name: "text-normalized", mutate: func(r *geometryFixtureRows) { r.values[0][1] = geometryShapes[0] + " " }},
		{name: "non-zero-srid", mutate: func(r *geometryFixtureRows) { r.values[0][2] = int64(4326) }},
		{name: "missing-srid-prefix", mutate: func(r *geometryFixtureRows) { r.values[0][0] = []byte{0xff} }},
		{name: "rebound-bytes", rebind: []byte{9, 9, 9, 9}},
		{name: caseUnclassifiedRejection, code: 1064},
	}
}

func TestGeometryWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, geometryCase, oceanbase.ModeMySQL)
	for _, tc := range geometryCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := geometrySteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeMySQL
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("geometry workflow: %+v; want %s", result, want)
			}
		})
	}
}

func geometrySteps(t *testing.T, tc geometryWorkflowCase) []fixtureStep {
	t.Helper()
	steps := []fixtureStep{{query: geometryCreateSQL}}
	shapes := geometryShapes
	if tc.mutate != nil || tc.rebind != nil {
		shapes = geometryShapes[:1]
	}
	for i, shape := range shapes {
		id := int64(i + 1)
		steps = append(steps, fixtureStep{query: geometryInsertSQL, inspect: geometryInsertInspect(t, id, shape)})
		rows := &geometryFixtureRows{fixtureRows: fixtureRows{width: 3, values: [][]driver.Value{{geometryBytes, shape, int64(0)}}}}
		if tc.mutate != nil {
			tc.mutate(rows)
		}
		steps = append(steps, fixtureStep{query: geometryReadSQL, rows: rows})
		if tc.mutate != nil {
			break
		}
		rebound := geometryBytes
		if tc.rebind != nil {
			rebound = tc.rebind
		}
		steps = append(steps,
			fixtureStep{query: geometryRebindSQL, inspect: geometryRebindInspect(t, id+10)},
			fixtureStep{query: geometryRawSQL, rows: &geometryFixtureRows{fixtureRows: fixtureRows{width: 1, values: [][]driver.Value{{rebound}}}}},
		)
		if tc.rebind != nil {
			break
		}
		if tc.code != 0 {
			steps = append(steps, fixtureStep{query: geometrySRIDSQL, err: &oceanbase.Error{Code: tc.code}})
			return append(steps, fixtureStep{query: fixtureDropTableSQL})
		}
		steps = append(steps, fixtureStep{query: geometrySRIDSQL, err: &oceanbase.Error{Code: missingSRSCode}})
	}
	if tc.mutate == nil && tc.rebind == nil {
		steps = append(steps,
			fixtureStep{query: geometryLiteralSQL, err: &oceanbase.Error{Code: missingSRSCode}},
			fixtureStep{query: geometryCountSQL, values: [][]driver.Value{{int64(0)}}},
		)
	}
	return append(steps, fixtureStep{query: fixtureDropTableSQL})
}

func geometryInsertInspect(t *testing.T, id int64, shape string) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(id) || args[1].Value != shape {
			t.Errorf("geometry insert bound unexpected arguments for id %d", id)
		}
	}
}

func geometryRebindInspect(t *testing.T, id int64) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 2 || fmt.Sprint(args[0].Value) != fmt.Sprint(id) {
			t.Errorf("geometry rebind bound unexpected identity %d", id)
		}
		if _, ok := args[1].Value.([]byte); !ok {
			t.Errorf("geometry rebind did not send bytes: %T", args[1].Value)
		}
	}
}

type geometryFixtureRows struct{ fixtureRows }
