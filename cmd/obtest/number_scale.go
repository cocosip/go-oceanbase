package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
)

const (
	numberScaleCase        = "oracle-number-scale"
	numberScaleDDL         = "(ID INTEGER PRIMARY KEY,V NUMBER(10,-2),W NUMBER(5,8))"
	numberScaleInsertSQL   = "INSERT INTO %s VALUES (?,?,?)"
	numberScaleSelectSQL   = "SELECT V,W FROM %s WHERE ID>=1 ORDER BY ID"
	numberScaleBoundSQL    = "SELECT V,W FROM %s WHERE ID>=? ORDER BY ID"
	numberScaleRejectSQL   = "INSERT INTO %s VALUES (?,?,?)"
	numberScaleCountSQL    = "SELECT COUNT(*) FROM %s WHERE ID>=90"
	numberScaleOverflowErr = 1438
)

// numberScaleSeed is one row of declared negative-scale and scale>precision values.
type numberScaleSeed struct {
	id    int
	v     any
	w     any
	vWant sql.NullString
	wWant sql.NullString
}

// runOracleNumberScale covers NUMBER(p,-s) rounding on write and NUMBER(p,s>p)
// magnitude limits: declared scale is reported negative (or above precision)
// while reads keep the target's normalized text.
func runOracleNumberScale(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, numberScaleDDL)
	if err != nil {
		return err
	}
	for _, seed := range numberScaleSeeds() {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(numberScaleInsertSQL, table), seed.id, seed.v, seed.w); err != nil {
			return err
		}
	}
	if err = readNumberScale(ctx, s.db, table, false); err != nil {
		return err
	}
	if err = readNumberScale(ctx, s.db, table, true); err != nil {
		return err
	}
	for _, rejected := range []struct {
		v, w  string
		label string
	}{
		{"1000000000000", "0", "negative-scale magnitude was not rejected"},
		{"0", "0.001", "scale above precision was not rejected"},
	} {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(numberScaleRejectSQL, table), 91, rejected.v, rejected.w); !onlyServerErrorCode(err, numberScaleOverflowErr) {
			return checkFailure(rejected.label)
		}
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(numberScaleCountSQL, table)).Scan(&stored); err != nil {
		return err
	}
	return expect(stored == 0, "rejected NUMBER left a stored row")
}

func readNumberScale(ctx context.Context, db *sql.DB, table string, parameterized bool) (err error) {
	query, args := fmt.Sprintf(numberScaleSelectSQL, table), []any(nil)
	if parameterized {
		query, args = fmt.Sprintf(numberScaleBoundSQL, table), []any{1}
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
	if err = checkNumberScaleMetadata(columns); err != nil {
		return err
	}
	for _, seed := range numberScaleSeeds() {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("NUMBER row missing")
		}
		var v, w sql.NullString
		if err = rows.Scan(&v, &w); err != nil {
			return err
		}
		if err = expect(v == seed.vWant && w == seed.wWant, "NUMBER text changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected NUMBER row")
	}
	return rows.Err()
}

func checkNumberScaleMetadata(columns []*sql.ColumnType) error {
	if len(columns) != 2 {
		return checkFailure("NUMBER metadata missing")
	}
	for i, want := range []struct{ precision, scale int64 }{{10, -2}, {5, 8}} {
		precision, scale, ok := columns[i].DecimalSize()
		if !ok || precision != want.precision || scale != want.scale || columns[i].ScanType() != reflect.TypeFor[string]() {
			return checkFailure("NUMBER precision/scale changed")
		}
	}
	return nil
}

func numberScaleSeeds() []numberScaleSeed {
	return []numberScaleSeed{
		{1, "12345", "0", numberString("12300"), numberString("0")},
		{2, "-12345", "0.00012345", numberString("-12300"), numberString(".00012345")},
		{3, "100", "0.00099999", numberString("100"), numberString(".00099999")},
		{4, "99999999", "-0.00012345", numberString("100000000"), numberString("-.00012345")},
		{5, nil, nil, sql.NullString{}, sql.NullString{}},
	}
}

func numberString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}
