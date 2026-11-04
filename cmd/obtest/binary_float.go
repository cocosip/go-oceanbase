package main

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"reflect"
)

const (
	binaryFloatCase   = "oracle-binary-float"
	binaryFloatDDL    = "(ID INTEGER PRIMARY KEY,F BINARY_FLOAT,G BINARY_DOUBLE)"
	binaryFloatInsert = "INSERT INTO %s VALUES (?,?,?)"
	binaryFloatSelect = "SELECT F,G FROM %s WHERE ID>=1 ORDER BY ID"
	binaryFloatBound  = "SELECT F,G FROM %s WHERE ID>=? ORDER BY ID"
	binaryFloatType   = "BINARY_FLOAT"
	binaryDoubleType  = "BINARY_DOUBLE"
)

// binaryFloatSeed is one row: the bound float64, the float32 value the server
// must store in BINARY_FLOAT and the exact BINARY_DOUBLE value.
type binaryFloatSeed struct {
	id     int
	v      any
	float  sql.NullFloat64
	double sql.NullFloat64
}

func binaryFloatSeeds() []binaryFloatSeed {
	float32Max := float64(math.MaxFloat32)
	float32Min := float64(math.SmallestNonzeroFloat32)
	return []binaryFloatSeed{
		{1, float32Max, binaryFloatWant(float64(float32(float32Max))), binaryFloatWant(float32Max)},
		{2, 1.1754943508222875e-38, binaryFloatWant(1.1754943508222875e-38), binaryFloatWant(1.1754943508222875e-38)},
		{3, 0.1, binaryFloatWant(float64(float32(0.1))), binaryFloatWant(0.1)},
		{4, math.NaN(), binaryFloatWant(math.NaN()), binaryFloatWant(math.NaN())},
		{5, math.Inf(1), binaryFloatWant(math.Inf(1)), binaryFloatWant(math.Inf(1))},
		{6, math.Inf(-1), binaryFloatWant(math.Inf(-1)), binaryFloatWant(math.Inf(-1))},
		{7, float32Min, binaryFloatWant(float64(float32(float32Min))), binaryFloatWant(float32Min)},
		{8, nil, sql.NullFloat64{}, sql.NullFloat64{}},
	}
}

// runOracleBinaryFloat verifies BINARY_FLOAT as float32 precision (including
// subnormals, NaN and infinities) alongside exact BINARY_DOUBLE values, with
// BINARY_FLOAT/BINARY_DOUBLE metadata and no declared decimal size.
func runOracleBinaryFloat(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, binaryFloatDDL)
	if err != nil {
		return err
	}
	for _, seed := range binaryFloatSeeds() {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(binaryFloatInsert, table), seed.id, seed.v, seed.v); err != nil {
			return err
		}
	}
	if err = readBinaryFloat(ctx, s.db, table, false); err != nil {
		return err
	}
	return readBinaryFloat(ctx, s.db, table, true)
}

func readBinaryFloat(ctx context.Context, db *sql.DB, table string, parameterized bool) (err error) {
	query, args := fmt.Sprintf(binaryFloatSelect, table), []any(nil)
	if parameterized {
		query, args = fmt.Sprintf(binaryFloatBound, table), []any{1}
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	if err = checkBinaryFloatMetadata(columns); err != nil {
		return err
	}
	for _, seed := range binaryFloatSeeds() {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("binary float row missing")
		}
		var float, double sql.NullFloat64
		if err = rows.Scan(&float, &double); err != nil {
			return err
		}
		if err = expect(sameFloat(float, seed.float) && sameFloat(double, seed.double), "binary float value changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected binary float row")
	}
	return rows.Err()
}

func checkBinaryFloatMetadata(columns []*sql.ColumnType) error {
	if len(columns) != 2 || columns[0].DatabaseTypeName() != binaryFloatType || columns[1].DatabaseTypeName() != binaryDoubleType {
		return checkFailure("binary float type metadata changed")
	}
	for _, column := range columns {
		if _, _, ok := column.DecimalSize(); ok || column.ScanType() != reflect.TypeFor[float64]() {
			return checkFailure("binary float size metadata changed")
		}
	}
	return nil
}

func sameFloat(got, want sql.NullFloat64) bool {
	if got.Valid != want.Valid {
		return false
	}
	if !got.Valid {
		return true
	}
	if math.IsNaN(want.Float64) {
		return math.IsNaN(got.Float64)
	}
	return got.Float64 == want.Float64
}

func binaryFloatWant(value float64) sql.NullFloat64 {
	return sql.NullFloat64{Float64: value, Valid: true}
}
