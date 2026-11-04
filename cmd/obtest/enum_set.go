package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
)

const (
	enumSetCase       = "mysql-enum-set"
	enumSetDDL        = "(ID INTEGER PRIMARY KEY,E ENUM('a','b','c'),S SET('x','y','z'))"
	enumSetInsertSQL  = "INSERT INTO %s VALUES (?,?,?)"
	enumSetSelectSQL  = "SELECT E,S FROM %s WHERE ID>=1 ORDER BY ID"
	enumSetBoundSQL   = "SELECT E,S FROM %s WHERE ID>=? ORDER BY ID"
	enumSetCountSQL   = "SELECT COUNT(*) FROM %s WHERE ID>=90"
	enumSetRejectCode = 1265
	enumSetTypeName   = "CHAR"
)

type enumSetSeed struct {
	id int
	e  any
	s  any
	// eWant/sWant are the target's stored text after normalization.
	eWant string
	sWant string
}

func enumSetSeeds() []enumSetSeed {
	return []enumSetSeed{
		{1, "b", "x,z", "b", "x,z"},
		{2, "c", "y", "c", "y"},
		{3, nil, nil, "", ""},
		{4, "a", "z,y", "a", "y,z"},
		{5, "a", "x,x", "a", "x"},
	}
}

// runMySQLEnumSet verifies ENUM/SET columns as CHAR text: accepted members round
// trip, SET input is normalized (ordered, deduplicated), NULL stays NULL and
// invalid members are rejected with 1265 without storing a row.
func runMySQLEnumSet(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, enumSetDDL)
	if err != nil {
		return err
	}
	for _, seed := range enumSetSeeds() {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(enumSetInsertSQL, table), seed.id, seed.e, seed.s); err != nil {
			return err
		}
	}
	if err = readEnumSet(ctx, s.db, table, false); err != nil {
		return err
	}
	if err = readEnumSet(ctx, s.db, table, true); err != nil {
		return err
	}
	for _, rejected := range []struct {
		e, s  string
		label string
	}{
		{"d", "x", "invalid ENUM member was not rejected"},
		{"a", "x,w", "invalid SET member was not rejected"},
	} {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(enumSetInsertSQL, table), 90, rejected.e, rejected.s); !onlyServerErrorCode(err, enumSetRejectCode) {
			return checkFailure(rejected.label)
		}
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(enumSetCountSQL, table)).Scan(&stored); err != nil {
		return err
	}
	return expect(stored == 0, "rejected ENUM/SET left a stored row")
}

func readEnumSet(ctx context.Context, db *sql.DB, table string, parameterized bool) (err error) {
	query, args := fmt.Sprintf(enumSetSelectSQL, table), []any(nil)
	if parameterized {
		query, args = fmt.Sprintf(enumSetBoundSQL, table), []any{1}
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
	if err = checkEnumSetMetadata(columns); err != nil {
		return err
	}
	for _, seed := range enumSetSeeds() {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("ENUM/SET row missing")
		}
		var enum, set sql.NullString
		if err = rows.Scan(&enum, &set); err != nil {
			return err
		}
		want := sql.NullString{String: seed.eWant, Valid: seed.e != nil}
		wantSet := sql.NullString{String: seed.sWant, Valid: seed.s != nil}
		if err = expect(enum == want && set == wantSet, "ENUM/SET text changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected ENUM/SET row")
	}
	return rows.Err()
}

func checkEnumSetMetadata(columns []*sql.ColumnType) error {
	if len(columns) != 2 {
		return checkFailure("ENUM/SET metadata missing")
	}
	for _, column := range columns {
		if column.DatabaseTypeName() != enumSetTypeName || column.ScanType() != reflect.TypeFor[string]() {
			return checkFailure("ENUM/SET metadata changed")
		}
	}
	return nil
}
