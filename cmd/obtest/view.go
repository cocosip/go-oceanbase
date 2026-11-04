package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase"
)

const (
	viewCase        = "views"
	viewTableSuffix = "VIEWT"
	viewSuffix      = "VIEWV"
	viewInsert      = "INSERT INTO %s (ID,V) VALUES (?,?)"
	viewUpdate      = "UPDATE %s SET V=? WHERE ID=?"
	viewDelete      = "DELETE FROM %s WHERE ID=?"
	viewSelect      = "SELECT ID,V FROM %s ORDER BY ID"
	viewCount       = "SELECT COUNT(*) FROM %s"
	viewColumnCount = 2
)

// runViews verifies view semantics on the target: a simple view is updatable
// through the driver, the view predicate filters only reads, and the dictionary
// reports the view and its columns like a table.
func runViews(ctx context.Context, s *scenario) error {
	definition := "(ID INTEGER PRIMARY KEY,V VARCHAR(10))"
	if s.mode == oceanbase.ModeOracle {
		definition = "(ID INTEGER PRIMARY KEY,V VARCHAR2(10))"
	}
	table, err := s.tableNamed(ctx, viewTableSuffix, definition)
	if err != nil {
		return err
	}
	view, err := s.create(ctx, "VIEW", viewSuffix, "AS SELECT ID,V FROM "+table+" WHERE ID>=1")
	if err != nil {
		return err
	}
	for _, step := range []struct {
		query string
		args  []any
	}{
		{fmt.Sprintf(viewInsert, view), []any{2, "b"}},
		{fmt.Sprintf(viewUpdate, view), []any{"c", 2}},
		{fmt.Sprintf(viewInsert, view), []any{0, "z"}},
	} {
		if _, err = s.db.ExecContext(ctx, step.query, step.args...); err != nil {
			return err
		}
	}
	var base [][2]string
	if base, err = readViewRows(ctx, s.db, fmt.Sprintf(viewSelect, table)); err != nil {
		return err
	}
	if err = expectRows(base, [][2]string{{"0", "z"}, {"2", "c"}}, "base table rows changed after view DML"); err != nil {
		return err
	}
	var visible [][2]string
	if visible, err = readViewRows(ctx, s.db, fmt.Sprintf(viewSelect, view)); err != nil {
		return err
	}
	if err = expectRows(visible, [][2]string{{"2", "c"}}, "view predicate did not filter reads"); err != nil {
		return err
	}
	if _, err = s.db.ExecContext(ctx, fmt.Sprintf(viewDelete, view), 2); err != nil {
		return err
	}
	var stored int
	if err = s.db.QueryRowContext(ctx, fmt.Sprintf(viewCount, table)).Scan(&stored); err != nil {
		return err
	}
	if err = expect(stored == 1, "delete through the view left another row"); err != nil {
		return err
	}
	if err = checkViewMetadata(ctx, s.db, view); err != nil {
		return err
	}
	return checkViewColumns(ctx, s.db, view)
}

func readViewRows(ctx context.Context, db *sql.DB, query string) (rows [][2]string, err error) {
	result, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer closeInto(result, &err)
	for result.Next() {
		var id string
		var text string
		if err = result.Scan(&id, &text); err != nil {
			return nil, err
		}
		rows = append(rows, [2]string{id, text})
	}
	return rows, result.Err()
}

func expectRows(got, want [][2]string, message string) error {
	if len(got) != len(want) {
		return checkFailure(message)
	}
	for i := range want {
		if got[i] != want[i] {
			return checkFailure(message)
		}
	}
	return nil
}

func checkViewMetadata(ctx context.Context, db *sql.DB, view string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataTables,
			NamePattern: exactPattern(view),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		if !rows.Next() {
			if err = rows.Err(); err != nil {
				return err
			}
			return checkFailure("view row missing from the table dictionary")
		}
		var catalog, schema, name, kind, remarks sql.NullString
		if err = rows.Scan(&catalog, &schema, &name, &kind, &remarks); err != nil {
			return err
		}
		if err = expect(strings.EqualFold(name.String, view) && strings.EqualFold(kind.String, "VIEW"), "view dictionary type changed"); err != nil {
			return err
		}
		if rows.Next() {
			return checkFailure("unexpected view dictionary row")
		}
		return rows.Err()
	})
}

func checkViewColumns(ctx context.Context, db *sql.DB, view string) (err error) {
	return withSession(ctx, db, func(c *sql.Conn) error {
		rows, err := oceanbase.Metadata(ctx, c, oceanbase.MetadataFilter{
			Kind:        oceanbase.MetadataColumns,
			NamePattern: exactPattern(view),
		})
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		index := 0
		for rows.Next() {
			var catalog, schema, table, name, typeName, defaultValue, remarks, owner sql.NullString
			var ordinal, length, precision, scale, nullable, autoIncrement, generated sql.NullInt64
			if err = rows.Scan(&catalog, &schema, &table, &name, &ordinal, &typeName, &length, &precision, &scale,
				&nullable, &defaultValue, &remarks, &owner, &autoIncrement, &generated); err != nil {
				return err
			}
			if err = expect(typeName.String != "", "view column type missing"); err != nil {
				return err
			}
			index++
		}
		if err = rows.Err(); err != nil {
			return err
		}
		return expect(index == viewColumnCount, "view column count changed")
	})
}
