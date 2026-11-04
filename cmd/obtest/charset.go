package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	charsetCase     = "mysql-charset"
	charsetResetDDL = "SET character_set_results=utf8mb4"
	charsetColumn   = "VARCHAR(200) CHARACTER SET %s COLLATE %s_bin"
)

// charsetSeed is a frozen server-verified mapping: the UTF-8 text, the exact
// legacy bytes the server stores for it, and the column character set.
type charsetSeed struct {
	name string
	text string
	raw  string
}

func charsetSeeds() []charsetSeed {
	return []charsetSeed{
		{"cp850", "café", "63616682"},
		{"hp8", "ÄÖé", "d8dac5"},
		{"macroman", "ÄéΩ", "808ebd"},
		{"swe7", "ÅÄÖåäö", "5d5b5c7d7b7c"},
		{"ujis", "日本語ｱ〜˘\ue000\ue3ac", "c6fccbdcb8ec8eb1a1c18fa2aff5a18ff5a1"},
		{"cp932", "日本語ｱ～①髙\ue000", "93fa967b8ceab181608740fbfcf040"},
		{"eucjpms", "日本語ｱ～①髙\ue000\ue3ac", "c6fccbdcb8ec8eb1a1c1ada18ff4fbf5a18ff5a1"},
	}
}

// runMySQLCharset verifies the strict legacy charsets U06 added: text bound in a
// utf8mb4 session is transcoded into the column charset, the frozen bytes match,
// and forcing `character_set_results` to the legacy charset still decodes both
// the text and its HEX into the original UTF-8 values.
func runMySQLCharset(ctx context.Context, s *scenario) error {
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, seed := range charsetSeeds() {
			if err := runCharsetColumn(ctx, s, c, seed); err != nil {
				return err
			}
		}
		return nil
	})
}

func runCharsetColumn(ctx context.Context, s *scenario, c *sql.Conn, seed charsetSeed) error {
	encoded, err := hex.DecodeString(seed.raw)
	if err != nil {
		return err
	}
	table, err := s.tableNamed(ctx, "CS_"+seed.name, "(ID INTEGER PRIMARY KEY,V "+fmt.Sprintf(charsetColumn, seed.name, seed.name)+")")
	if err != nil {
		return err
	}
	for _, insert := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO " + table + " VALUES (?,?)", []any{1, seed.text}},
		{"INSERT INTO " + table + " VALUES (?,?)", []any{2, nil}},
		{"INSERT INTO " + table + " VALUES (3,CONVERT(CAST(? AS BINARY) USING " + seed.name + "))", []any{encoded}},
	} {
		if _, err = c.ExecContext(ctx, insert.query, insert.args...); err != nil {
			return err
		}
	}
	if _, err = c.ExecContext(ctx, "SET character_set_results="+seed.name); err != nil {
		return err
	}
	if err = readCharsetColumn(ctx, c, table, seed, false); err != nil {
		return err
	}
	if err = readCharsetColumn(ctx, c, table, seed, true); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, charsetResetDDL)
	return err
}

func readCharsetColumn(ctx context.Context, c *sql.Conn, table string, seed charsetSeed, parameterized bool) (err error) {
	query := "SELECT V,HEX(V) FROM " + table + " WHERE ID>=1 ORDER BY ID"
	var args []any
	if parameterized {
		query, args = "SELECT V,HEX(V) FROM "+table+" WHERE ID>=? ORDER BY ID", []any{1}
	}
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	for _, want := range []struct{ text, raw sql.NullString }{
		{numberString(seed.text), numberString(seed.raw)},
		{},
		{numberString(seed.text), numberString(seed.raw)},
	} {
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("charset row missing for " + seed.name)
		}
		var text, raw sql.NullString
		if err = rows.Scan(&text, &raw); err != nil {
			return err
		}
		if err = expect(text == want.text && equalFoldNull(raw, want.raw), "charset text changed for "+seed.name); err != nil {
			return err
		}
	}
	if rows.Next() {
		return checkFailure("unexpected charset row")
	}
	return rows.Err()
}

func equalFoldNull(got, want sql.NullString) bool {
	if got.Valid != want.Valid {
		return false
	}
	return !got.Valid || strings.EqualFold(got.String, want.String)
}
