package main

import (
	"bytes"
	"context"
	"database/sql"
	"math"
	"reflect"
	"strings"
)

type mysqlScalarSample struct {
	duration                   string
	year                       int64
	unsigned                   uint64
	signed, tiny               int64
	bit1                       byte
	bit9                       uint16
	variable, fixed, wantFixed []byte
}

func mysqlScalarSamples() []mysqlScalarSample {
	return []mysqlScalarSample{
		{duration: "-838:59:58.999999", year: 2026, unsigned: math.MaxUint64, signed: math.MinInt64, tiny: 2, bit1: 1, bit9: 257, variable: []byte{0, 255}, fixed: []byte{0, 255}, wantFixed: []byte{0, 255, 0, 0}},
		{duration: "838:59:59", signed: math.MaxInt64, tiny: -128, variable: []byte{}, fixed: []byte{}, wantFixed: []byte{0, 0, 0, 0}},
		{duration: "00:00:00", year: 2155, unsigned: 1 << 63, signed: -1, tiny: 127, bit1: 1, bit9: 511, variable: []byte{0, 0}, fixed: []byte{0, 1, 2, 3}, wantFixed: []byte{0, 1, 2, 3}},
	}
}

func runMySQLScalars(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,TM TIME(6),Y YEAR,U BIGINT UNSIGNED,S BIGINT,T TINYINT(1),B1 BIT(1),B9 BIT(9),VB VARBINARY(100),BF BINARY(4))")
	if err != nil {
		return err
	}
	samples := mysqlScalarSamples()
	for i := 0; i <= len(samples); i++ {
		args := []any{i + 1, nil, nil, nil, nil, nil, nil, nil, nil, nil}
		if i < len(samples) {
			v := samples[i]
			args = []any{i + 1, v.duration, v.year, v.unsigned, v.signed, v.tiny, int64(v.bit1), int64(v.bit9), v.variable, v.fixed}
		}
		r, e := s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?,?,?,?,?,?,?,?,?)", args...)
		if e = affected(r, e, 1); e != nil {
			return e
		}
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, parameterized := range []bool{false, true} {
			if err := readMySQLScalars(ctx, c, table, parameterized); err != nil {
				return err
			}
		}
		return nil
	})
}

func readMySQLScalars(ctx context.Context, c *sql.Conn, table string, parameterized bool) (err error) {
	query, args := "SELECT TM,Y,U,S,T,B1,B9,VB,BF FROM "+table+" WHERE ID>=1 ORDER BY ID", []any(nil)
	if parameterized {
		query, args = "SELECT TM,Y,U,S,T,B1,B9,VB,BF FROM "+table+" WHERE ID>=? ORDER BY ID", []any{1}
	}
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	if err = checkMySQLScalarMetadata(rows); err != nil {
		return err
	}
	samples := mysqlScalarSamples()
	for i := 0; i <= len(samples); i++ {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("MySQL scalar row missing")
		}
		var want *mysqlScalarSample
		if i < len(samples) {
			want = &samples[i]
		}
		if err = checkMySQLScalarRow(rows, want); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected MySQL scalar row")
	}
	return rows.Err()
}

func checkMySQLScalarMetadata(rows *sql.Rows) error {
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	names := []string{"TIME", "YEAR", "BIGINT", "BIGINT", "TINYINT", "BIT", "BIT", "VARBINARY", "BINARY"}
	types := []reflect.Type{reflect.TypeFor[string](), reflect.TypeFor[int64](), reflect.TypeFor[uint64](), reflect.TypeFor[int64](), reflect.TypeFor[int64](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte](), reflect.TypeFor[[]byte]()}
	if len(columns) != len(names) {
		return checkFailure("MySQL scalar metadata missing")
	}
	for i, c := range columns {
		if c.DatabaseTypeName() != names[i] || c.ScanType() != types[i] {
			return checkFailure("MySQL scalar type metadata mismatch")
		}
		if i >= 7 {
			want := int64(100)
			if i == 8 {
				want = 4
			}
			if length, known := c.Length(); !known || length != want {
				return checkFailure("MySQL binary byte length mismatch")
			}
		}
	}
	return nil
}

func checkMySQLScalarRow(rows *sql.Rows, want *mysqlScalarSample) error {
	var duration sql.NullString
	var year, signed, tiny sql.NullInt64
	var unsigned sql.Null[uint64]
	var bit1, bit9, variable, fixed sql.Null[[]byte]
	if err := rows.Scan(&duration, &year, &unsigned, &signed, &tiny, &bit1, &bit9, &variable, &fixed); err != nil {
		return err
	}
	if want == nil {
		return expect(!duration.Valid && !year.Valid && !unsigned.Valid && !signed.Valid && !tiny.Valid && !bit1.Valid && !bit9.Valid && !variable.Valid && !fixed.Valid, "MySQL scalar NULL changed")
	}
	numbers := year.Valid && year.Int64 == want.year && unsigned.Valid && unsigned.V == want.unsigned && signed.Valid && signed.Int64 == want.signed && tiny.Valid && tiny.Int64 == want.tiny
	binary := bit1.Valid && bytes.Equal(bit1.V, []byte{want.bit1}) && bit9.Valid && bytes.Equal(bit9.V, []byte{byte(want.bit9 >> 8), byte(want.bit9 & 255)}) && variable.Valid && bytes.Equal(variable.V, want.variable) && fixed.Valid && bytes.Equal(fixed.V, want.wantFixed)
	clock := duration.Valid && (duration.String == want.duration || !strings.Contains(want.duration, ".") && duration.String == want.duration+".000000")
	return expect(numbers && binary && clock, "MySQL scalar content mismatch")
}
