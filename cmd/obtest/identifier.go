package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

const (
	identifierCase        = "identifier-length"
	identifierColumnLen   = 128
	identifierMySQLTable  = 64
	identifierOracleTable = 128
	identifierMySQLCode   = 1059
	identifierOracleCode  = 972
)

// runIdentifierLength verifies the identifier boundary of both modes: the driver
// passes maximum-length table and column names through DDL, DML and metadata
// unchanged, and one character beyond the limit is rejected by the server with
// its documented code.
func runIdentifierLength(ctx context.Context, s *scenario) error {
	tableLimit := identifierMySQLTable
	rejectedCode := uint32(identifierMySQLCode)
	if s.mode == oceanbase.ModeOracle {
		tableLimit = identifierOracleTable
		rejectedCode = identifierOracleCode
	}
	column := identifierColumn()
	table, err := s.tableNamed(ctx, identifierSuffix(tableLimit, s.prefix), identifierDDL(column))
	if err != nil {
		return err
	}
	if len(table) != tableLimit {
		return checkFailure("maximum-length table name changed")
	}
	tooLong := identifierTooLong(s.prefix, tableLimit)
	if _, err = s.db.ExecContext(ctx, "CREATE TABLE "+tooLong+" (ID INTEGER PRIMARY KEY)"); err == nil {
		_, err = s.db.ExecContext(ctx, "DROP TABLE "+tooLong)
		if err != nil {
			return err
		}
		return checkFailure("over-long table name was accepted")
	} else if !onlyServerErrorCode(err, rejectedCode) {
		return err
	}
	tooLongColumn := strings.Repeat("X", identifierColumnLen+1)
	if _, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD "+tooLongColumn+" INTEGER"); err == nil {
		_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" DROP COLUMN "+tooLongColumn)
		if err != nil {
			return err
		}
		return checkFailure("over-long column name was accepted")
	} else if !onlyServerErrorCode(err, rejectedCode) {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(identifierInsert, table, column), 1, 42); err != nil {
		return err
	}
	var id, value int64
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(identifierSelect, column, table)).Scan(&id, &value); err != nil {
		return err
	}
	if err = expect(id == 1 && value == 42, "long-identifier row changed"); err != nil {
		return err
	}
	if err = checkIdentifierTable(ctx, s.db, table, s.mode); err != nil {
		return err
	}
	return checkIdentifierColumn(ctx, s.db, table, column)
}

func identifierSuffix(limit int, prefix string) string {
	return strings.Repeat("L", limit-len(prefix)-1)
}

func identifierTooLong(prefix string, limit int) string {
	return prefix + "_" + strings.Repeat("T", limit-len(prefix))
}

func identifierColumn() string {
	return "C" + strings.Repeat("C", identifierColumnLen-1)
}

func identifierDDL(column string) string {
	return "(ID INTEGER PRIMARY KEY," + column + " INTEGER)"
}

const (
	identifierInsert = "INSERT INTO %s (ID,%s) VALUES (?,?)"
	identifierSelect = "SELECT ID,%s FROM %s"
)

func checkIdentifierTable(ctx context.Context, db *sql.DB, table string, mode oceanbase.Mode) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataTables,
			NamePattern: exactPattern(table),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("long table name missing from the dictionary")
		}
		var catalog, schema, name, kind, remarks sql.NullString
		if err = rows.Scan(&catalog, &schema, &name, &kind, &remarks); err != nil {
			return err
		}
		// OceanBase MySQL mode may fold table identifiers (lower_case_table_names=1), so the
		// dictionary can return a complete case-folded name; truncation still fails the length check.
		got := name.String
		matched := got == table || mode == oceanbase.ModeMySQL && strings.EqualFold(got, table)
		if err = expect(matched && len(got) == len(table), "long table name was truncated in the dictionary"); err != nil {
			return err
		}
		if rows.Next() {
			return checkFailure("unexpected row for the long table name")
		}
		return rows.Err()
	})
}

func checkIdentifierColumn(ctx context.Context, db *sql.DB, table, column string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataColumns,
			NamePattern: exactPattern(table),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		count, matched := 0, false
		for rows.Next() {
			var catalog, schema, gotTable, name, typeName, defaultValue, remarks, owner sql.NullString
			var ordinal, length, precision, scale, nullable, autoIncrement, generated sql.NullInt64
			if err = rows.Scan(&catalog, &schema, &gotTable, &name, &ordinal, &typeName, &length, &precision, &scale,
				&nullable, &defaultValue, &remarks, &owner, &autoIncrement, &generated); err != nil {
				return err
			}
			if name.String == column {
				matched = true
			}
			count++
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if err = expect(count == 2, "long-identifier column count changed"); err != nil {
			return err
		}
		return expect(matched, "long column name was truncated in the dictionary")
	})
}
