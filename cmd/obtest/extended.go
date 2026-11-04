package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"reflect"
	"strings"
)

const (
	extendedVector   = "[1,2,3]"
	extendedSparse   = "{1:1,3:2}"
	extendedArray    = "[1,2,3]"
	bitmapMembers    = "1,2,4294967295"
	extendedCharType = "CHAR"
	typeNameVarchar2 = "VARCHAR2"
	jsonDocument     = "{\"name\":\"病人😀\"}"
	jsonName         = "病人😀"
)

// extendedTypeColumns lists the declared columns in read order with the metadata
// the driver must expose for them on this target.
func extendedTypeColumns() []struct {
	name, databaseType string
	scanType           reflect.Type
} {
	return []struct {
		name, databaseType string
		scanType           reflect.Type
	}{
		{"V1", extendedCharType, reflect.TypeFor[string]()},
		{"V2", extendedCharType, reflect.TypeFor[string]()},
		{"V3", extendedCharType, reflect.TypeFor[string]()},
		{"V4", blobType, reflect.TypeFor[[]byte]()},
	}
}

// runExtendedTypes verifies MySQL extended column types as the driver's generic
// text/bytes paths: canonical text round trips, NULL, metadata, a rejected
// malformed value without a stored row, and target-side normalization.
func runExtendedTypes(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V1 VECTOR(3),V2 SPARSEVECTOR,V3 ARRAY(INT),V4 ROARINGBITMAP,V5 JSON)")
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,rb_from_string(?),?)",
		1, extendedVector, extendedSparse, extendedArray, bitmapMembers, jsonDocument); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,rb_from_string(?),?)",
		2, nil, nil, nil, nil, nil); err != nil {
		return err
	}
	if err = readExtendedTypes(ctx, s.db, table, false); err != nil {
		return err
	}
	if err = readExtendedTypes(ctx, s.db, table, true); err != nil {
		return err
	}
	var bitmap string
	if err = s.db.QueryRowContext(ctx, "SELECT rb_to_string(V4) FROM "+table+" WHERE ID=?", 1).Scan(&bitmap); err != nil {
		return err
	}
	if err = expect(bitmap == bitmapMembers, "bitmap members changed"); err != nil {
		return err
	}
	for _, extracted := range []struct {
		query string
		args  []any
	}{
		{"SELECT JSON_UNQUOTE(JSON_EXTRACT(V5,'$.name')) FROM " + table + " WHERE ID=1", nil},
		{"SELECT JSON_UNQUOTE(JSON_EXTRACT(V5,'$.name')) FROM " + table + " WHERE ID=?", []any{1}},
	} {
		var name string
		if err = s.db.QueryRowContext(ctx, extracted.query, extracted.args...).Scan(&name); err != nil {
			return err
		}
		if err = expect(name == jsonName, "JSON embedded text changed"); err != nil {
			return err
		}
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,rb_from_string(?),?)",
		3, "[1,2]", extendedSparse, extendedArray, bitmapMembers, jsonDocument); !onlyServerErrorCode(err, 7600) {
		return checkFailure("malformed VECTOR was not rejected by dimension")
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID=?", 3).Scan(&stored); err != nil || stored != 0 {
		return checkFailure("rejected VECTOR left a stored row")
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,rb_from_string(?),?)",
		4, "[NaN,0,0]", extendedSparse, extendedArray, bitmapMembers, jsonDocument); err != nil {
		return err
	}
	var normalized string
	if err = s.db.QueryRowContext(ctx, "SELECT V1 FROM "+table+" WHERE ID=?", 4).Scan(&normalized); err != nil {
		return err
	}
	return expect(normalized == "[0,0,0]", "target VECTOR normalization changed")
}

