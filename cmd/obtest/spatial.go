package main

import (
	"context"

	"github.com/cocosip/go-oceanbase"
)

const (
	sdoGeometryType    = "SDO_GEOMETRY"
	sdoPointType       = "SDO_POINT_TYPE"
	sdoInvalidSRIDCode = uint32(13029)
	sdoPointLiteral    = "SDO_GEOMETRY(2001,0,SDO_POINT_TYPE(116,39,NULL),NULL,NULL)"
	sdoPolygonLiteral  = "SDO_GEOMETRY(2003,0,NULL,SDO_ELEM_INFO_ARRAY(1,1003,1),SDO_ORDINATE_ARRAY(0,0,10,0,10,10,0,10))"
	sdoRejectedLiteral = "SDO_GEOMETRY(2001,4326,SDO_POINT_TYPE(116,39,NULL),NULL,NULL)"
)

// runOracleSpatial verifies SDO_GEOMETRY literal storage, member access and the
// driver's OBJECT/collection decoding of the stored value, plus the invalid-SRID
// rejection. Native OBJECT input needs the deployment's type owner and therefore
// stays in the integration suite instead of a standalone acceptance run.
func runOracleSpatial(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,G SDO_GEOMETRY)")
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,"+sdoPointLiteral+")"); err != nil {
		return err
	}
	var srid, x, y int
	if err = s.db.QueryRowContext(ctx, "SELECT t.G.SDO_SRID,t.G.SDO_POINT.X,t.G.SDO_POINT.Y FROM "+table+" t WHERE ID=1").Scan(&srid, &x, &y); err != nil {
		return err
	}
	if err = expect(srid == 0 && x == 116 && y == 39, "SDO point members changed"); err != nil {
		return err
	}
	var point oceanbase.Object
	if err = s.db.QueryRowContext(ctx, "SELECT G FROM "+table+" WHERE ID=?", 1).Scan(&point); err != nil {
		return err
	}
	if err = checkSDOPoint(point); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (2,"+sdoPolygonLiteral+")"); err != nil {
		return err
	}
	var polygon oceanbase.Object
	if err = s.db.QueryRowContext(ctx, "SELECT G FROM "+table+" WHERE ID=?", 2).Scan(&polygon); err != nil {
		return err
	}
	if err = checkSDOPolygon(polygon); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (3,"+sdoRejectedLiteral+")"); !onlyServerErrorCode(err, sdoInvalidSRIDCode) {
		return checkFailure("invalid SDO SRID was not rejected")
	}
	var rejected int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=3").Scan(&rejected); err != nil {
		return err
	}
	return expect(rejected == 0, "rejected SDO geometry left a stored row")
}

func checkSDOPoint(object oceanbase.Object) error {
	if object.Type != sdoGeometryType || len(object.Fields) != 5 {
		return checkFailure("SDO point object identity changed")
	}
	if object.Fields[0].Value != oceanbase.Number("2001") || object.Fields[1].Value != oceanbase.Number("0") {
		return checkFailure("SDO point GTYPE/SRID changed")
	}
	point, ok := object.Fields[2].Value.(oceanbase.Object)
	if !ok || point.Type != sdoPointType || len(point.Fields) != 3 {
		return checkFailure("SDO point nested object shape changed")
	}
	if point.Fields[0].Value != oceanbase.Number("116") || point.Fields[1].Value != oceanbase.Number("39") || point.Fields[2].Value != nil {
		return checkFailure("SDO point coordinates changed")
	}
	return nil
}

func checkSDOPolygon(object oceanbase.Object) error {
	if object.Type != sdoGeometryType || len(object.Fields) != 5 {
		return checkFailure("SDO polygon object identity changed")
	}
	if object.Fields[0].Value != oceanbase.Number("2003") || object.Fields[1].Value != oceanbase.Number("0") || object.Fields[2].Value != nil {
		return checkFailure("SDO polygon GTYPE/SRID/point changed")
	}
	for i, want := range [][]string{{"1", "1003", "1"}, {"0", "0", "10", "0", "10", "10", "0", "10"}} {
		array, ok := object.Fields[i+3].Value.(oceanbase.Array)
		if !ok || len(array.Elements) != len(want) {
			return checkFailure("SDO polygon collection shape changed")
		}
		for j, element := range want {
			if array.Elements[j] != oceanbase.Number(element) {
				return checkFailure("SDO polygon collection elements changed")
			}
		}
	}
	return nil
}
