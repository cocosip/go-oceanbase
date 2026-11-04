package main

import (
	"context"
	"database/sql"
	"reflect"
)

const (
	xmlTypeName  = "XMLTYPE"
	xmlText      = "<x>患者😀</x>"
	xmlStored    = xmlText + "\n"
	xmlCreateSQL = "(ID INTEGER PRIMARY KEY,X XMLTYPE)"
)

// runOracleXML verifies XMLTYPE as a string-valued column: the server-normalized
// serialization (trailing newline included), NULL, and XMLTYPE/string metadata
// through text and parameterized reads.
func runOracleXML(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, xmlCreateSQL)
	if err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,XMLTYPE('"+xmlText+"'))"); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", 2, nil); err != nil {
		return err
	}
	if err = readXML(ctx, s.db, "SELECT X FROM "+table+" WHERE ID>=1 ORDER BY ID"); err != nil {
		return err
	}
	return readXML(ctx, s.db, "SELECT X FROM "+table+" WHERE ID>=? ORDER BY ID", 1)
}

func readXML(ctx context.Context, db *sql.DB, query string, args ...any) (err error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	if err = checkXMLMetadata(columns); err != nil {
		return err
	}
	for _, want := range []string{xmlStored, ""} {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("XML row missing")
		}
		var got sql.NullString
		if err = rows.Scan(&got); err != nil {
			return err
		}
		if want == "" {
			if err = expect(!got.Valid, "XML NULL changed"); err != nil {
				return err
			}
			continue
		}
		if err = expect(got.Valid && got.String == want, "XML text changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected XML row")
	}
	return rows.Err()
}

func checkXMLMetadata(columns []*sql.ColumnType) error {
	if len(columns) != 1 {
		return checkFailure("XML metadata missing")
	}
	column := columns[0]
	if column.DatabaseTypeName() != xmlTypeName || column.ScanType() != reflect.TypeFor[string]() {
		return checkFailure("XML metadata mismatch")
	}
	return nil
}
