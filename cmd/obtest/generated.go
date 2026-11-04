package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
)

const (
	generatedCase     = "mysql-generated-columns"
	generatedDDL      = "(ID INTEGER PRIMARY KEY,A INTEGER,B INTEGER AS (A*2) STORED,V INTEGER AS (A+1) VIRTUAL)"
	generatedInsert   = "INSERT INTO %s (ID,A) VALUES (?,?)"
	generatedUpdate   = "UPDATE %s SET A=? WHERE ID=?"
	generatedReject   = "INSERT INTO %s (ID,A,B) VALUES (?,?,?)"
	generatedTextSQL  = "SELECT A,B,V FROM %s WHERE ID>=1 ORDER BY ID"
	generatedBoundSQL = "SELECT A,B,V FROM %s WHERE ID>=? ORDER BY ID"
	generatedCountSQL = "SELECT COUNT(*) FROM %s WHERE ID>=90"
	generatedRejected = 3105
	generatedTypeName = "INTEGER"
)

type generatedSeed struct {
	a, b, v sql.NullInt64
}

// runMySQLGeneratedColumns verifies STORED and VIRTUAL generated columns: they
// are computed (not bound), follow NULL inputs, are recomputed after an update,
// keep INTEGER/int64 metadata and reject explicit writes with 3105.
func runMySQLGeneratedColumns(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, generatedDDL)
	if err != nil {
		return err
	}
	for id, a := range []any{21, nil, 0} {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(generatedInsert, table), id+1, a); err != nil {
			return err
		}
	}
	if err = readGenerated(ctx, s.db, table, false, generatedInitial()); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(generatedUpdate, table), 10, 1); err != nil {
		return err
	}
	if err = readGenerated(ctx, s.db, table, true, generatedUpdated()); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(generatedReject, table), 91, 1, 2); !onlyServerErrorCode(err, generatedRejected) {
		return checkFailure("explicit write to a generated column was not rejected")
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(generatedCountSQL, table)).Scan(&stored); err != nil {
		return err
	}
	return expect(stored == 0, "rejected generated column write left a stored row")
}

func readGenerated(ctx context.Context, db *sql.DB, table string, parameterized bool, want []generatedSeed) (err error) {
	query, args := fmt.Sprintf(generatedTextSQL, table), []any(nil)
	if parameterized {
		query, args = fmt.Sprintf(generatedBoundSQL, table), []any{1}
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
	if err = checkGeneratedMetadata(columns); err != nil {
		return err
	}
	for _, seed := range want {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("generated column row missing")
		}
		var a, b, v sql.NullInt64
		if err = rows.Scan(&a, &b, &v); err != nil {
			return err
		}
		if err = expect(a == seed.a && b == seed.b && v == seed.v, "generated column value changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected generated column row")
	}
	return rows.Err()
}

func checkGeneratedMetadata(columns []*sql.ColumnType) error {
	if len(columns) != 3 {
		return checkFailure("generated column metadata missing")
	}
	for _, column := range columns {
		if column.DatabaseTypeName() != generatedTypeName || column.ScanType() != reflect.TypeFor[int64]() {
			return checkFailure("generated column metadata changed")
		}
	}
	return nil
}

func generatedInitial() []generatedSeed {
	return []generatedSeed{
		{generatedInt(21), generatedInt(42), generatedInt(22)},
		{sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}},
		{generatedInt(0), generatedInt(0), generatedInt(1)},
	}
}

func generatedUpdated() []generatedSeed {
	return []generatedSeed{
		{generatedInt(10), generatedInt(20), generatedInt(11)},
		{sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}},
		{generatedInt(0), generatedInt(0), generatedInt(1)},
	}
}

func generatedInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: true}
}
