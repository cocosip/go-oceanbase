package main

import (
	"context"
	"database/sql"

	"github.com/cocosip/go-oceanbase"
)

const countExecutionPath = "execution"

func runRowCounts(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V INTEGER)")
	if err != nil {
		return err
	}
	noChange := int64(1)
	if s.cfg.RowsAffectedMode == oceanbase.ChangedRows {
		noChange = 0
	}
	type command struct {
		query string
		count int64
	}
	operations := []command{
		{"INSERT INTO " + table + " VALUES (1,7)", 1},
		{"UPDATE " + table + " SET V=7 WHERE ID=1", noChange},
		{"UPDATE " + table + " SET V=8 WHERE ID=1", 1},
		{"UPDATE " + table + " SET V=8 WHERE ID=99", 0},
	}
	deleted := int64(1)
	if s.mode == oceanbase.ModeMySQL {
		operations = append(operations,
			command{"INSERT INTO " + table + " VALUES (1,8) ON DUPLICATE KEY UPDATE V=8", noChange},
			command{"INSERT INTO " + table + " VALUES (1,9) ON DUPLICATE KEY UPDATE V=9", 2},
			command{"INSERT INTO " + table + " VALUES (2,9) ON DUPLICATE KEY UPDATE V=9", 1})
		deleted = 2
	}
	operations = append(operations, command{"DELETE FROM " + table, deleted}, command{"DELETE FROM " + table, 0})
	return withSession(ctx, s.db, func(c *sql.Conn) error {
		for _, path := range []string{"exec", countExecutionPath, "batch"} {
			for _, operation := range operations {
				if err := checkRowCountCommand(ctx, c, path, operation.query, operation.count); err != nil {
					return err
				}
			}
		}
		var remaining int
		if err := c.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&remaining); err != nil {
			return err
		}
		return expect(remaining == 0, "row-count scenario left unexpected rows")
	})
}

func checkRowCountCommand(ctx context.Context, c *sql.Conn, path, query string, want int64) (err error) {
	switch path {
	case "exec":
		r, e := c.ExecContext(ctx, query)
		return affected(r, e, want)
	case countExecutionPath:
		var e *oceanbase.Execution
		e, err = oceanbase.Execute(ctx, c, query)
		if e != nil {
			defer closeInto(e, &err)
		}
		if err != nil {
			return err
		}
		if err = e.Close(); err != nil {
			return err
		}
		snapshot := e.Snapshot()
		if err = snapshot.Err; err != nil {
			return err
		}
		if !snapshot.Done || len(snapshot.Results) != 1 {
			return checkFailure("row-count execution has incomplete or extra results")
		}
		r := snapshot.Results[0]
		return expect(r.Kind == oceanbase.ResultCommand && r.RowSetIndex == -1 && r.Complete && r.Err == nil && r.RowsAffectedKnown && r.RowsAffected == want, "execution affected row count mismatch")
	default:
		r, err := oceanbase.ExecBatch(ctx, c, query, [][]any{nil}, oceanbase.BatchOptions{Strategy: oceanbase.BatchSequential})
		if err != nil {
			return err
		}
		if r.Strategy != oceanbase.BatchSequential || len(r.Items) != 1 {
			return checkFailure("row-count batch strategy or item count mismatch")
		}
		item := r.Items[0]
		return expect(item.Started && item.Status == oceanbase.BatchSucceeded && item.Err == nil && item.RowsAffectedKnown && item.RowsAffected == want, "batch affected row count mismatch")
	}
}
