package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataTypeInfoCase    = "metadata-type-info"
	typeInfoMySQLCount      = 29
	typeInfoNumeric         = "numeric"
	typeInfoCharacter       = "character"
	typeInfoBinary          = "binary"
	typeInfoDatetime        = "datetime"
	typeInfoInterval        = "interval"
	typeInfoLOB             = "lob"
	typeInfoRowID           = "rowid"
	typeInfoBit             = "BIT"
	typeInfoTimestamp       = "TIMESTAMP"
	typeInfoLength          = "length"
	typeInfoFractional      = "fractional_seconds_precision"
	typeInfoTimestampPrefix = "TIMESTAMP '"
	typeInfoOracleCount     = 19
)

// runMetadataTypeInfo verifies the built-in type dictionary: ten documented
// columns, one row per built-in SQL type family, documented limits for a curated
// subset, mode-correct auto-increment flags, and no duplicate or unnamed rows.
func runMetadataTypeInfo(ctx context.Context, s *scenario) error {
	rows, err := metadataTypeInfo(ctx, s.db)
	if err != nil {
		return err
	}
	wantCount := typeInfoMySQLCount
	if s.mode == oceanbase.ModeOracle {
		wantCount = typeInfoOracleCount
	}
	if err = expect(len(rows) == wantCount, "built-in type row count changed"); err != nil {
		return err
	}
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if err = expect(row.name != "" && row.category != "", "built-in type row without name or category"); err != nil {
			return err
		}
		if seen[row.name] {
			return checkFailure("built-in type row repeated")
		}
		seen[row.name] = true
		if err = expect(validTypeCategory(row.category), "built-in type category changed"); err != nil {
			return err
		}
	}
	for _, expected := range typeInfoExpectations(s.mode) {
		row, ok := metadataTypeRow(rows, expected.name)
		if !ok {
			return checkFailure("built-in type row missing")
		}
		if err = expect(row.matches(expected), fmt.Sprintf("built-in type %q limits changed: got %+v want %+v", expected.name, row, expected)); err != nil {
			return err
		}
	}
	return nil
}

type metadataTypeInfoRow struct {
	name     string
	category string
	width    typeInfoLimit
	minimum  typeInfoLimit
	maximum  typeInfoLimit
	radix    typeInfoLimit
	prefix   typeInfoLimit
	suffix   typeInfoLimit
	params   typeInfoLimit
	auto     typeInfoLimit
}

type typeInfoLimit struct {
	valid bool
	text  string
}

func (r metadataTypeInfoRow) matches(want metadataTypeInfoRow) bool {
	return r.category == want.category && r.width.equal(want.width) && r.minimum.equal(want.minimum) &&
		r.maximum.equal(want.maximum) && r.radix.equal(want.radix) && r.prefix.equal(want.prefix) &&
		r.suffix.equal(want.suffix) && r.params.equal(want.params) && r.auto.equal(want.auto)
}

func (limit typeInfoLimit) equal(other typeInfoLimit) bool {
	return limit.valid == other.valid && limit.text == other.text
}

func metadataTypeInfo(ctx context.Context, db *sql.DB) (rows []metadataTypeInfoRow, err error) {
	err = withSession(ctx, db, func(c *sql.Conn) error {
		result, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: oceanbase.MetadataTypeInfo})
		if err != nil {
			return err
		}
		defer closeInto(result, &err)
		for result.Next() {
			var row metadataTypeInfoRow
			var width, minimum, maximum, radix, prefix, suffix, params, auto sql.NullString
			if err = result.Scan(&row.name, &row.category, &width, &minimum, &maximum, &radix, &prefix, &suffix, &params, &auto); err != nil {
				return err
			}
			row.width, row.minimum, row.maximum = limit(width), limit(minimum), limit(maximum)
			row.radix, row.prefix, row.suffix = limit(radix), limit(prefix), limit(suffix)
			row.params, row.auto = limit(params), limit(auto)
			rows = append(rows, row)
		}
		return result.Err()
	})
	return rows, err
}

func limit(value sql.NullString) typeInfoLimit {
	return typeInfoLimit{valid: value.Valid, text: value.String}
}

func metadataTypeRow(rows []metadataTypeInfoRow, name string) (metadataTypeInfoRow, bool) {
	for _, row := range rows {
		if row.name == name {
			return row, true
		}
	}
	return metadataTypeInfoRow{}, false
}

func validTypeCategory(category string) bool {
	switch category {
	case typeInfoNumeric, typeInfoCharacter, typeInfoBinary, typeInfoDatetime, typeInfoInterval, typeInfoLOB, typeInfoRowID:
		return true
	}
	return false
}

func typeInfoExpectations(mode oceanbase.Mode) []metadataTypeInfoRow {
	value := func(text string) typeInfoLimit { return typeInfoLimit{valid: true, text: text} }
	if mode == oceanbase.ModeMySQL {
		return []metadataTypeInfoRow{
			{name: "INT", category: typeInfoNumeric, width: value("10"), minimum: value("0"), maximum: value("0"), radix: value("10"), auto: value("1")},
			{name: "DECIMAL", category: typeInfoNumeric, width: value("65"), minimum: value("0"), maximum: value("30"), radix: value("10"), params: value("precision,scale"), auto: value("0")},
			{name: "VARCHAR", category: typeInfoCharacter, prefix: value("'"), suffix: value("'"), params: value(typeInfoLength), auto: value("0")},
			{name: typeInfoBit, category: typeInfoBinary, width: value("64"), radix: value("2"), prefix: value("b'"), suffix: value("'"), params: value(typeInfoLength), auto: value("0")},
			{name: "DATETIME", category: typeInfoDatetime, minimum: value("0"), maximum: value("6"), prefix: value("'"), suffix: value("'"), params: value(typeInfoFractional), auto: value("0")},
		}
	}
	return []metadataTypeInfoRow{
		{name: "NUMBER", category: typeInfoNumeric, width: value("38"), minimum: value("-84"), maximum: value("127"), radix: value("10"), params: value("precision,scale")},
		{name: typeNameVarchar2, category: typeInfoCharacter, prefix: value("'"), suffix: value("'"), params: value(typeInfoLength)},
		{name: typeInfoTimestamp, category: typeInfoDatetime, minimum: value("0"), maximum: value("9"), prefix: value(typeInfoTimestampPrefix), suffix: value("'"), params: value(typeInfoFractional)},
		{name: "ROWID", category: typeInfoRowID, prefix: value("'"), suffix: value("'")},
		{name: blobType, category: typeInfoLOB},
	}
}
