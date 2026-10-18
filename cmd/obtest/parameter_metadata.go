package main

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

type parameterIdentity struct {
	logical int64
	name    string
}

func runParameterMetadata(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,'before')")
	if err = affected(r, err, 1); err != nil {
		return err
	}
	type description struct {
		query string
		slots []parameterIdentity
	}
	queries := []description{{"UPDATE " + table + " SET V=? WHERE ID=?", []parameterIdentity{{1, ""}, {2, ""}}}}
	if s.mode == oceanbase.ModeOracle {
		queries = append(queries, description{"UPDATE " + table + " SET V=:text WHERE ID=:key OR ID=:KEY", []parameterIdentity{{1, "TEXT"}, {2, "KEY"}, {2, "KEY"}}})
	}
	queries = append(queries, description{"SELECT ID,V FROM " + table + " WHERE ID=1", nil})
	if err = withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, q := range queries {
			if err := checkParameterMetadata(ctx, c, s.mode, q.query, q.slots); err != nil {
				return err
			}
			var value string
			if err := c.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value); err != nil {
				return err
			}
			if err := expect(value == "before", "describing parameters changed the seeded row"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 1)
}

func checkParameterMetadata(ctx context.Context, c *sql.Conn, mode oceanbase.Mode, query string, slots []parameterIdentity) (err error) {
	rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{Kind: oceanbase.MetadataStatementParameters, Query: query})
	if err != nil {
		return err
	}
	defer closeInto(rows, &err)
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	if !slices.Equal(columns, strings.Fields("physical_ordinal logical_ordinal name direction data_type nullable length precision scale protocol_type protocol_direction")) {
		return checkFailure("statement parameter metadata columns mismatch")
	}
	n := 0
	for rows.Next() {
		if n >= len(slots) {
			return checkFailure("unexpected statement parameter metadata row")
		}
		if err = checkParameterMetadataRow(rows, mode, int64(n+1), slots[n]); err != nil {
			return err
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return expect(n == len(slots), "statement parameter metadata row count mismatch")
}

func checkParameterMetadataRow(rows *sql.Rows, mode oceanbase.Mode, ordinal int64, slot parameterIdentity) error {
	var physical, logical, protocolType, protocolDirection int64
	var name, direction, dataType sql.NullString
	var nullable, length, precision, scale sql.NullInt64
	if err := rows.Scan(&physical, &logical, &name, &direction, &dataType, &nullable, &length, &precision, &scale, &protocolType, &protocolDirection); err != nil {
		return err
	}
	if physical != ordinal || logical != slot.logical || name.Valid != (slot.name != "") || name.String != slot.name || direction.Valid {
		return checkFailure("statement parameter identity or direction mismatch")
	}
	nullableOK := !nullable.Valid
	if mode == oceanbase.ModeOracle {
		nullableOK = nullable.Valid && (nullable.Int64 == 0 || nullable.Int64 == 1)
	}
	if !nullableOK || protocolType < 0 || protocolType > 255 || protocolDirection < 0 || protocolDirection > 255 || dataType.Valid && dataType.String == "" {
		return checkFailure("statement parameter protocol description mismatch")
	}
	if length.Valid && length.Int64 < 0 || precision.Valid != scale.Valid || precision.Valid && precision.Int64 < 0 || protocolType == 6 && (dataType.Valid || length.Valid || precision.Valid || scale.Valid) {
		return checkFailure("statement parameter size or unknown description mismatch")
	}
	return nil
}
