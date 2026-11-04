package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/cocosip/go-oceanbase"
)

const (
	emptyLOBCase      = "empty-lob"
	emptyLOBInsertSQL = "INSERT INTO %s VALUES (?,?,?)"
	emptyLOBSelectSQL = "SELECT B,T FROM %s ORDER BY ID"
	emptyLOBRawSQL    = "SELECT B FROM %s WHERE ID=%d"
	emptyLOBNullSQL   = "SELECT CASE WHEN B IS NULL THEN 1 ELSE 0 END,CASE WHEN T IS NULL THEN 1 ELSE 0 END FROM %s ORDER BY ID"
)

type emptyLOBProfile struct {
	ddl         string
	emptyInsert string
	// boundEmpty keeps an empty value for a bound zero-length parameter; Oracle
	// mode treats an empty string as NULL, so only MySQL keeps it.
	boundEmpty bool
}

func emptyLOBProfileFor(mode oceanbase.Mode) emptyLOBProfile {
	if mode == oceanbase.ModeOracle {
		return emptyLOBProfile{
			ddl:         "(ID INTEGER PRIMARY KEY,B BLOB,T CLOB)",
			emptyInsert: "INSERT INTO %s VALUES (1,EMPTY_BLOB(),EMPTY_CLOB())",
			boundEmpty:  false,
		}
	}
	return emptyLOBProfile{
		ddl:         "(ID INTEGER PRIMARY KEY,B BLOB,T TEXT)",
		emptyInsert: "INSERT INTO %s VALUES (1,'','')",
		boundEmpty:  true,
	}
}

// runEmptyLOB verifies that a zero-length LOB is distinct from SQL NULL: the
// server-side empty value scans as a valid empty value with a non-nil raw slice,
// SQL NULL scans as invalid/nil, and a bound zero-length parameter keeps that
// meaning in MySQL mode while Oracle mode (where an empty string is NULL) turns
// it into NULL.
func runEmptyLOB(ctx context.Context, s *scenario) error {
	profile := emptyLOBProfileFor(s.mode)
	table, err := s.table(ctx, profile.ddl)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(profile.emptyInsert, table)); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(emptyLOBInsertSQL, table), 2, nil, nil); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(emptyLOBInsertSQL, table), 3, []byte{}, ""); err != nil {
		return err
	}
	if err = readEmptyLOB(ctx, s.db, table, profile); err != nil {
		return err
	}
	if err = readEmptyLOBRaw(ctx, s.db, table); err != nil {
		return err
	}
	return readEmptyLOBNulls(ctx, s.db, table, profile)
}

func readEmptyLOB(ctx context.Context, db *sql.DB, table string, profile emptyLOBProfile) (err error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(emptyLOBSelectSQL, table))
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	want := []struct {
		valid bool
	}{
		{true},
		{false},
		{profile.boundEmpty},
	}
	for _, expected := range want {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("empty LOB row missing")
		}
		var b sql.Null[[]byte]
		var t sql.Null[string]
		if err = rows.Scan(&b, &t); err != nil {
			return err
		}
		if err = expect(b.Valid == expected.valid && t.Valid == expected.valid, "empty LOB lost its NULL distinction"); err != nil {
			return err
		}
		if err = expect(len(b.V) == 0 && len(t.V) == 0, "empty LOB returned content"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected empty LOB row")
	}
	return rows.Err()
}

func readEmptyLOBRaw(ctx context.Context, db *sql.DB, table string) error {
	var empty []byte
	if err := db.QueryRowContext(ctx, fmt.Sprintf(emptyLOBRawSQL, table, 1)).Scan(&empty); err != nil {
		return err
	}
	if err := expect(len(empty) == 0, "empty LOB raw value changed"); err != nil {
		return err
	}
	var missing []byte
	if err := db.QueryRowContext(ctx, fmt.Sprintf(emptyLOBRawSQL, table, 2)).Scan(&missing); err != nil {
		return err
	}
	return expect(missing == nil, "NULL LOB raw value changed")
}

func readEmptyLOBNulls(ctx context.Context, db *sql.DB, table string, profile emptyLOBProfile) (err error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(emptyLOBNullSQL, table))
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	want := []int64{0, 1, 0}
	if !profile.boundEmpty {
		want[2] = 1
	}
	for _, expected := range want {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("empty LOB NULL flag row missing")
		}
		var binaryNull, textNull int64
		if err = rows.Scan(&binaryNull, &textNull); err != nil {
			return err
		}
		if err = expect(binaryNull == expected && textNull == expected, "empty LOB NULL flag changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected empty LOB NULL flag row")
	}
	return rows.Err()
}
