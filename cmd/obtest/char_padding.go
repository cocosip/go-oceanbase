package main

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/cocosip/go-oceanbase"
)

const (
	charPaddingCase      = "char-padding"
	charPaddingInsertSQL = "INSERT INTO %s VALUES (?,?,?)"
	charPaddingSelectSQL = "SELECT C,V FROM %s WHERE ID>=1 ORDER BY ID"
	charPaddingBoundSQL  = "SELECT C,V FROM %s WHERE ID>=? ORDER BY ID"
	charPaddingBytesSQL  = "SELECT C,V FROM %s WHERE ID=1"
	charPaddingMatchSQL  = "SELECT COUNT(*) FROM %s WHERE C='ab'"
	charPaddingTrimSQL   = "SELECT COUNT(*) FROM %s WHERE RTRIM(C)='ab'"
	charPaddingCountSQL  = "SELECT COUNT(*) FROM %s WHERE ID>=90"
	charPaddingFloat     = "ab"
	charPaddingFull      = "abcde"
)

// charPaddingProfile records the mode-specific CHAR semantics: Oracle returns a
// padded CHAR value while MySQL returns the value without trailing blanks, and
// the over-length rejection code differs.
type charPaddingProfile struct {
	ddl       string
	padded    string
	typeNameC string
	typeNameV string
	overflow  uint32
}

func charPaddingProfileFor(mode oceanbase.Mode) charPaddingProfile {
	if mode == oceanbase.ModeOracle {
		return charPaddingProfile{
			ddl: "(ID INTEGER PRIMARY KEY,C CHAR(5),V VARCHAR2(10))", padded: "ab   ",
			typeNameC: extendedCharType, typeNameV: typeNameVarchar2, overflow: 12899,
		}
	}
	return charPaddingProfile{
		ddl: "(ID INTEGER PRIMARY KEY,C CHAR(5),V VARCHAR(10))", padded: charPaddingFloat,
		typeNameC: extendedCharType, typeNameV: typeNameVarchar, overflow: 1406,
	}
}

// runCharPadding verifies that a fixed-length CHAR column is returned exactly as
// the server stores it (Oracle pads to the declared length, MySQL trims
// trailing blanks), that blank padding still compares equal to the unpadded
// value, that VARCHAR values are never padded and that over-length input is
// rejected with the mode-specific 22001-family code without storing a row.
func runCharPadding(ctx context.Context, s *scenario) error {
	profile := charPaddingProfileFor(s.mode)
	table, err := s.table(ctx, profile.ddl)
	if err != nil {
		return err
	}
	for id, value := range []any{charPaddingFloat, charPaddingFull, nil} {
		if _, err = s.db.ExecContext(ctx, fmt.Sprintf(charPaddingInsertSQL, table), id+1, value, value); err != nil {
			return err
		}
	}
	if err = readCharPadding(ctx, s.db, table, profile, false); err != nil {
		return err
	}
	if err = readCharPadding(ctx, s.db, table, profile, true); err != nil {
		return err
	}
	if err = readCharPaddingBytes(ctx, s.db, table, profile); err != nil {
		return err
	}
	for _, tc := range []struct {
		query string
		label string
	}{
		{fmt.Sprintf(charPaddingMatchSQL, table), "CHAR blank padding no longer compares equal"},
		{fmt.Sprintf(charPaddingTrimSQL, table), "RTRIM of a padded CHAR changed"},
	} {
		var matched int
		if err = s.db.QueryRowContext(ctx, tc.query).Scan(&matched); err != nil {
			return err
		}
		if err = expect(matched == 1, tc.label); err != nil {
			return err
		}
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(charPaddingInsertSQL, table), 90, "abcdef", "abcdefghijk"); !onlyServerErrorCode(err, profile.overflow) {
		return checkFailure("over-length CHAR was not rejected")
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(charPaddingCountSQL, table)).Scan(&stored); err != nil {
		return err
	}
	return expect(stored == 0, "rejected over-length CHAR left a stored row")
}

func readCharPadding(ctx context.Context, db *sql.DB, table string, profile charPaddingProfile, parameterized bool) (err error) {
	query, args := fmt.Sprintf(charPaddingSelectSQL, table), []any(nil)
	if parameterized {
		query, args = fmt.Sprintf(charPaddingBoundSQL, table), []any{1}
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
	if err = checkCharPaddingMetadata(columns, profile); err != nil {
		return err
	}
	want := []struct{ c, v sql.NullString }{
		{charPaddingString(profile.padded), charPaddingString(charPaddingFloat)},
		{charPaddingString(charPaddingFull), charPaddingString(charPaddingFull)},
		{},
	}
	for _, expected := range want {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("CHAR row missing")
		}
		var c, v sql.NullString
		if err = rows.Scan(&c, &v); err != nil {
			return err
		}
		if err = expect(c == expected.c && v == expected.v, "CHAR/VARCHAR text changed"); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected CHAR row")
	}
	return rows.Err()
}

func readCharPaddingBytes(ctx context.Context, db *sql.DB, table string, profile charPaddingProfile) error {
	var c, v []byte
	if err := db.QueryRowContext(ctx, fmt.Sprintf(charPaddingBytesSQL, table)).Scan(&c, &v); err != nil {
		return err
	}
	return expect(string(c) == profile.padded && string(v) == charPaddingFloat, "CHAR bytes changed")
}

func checkCharPaddingMetadata(columns []*sql.ColumnType, profile charPaddingProfile) error {
	if len(columns) != 2 {
		return checkFailure("CHAR metadata missing")
	}
	if columns[0].DatabaseTypeName() != profile.typeNameC || columns[1].DatabaseTypeName() != profile.typeNameV {
		return checkFailure("CHAR metadata changed")
	}
	for _, column := range columns {
		if column.ScanType() != reflect.TypeFor[string]() {
			return checkFailure("CHAR scan type changed")
		}
		if length, ok := column.Length(); !ok || (length != 5 && length != 10) {
			return checkFailure("CHAR declared length changed")
		}
	}
	return nil
}

func charPaddingString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}