func readExtendedTypes(ctx context.Context, db *sql.DB, table string, parameterized bool) (err error) {
	query := "SELECT V1,V2,V3,V4,V5 FROM " + table + " WHERE ID>=1 ORDER BY ID"
	var args []any
	if parameterized {
		query, args = "SELECT V1,V2,V3,V4,V5 FROM "+table+" WHERE ID>=? ORDER BY ID", []any{1}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	if err = checkExtendedTypeMetadata(rows); err != nil {
		return err
	}
	for _, populated := range []bool{true, false} {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("extended type row missing")
		}
		var first, second, third, document sql.NullString
		var bitmap []byte
		if err = rows.Scan(&first, &second, &third, &bitmap, &document); err != nil {
			return err
		}
		if !populated {
			if err = expect(!first.Valid && !second.Valid && !third.Valid && bitmap == nil && !document.Valid, "extended type NULL changed"); err != nil {
				return err
			}
			continue
		}
		if err = expect(first.Valid && second.Valid && third.Valid &&
			first.String == extendedVector && second.String == extendedSparse && third.String == extendedArray &&
			len(bitmap) != 0 && strings.Contains(document.String, jsonName), "extended type content changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected extended type row")
	}
	return rows.Err()
}

func checkExtendedTypeMetadata(rows *sql.Rows) error {
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	expected := extendedTypeColumns()
	// The JSON column is intentionally excluded: this target's JSON metadata is
	// not part of the effective live contract, only its value/extract behavior is.
	if len(columns) < len(expected) {
		return checkFailure("extended type metadata missing")
	}
	for i, column := range columns[:len(expected)] {
		if column.DatabaseTypeName() != expected[i].databaseType || column.ScanType() != expected[i].scanType {
			return checkFailure("extended type metadata mismatch for " + expected[i].name)
		}
	}
	return nil
}

// geometryShapes keeps the scenario and its offline workflow cases on the same
// SRID-0 WKT inputs.
var geometryShapes = []string{
	"LINESTRING(0 0,1 1)",
	"POLYGON((0 0,2 0,2 2,0 0))",
	"MULTIPOINT((1 2),(3 4))",
	"GEOMETRYCOLLECTION(POINT(1 2),LINESTRING(0 0,1 1))",
}

// runGeometry verifies SRID-0 GEOMETRY text/bytes round trips and the missing-SRS
// rejection for an explicit SRID, including a pure SQL literal control.
func runGeometry(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,G GEOMETRY)")
	if err != nil {
		return err
	}
	for i, wkt := range geometryShapes {
		if err = checkGeometryShape(ctx, s.db, table, i+1, wkt); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,ST_GeomFromText(?,?))", 90+i, wkt, 4326); !onlyServerErrorCode(err, 3548) {
			return checkFailure("bound SRID 4326 was not rejected for the missing SRS")
		}
	}
	// The fixed synthetic WKT contains no quotes or user data.
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (99,ST_GeomFromText('"+geometryShapes[0]+"',4326))"); !onlyServerErrorCode(err, 3548) {
		return checkFailure("literal SRID 4326 was not rejected for the missing SRS")
	}
	var rejected int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE ID>=90").Scan(&rejected); err != nil {
		return err
	}
	return expect(rejected == 0, "rejected SRID geometry left a stored row")
}

func checkGeometryShape(ctx context.Context, db *sql.DB, table string, id int, wkt string) error {
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,ST_GeomFromText(?))", id, wkt); err != nil {
		return err
	}
	var raw []byte
	var text string
	var srid uint32
	if err := db.QueryRowContext(ctx, "SELECT G,ST_AsText(G),ST_SRID(G) FROM "+table+" WHERE ID=?", id).Scan(&raw, &text, &srid); err != nil {
		return err
	}
	if err := expect(text == wkt && srid == 0, "SRID-0 geometry text changed"); err != nil {
		return err
	}
	if len(raw) < 5 || binary.LittleEndian.Uint32(raw[:4]) != 0 {
		return checkFailure("geometry bytes lost their SRID prefix")
	}
	reboundID := id + 10
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", reboundID, raw); err != nil {
		return err
	}
	var rebound []byte
	if err := db.QueryRowContext(ctx, "SELECT G FROM "+table+" WHERE ID=?", reboundID).Scan(&rebound); err != nil {
		return err
	}
	return expect(bytes.Equal(raw, rebound), "geometry byte rebound changed")
}
