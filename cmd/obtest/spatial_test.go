package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	oracleSpatialCase  = "oracle-spatial"
	spatialCreateSQL   = "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,G SDO_GEOMETRY)"
	spatialPointSQL    = "INSERT INTO OBT_OFFLINE_T VALUES (1,SDO_GEOMETRY(2001,0,SDO_POINT_TYPE(116,39,NULL),NULL,NULL))"
	spatialMemberSQL   = "SELECT t.G.SDO_SRID,t.G.SDO_POINT.X,t.G.SDO_POINT.Y FROM OBT_OFFLINE_T t WHERE ID=1"
	spatialObjectSQL   = "SELECT G FROM OBT_OFFLINE_T WHERE ID=?"
	spatialPolygonSQL  = "INSERT INTO OBT_OFFLINE_T VALUES (2,SDO_GEOMETRY(2003,0,NULL,SDO_ELEM_INFO_ARRAY(1,1003,1),SDO_ORDINATE_ARRAY(0,0,10,0,10,10,0,10)))"
	spatialRejectedSQL = "INSERT INTO OBT_OFFLINE_T VALUES (3,SDO_GEOMETRY(2001,4326,SDO_POINT_TYPE(116,39,NULL),NULL,NULL))"
	spatialCountSQL    = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID=3"
	spatialValidSRID   = uint32(13029)
	numberTypeName     = "NUMBER"
)

type spatialCase struct {
	name    string
	members [3]int64
	point   func(*oceanbase.Object)
	polygon func(*oceanbase.Object)
	code    uint32
	failAt  int
	wantOK  bool
}

func spatialCases() []spatialCase {
	return []spatialCase{
		{name: "point-and-polygon", wantOK: true},
		{name: "member-srid", members: [3]int64{4326, 116, 39}, failAt: 1},
		{name: "member-x", members: [3]int64{0, 117, 39}, failAt: 1},
		{name: "point-identity", point: func(o *oceanbase.Object) { o.Type = "OTHER_TYPE" }, failAt: 2},
		{name: "point-srid", point: func(o *oceanbase.Object) { o.Fields[1].Value = oceanbase.Number("4326") }, failAt: 2},
		{name: "point-nested-shape", point: func(o *oceanbase.Object) { o.Fields[2].Value = oceanbase.Number("1") }, failAt: 2},
		{name: "point-z-null", point: func(o *oceanbase.Object) {
			nested := o.Fields[2].Value.(oceanbase.Object)
			nested.Fields[2].Value = oceanbase.Number("0")
			o.Fields[2].Value = nested
		}, failAt: 2},
		{name: "polygon-gtype", polygon: func(o *oceanbase.Object) { o.Fields[0].Value = oceanbase.Number("2001") }, failAt: 3},
		{name: "polygon-element", polygon: func(o *oceanbase.Object) {
			array := o.Fields[3].Value.(oceanbase.Array)
			array.Elements[1] = oceanbase.Number("1004")
			o.Fields[3].Value = array
		}, failAt: 3},
		{name: caseUnclassifiedRejection, code: 1064, failAt: 4},
	}
}

func TestOracleSpatialWorkflow(t *testing.T) {
	definition := scenarioDefinition(t, oracleSpatialCase, oceanbase.ModeOracle)
	for _, tc := range spatialCases() {
		t.Run(tc.name, func(t *testing.T) {
			steps := spatialSteps(t, tc)
			s := fixtureScenario(t, steps)
			s.mode = oceanbase.ModeOracle
			result := runScenario(context.Background(), s, definition.run)
			want := statusFail
			if tc.wantOK {
				want = statusPass
			}
			if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
				t.Fatalf("oracle spatial workflow: %+v; want %s", result, want)
			}
		})
	}
}

