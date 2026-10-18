package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/cocosip/go-oceanbase"
)

func cursorTable(ctx context.Context, s *scenario) (string, error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(40))")
	if err != nil {
		return "", err
	}
	for _, row := range []struct {
		id    int
		value string
	}{{1, seedOne}, {2, seedTwo}} {
		if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", row.id, row.value); err != nil {
			return "", err
		}
	}
	return table, nil
}

func checkCursorBatch(rows *sql.Rows, id int, value string) (err error) {
	defer closeInto(rows, &err)
	var got int
	var text string
	dest := []any{&got}
	if value != "" {
		dest = append(dest, &text)
	}
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return err
		}
		return checkFailure("cursor row missing")
	}
	if err = rows.Scan(dest...); err != nil {
		return err
	}
	return expect(got == id && text == value, "cursor row mismatch")
}

func runCursor(ctx context.Context, s *scenario) error {
	table, err := cursorTable(ctx, s)
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, mode := range []oceanbase.CursorMode{oceanbase.CursorForward, oceanbase.CursorStatic} {
			if err := fetchCursor(ctx, c, table, mode); err != nil {
				return err
			}
		}
		return c.PingContext(ctx)
	})
}

func fetchCursor(ctx context.Context, c *sql.Conn, table string, mode oceanbase.CursorMode) (err error) {
	cur, err := oceanbase.OpenCursor(ctx, c, "SELECT ID,V FROM "+table+" ORDER BY ID", oceanbase.CursorOptions{Mode: mode})
	if err != nil {
		return err
	}
	defer closeInto(cur, &err)
	for i, value := range []string{seedOne, seedTwo} {
		position := oceanbase.CursorPosition{}
		if mode == oceanbase.CursorStatic {
			position = oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: int64(i + 1)}
		}
		rows, e := cur.Fetch(ctx, position, 1)
		if e != nil {
			return e
		}
		if err = checkCursorBatch(rows, i+1, value); err != nil {
			return err
		}
	}
	return nil
}

func runCursorUpdate(ctx context.Context, s *scenario) (err error) {
	table, err := cursorTable(ctx, s)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	cur, err := oceanbase.OpenCursor(ctx, tx, "SELECT ID,V FROM "+table, oceanbase.CursorOptions{Updatable: true})
	if err != nil {
		return err
	}
	defer closeInto(cur, &err)
	if err = cur.Update(ctx, map[string]any{"ID": 1}, oceanbase.ColumnChange{Name: "V", Value: "updated"}); err != nil {
		return err
	}
	rows, err := cur.Refresh(ctx, map[string]any{"ID": 1})
	if err != nil {
		return err
	}
	if err = checkCursorBatch(rows, 1, "updated"); err != nil {
		return err
	}
	if err = cur.Insert(ctx, oceanbase.ColumnChange{Name: "ID", Value: 3}, oceanbase.ColumnChange{Name: "V", Value: "new"}); err != nil {
		return err
	}
	if err = cur.Delete(ctx, map[string]any{"ID": 2}); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	_, e := cur.Fetch(ctx, oceanbase.CursorPosition{}, 1)
	if err = expect(errors.Is(e, sql.ErrTxDone) || errors.Is(e, oceanbase.ErrResource), "cursor survived its transaction"); err != nil {
		return err
	}
	if err = countRows(ctx, s.db, table, 2); err != nil {
		return err
	}
	var value string
	if err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=1").Scan(&value); err != nil {
		return err
	}
	return expect(value == seedOne, "cursor mutation survived rollback")
}

func runSensitiveCursor(ctx context.Context, s *scenario) error {
	table, err := cursorTable(ctx, s)
	if err != nil {
		return err
	}
	return withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		cur, err := oceanbase.OpenCursor(ctx, c, "SELECT ID,V FROM "+table+" ORDER BY ID", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
		if err != nil {
			return err
		}
		defer closeInto(cur, &err)
		for _, value := range []string{seedChanged, "changed-again"} {
			if _, err = s.db.ExecContext(ctx, "UPDATE "+table+" SET V=? WHERE ID=1", value); err != nil {
				return err
			}
			rows, e := cur.Fetch(ctx, oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
			if e != nil {
				return e
			}
			if err = checkCursorBatch(rows, 1, value); err != nil {
				return err
			}
		}
		return nil
	})
}
