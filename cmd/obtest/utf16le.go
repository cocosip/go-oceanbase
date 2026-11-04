package main

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
)

const (
	utf16leCase       = "mysql-utf16le"
	utf16leBoundSQL   = "SELECT CONVERT(? USING utf16le),HEX(CONVERT(? USING utf16le))"
	utf16leLiteralSQL = "SELECT CONVERT('中😀' USING utf16le),HEX(CONVERT('中😀' USING utf16le))"
)

// utf16leSeed is one UTF-8 text and the exact UTF-16LE bytes the server returns
// for it, including surrogate pairs for non-BMP runes.
type utf16leSeed struct {
	text string
	hex  string
}

func utf16leSeeds() []utf16leSeed {
	return []utf16leSeed{
		{"中😀", "2D4E3DD800DE"},
		{"", ""},
		{"\ufffd", "FDFF"},
		{"中文中文", "2D4E87652D4E8765"},
	}
}

// runMySQLUTF16LE verifies the utf16le result charset G01 covers: converted
// values decode back to the original UTF-8 text, the stored bytes are the exact
// little-endian UTF-16 encoding (surrogate pairs included) and metadata stays a
// VARCHAR/string column.
func runMySQLUTF16LE(ctx context.Context, s *scenario) error {
	for _, seed := range utf16leSeeds() {
		var text, hexed string
		if err := s.db.QueryRowContext(ctx, utf16leBoundSQL, seed.text, seed.text).Scan(&text, &hexed); err != nil {
			return err
		}
		if err := expect(text == seed.text && strings.EqualFold(hexed, seed.hex), "UTF-16LE bound result changed"); err != nil {
			return err
		}
	}
	return readUTF16LELiteral(ctx, s.db, utf16leSeeds()[0])
}

func readUTF16LELiteral(ctx context.Context, db *sql.DB, seed utf16leSeed) (err error) {
	rows, err := db.QueryContext(ctx, utf16leLiteralSQL)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	columns, err := rows.ColumnTypes()
	if err != nil {
		return err
	}
	if len(columns) != 2 || columns[0].DatabaseTypeName() != typeNameVarchar || columns[0].ScanType() != reflect.TypeFor[string]() {
		return checkFailure("UTF-16LE metadata changed")
	}
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return checkFailure("UTF-16LE literal row missing")
	}
	var text, hexed string
	if err = rows.Scan(&text, &hexed); err != nil {
		return err
	}
	if err = expect(text == seed.text && strings.EqualFold(hexed, seed.hex), "UTF-16LE literal result changed"); err != nil {
		return err
	}
	if rows.Next() {
		return checkFailure("unexpected UTF-16LE literal row")
	}
	return rows.Err()
}