func spatialSteps(t *testing.T, tc spatialCase) []fixtureStep {
	t.Helper()
	srid, x, y := int64(0), int64(116), int64(39)
	if tc.members != [3]int64{} {
		srid, x, y = tc.members[0], tc.members[1], tc.members[2]
	}
	steps := []fixtureStep{
		{query: spatialCreateSQL},
		{query: spatialPointSQL},
		{query: spatialMemberSQL, values: [][]driver.Value{{srid, x, y}}},
	}
	if tc.failAt == 1 {
		return append(steps, fixtureStep{query: fixtureDropTableSQL})
	}
	point := sdoPointObject()
	if tc.point != nil {
		tc.point(&point)
	}
	steps = append(steps, fixtureStep{query: spatialObjectSQL, values: [][]driver.Value{{spatialValue(t, point)}}})
	if tc.failAt == 2 {
		return append(steps, fixtureStep{query: fixtureDropTableSQL})
	}
	polygon := sdoPolygonObject()
	if tc.polygon != nil {
		tc.polygon(&polygon)
	}
	if tc.failAt == 4 {
		steps = append(steps, fixtureStep{query: spatialPolygonSQL}, fixtureStep{query: spatialObjectSQL, values: [][]driver.Value{{spatialValue(t, polygon)}}})
		return append(steps, fixtureStep{query: spatialRejectedSQL, err: &oceanbase.Error{Code: tc.code}}, fixtureStep{query: fixtureDropTableSQL})
	}
	steps = append(steps,
		fixtureStep{query: spatialPolygonSQL},
		fixtureStep{query: spatialObjectSQL, values: [][]driver.Value{{spatialValue(t, polygon)}}},
	)
	if tc.failAt == 3 {
		return append(steps, fixtureStep{query: fixtureDropTableSQL})
	}
	return append(steps,
		fixtureStep{query: spatialRejectedSQL, err: &oceanbase.Error{Code: spatialValidSRID}},
		fixtureStep{query: spatialCountSQL, values: [][]driver.Value{{int64(0)}}},
		fixtureStep{query: fixtureDropTableSQL},
	)
}

func spatialValue(t *testing.T, object oceanbase.Object) driver.Value {
	t.Helper()
	value, err := object.Value()
	if err != nil {
		t.Fatalf("encode fixture object: %v", err)
	}
	return value
}

func sdoPointObject() oceanbase.Object {
	return oceanbase.Object{Type: sdoGeometryType, Fields: []oceanbase.Attribute{
		{Name: "SDO_GTYPE", Type: numberTypeName, Value: oceanbase.Number("2001")},
		{Name: "SDO_SRID", Type: numberTypeName, Value: oceanbase.Number("0")},
		{Name: "SDO_POINT", Value: oceanbase.Object{Type: sdoPointType, Fields: []oceanbase.Attribute{
			{Name: "X", Type: numberTypeName, Value: oceanbase.Number("116")},
			{Name: "Y", Type: numberTypeName, Value: oceanbase.Number("39")},
			{Name: "Z"},
		}}},
		{Name: "SDO_ELEM_INFO"},
		{Name: "SDO_ORDINATES"},
	}}
}

func sdoPolygonObject() oceanbase.Object {
	elements := func(values ...string) oceanbase.Array {
		converted := make([]any, len(values))
		for i, value := range values {
			converted[i] = oceanbase.Number(value)
		}
		return oceanbase.Array{ElementType: numberTypeName, Elements: converted}
	}
	return oceanbase.Object{Type: sdoGeometryType, Fields: []oceanbase.Attribute{
		{Name: "SDO_GTYPE", Type: numberTypeName, Value: oceanbase.Number("2003")},
		{Name: "SDO_SRID", Type: numberTypeName, Value: oceanbase.Number("0")},
		{Name: "SDO_POINT"},
		{Name: "SDO_ELEM_INFO", Value: elements("1", "1003", "1")},
		{Name: "SDO_ORDINATES", Value: elements("0", "0", "10", "0", "10", "10", "0", "10")},
	}}
}
