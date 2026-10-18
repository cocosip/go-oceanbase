package main

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase"
)

func runBatchRewrite(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	r, err := oceanbase.ExecBatch(ctx, s.db, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{1, seedOne}, {2, seedTwo}, {3, unicodeValue}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite})
	s.observe("effective_strategy", strategyName(r.Strategy))
	if err != nil {
		return err
	}
	if err = expect(r.Strategy == oceanbase.BatchRewrite, "batch rewrite fell back; native rewrite not verified"); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 3)
}

func runBatchArray(ctx context.Context, s *scenario) (err error) {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackInto(tx, &err)
	r, e := oceanbase.ExecBatch(ctx, tx, "INSERT INTO "+table+" VALUES (?,?)", [][]any{{1, seedOne}, {1, "duplicate"}, {2, seedTwo}}, oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true, ChunkRows: 3})
	s.observe("effective_strategy", strategyName(r.Strategy))
	var batchErr *oceanbase.BatchError
	if r.Strategy != oceanbase.BatchArrayBinding || !errors.As(e, &batchErr) || batchErr.Index != 1 || len(r.Items) != 3 {
		return checkFailure("native array binding strategy or error attribution mismatch")
	}
	if err = expect(r.Items[0].Status == oceanbase.BatchSucceeded && r.Items[1].Status == oceanbase.BatchFailed && r.Items[2].Status == oceanbase.BatchSucceeded, "native array binding input statuses mismatch"); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		return err
	}
	if err = expect(count == 2, "native array binding did not write its successful inputs"); err != nil {
		return err
	}
	var value string
	if err = tx.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=2").Scan(&value); err != nil {
		return err
	}
	if err = expect(value == seedTwo, "native array binding input content mismatch"); err != nil {
		return err
	}
	if err = tx.Rollback(); err != nil {
		return err
	}
	return countRows(ctx, s.db, table, 0)
}

func runOptimized(ctx context.Context, s *scenario) error {
	table, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(100))")
	if err != nil {
		return err
	}
	s.db.SetMaxOpenConns(1)
	values := []any{unicodeValue, nil, seedChanged}
	for i, value := range values {
		r, e := s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", i, value)
		if e = affected(r, e, 1); e != nil {
			return e
		}
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?,?)", 0, "duplicate"); err == nil {
		return checkFailure("optimized execution lost duplicate error")
	}
	for i, value := range values {
		var got sql.NullString
		if err = s.db.QueryRowContext(ctx, "SELECT V FROM "+table+" WHERE ID=?", i).Scan(&got); err != nil {
			return err
		}
		if err = expect(value == nil && !got.Valid || value != nil && got.Valid && got.String == value, "optimized execution changed bound value"); err != nil {
			return err
		}
	}
	return countRows(ctx, s.db, table, 3)
}

func runFullReset(ctx context.Context, s *scenario) (err error) {
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	statement, err := s.db.PrepareContext(ctx, s.scalar("SELECT ?"))
	if err != nil {
		return err
	}
	defer closeInto(statement, &err)
	for _, want := range []int{1, 2, 3} {
		var got int
		if err = statement.QueryRowContext(ctx, want).Scan(&got); err != nil {
			return err
		}
		if err = expect(got == want, "prepared statement failed across full reset"); err != nil {
			return err
		}
	}
	return nil
}

func runMultiResults(ctx context.Context, s *scenario) error {
	return withSession(ctx, s.db, func(c *sql.Conn) (err error) {
		rows, err := c.QueryContext(ctx, "SELECT 1 AS N; SELECT 2 AS N")
		if err != nil {
			return err
		}
		defer closeInto(rows, &err)
		for i := 1; i <= 2; i++ {
			var got int
			if !rows.Next() {
				return errors.Join(checkFailure("multi-result row missing"), rows.Err())
			}
			if err = rows.Scan(&got); err != nil {
				return err
			}
			if err = expect(got == i, "multi-result order or value mismatch"); err != nil {
				return err
			}
			if rows.Next() {
				return checkFailure("multi-result contained an extra row")
			}
			if err = rows.Err(); err != nil {
				return err
			}
			if i == 1 && !rows.NextResultSet() {
				return errors.Join(checkFailure("second result set missing"), rows.Err())
			}
		}
		if rows.NextResultSet() {
			return checkFailure("unexpected additional result set")
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		return c.PingContext(ctx)
	})
}

func runTrace(ctx context.Context, s *scenario) (err error) {
	var events atomic.Int64
	cfg := s.cfg
	cfg.Diagnostics.Request = func(_ context.Context, d oceanbase.RequestDiagnostic) {
		if d.Operation == "query" && !d.Failed {
			events.Add(1)
		}
	}
	db, err := openDatabase(cfg)
	if err != nil {
		return err
	}
	defer closeInto(db, &err)
	for _, info := range []oceanbase.TraceInfo{{Module: "obtest", Action: "first"}, {}} {
		before := events.Load()
		var got int
		if err = db.QueryRowContext(oceanbase.WithTraceInfo(ctx, info), s.scalar("SELECT 42")).Scan(&got); err != nil {
			return err
		}
		if err = expect(got == 42, "trace context changed query result"); err != nil {
			return err
		}
		if err = expect(events.Load() > before, "SQL request diagnostic missing"); err != nil {
			return err
		}
	}
	s.observe("trace_scope", "client diagnostics only; server trace visibility requires independent observation")
	return expect(events.Load() > 0, "client request diagnostics missing")
}
