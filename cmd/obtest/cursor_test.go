package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
)

func TestCursorBatchAssertions(t *testing.T) {
	for _, singleColumn := range []bool{false, true} {
		for _, tc := range []struct {
			name   string
			change func(*cursorBatchFixtureRows)
			wantOK bool
		}{
			{name: "exact-batch", wantOK: true},
			{name: "missing-row", change: func(r *cursorBatchFixtureRows) { r.values = nil }},
			{name: "duplicate-row", change: func(r *cursorBatchFixtureRows) { r.values = append(r.values, r.values[0]) }},
			{name: "additional-row", change: func(r *cursorBatchFixtureRows) {
				row := append([]driver.Value(nil), r.values[0]...)
				row[0] = int64(999)
				r.values = append(r.values, row)
			}},
			{name: "late-fetch-error", change: func(r *cursorBatchFixtureRows) { r.nextErr = errors.New("fixture fetch failure") }},
			{name: "fetch-close-error", change: func(r *cursorBatchFixtureRows) { r.closeErr = errors.New("fixture close failure") }},
			{name: "wrong-identifier", change: func(r *cursorBatchFixtureRows) { r.values[0][0] = int64(-1) }},
			{name: "null-identifier", change: func(r *cursorBatchFixtureRows) { r.values[0][0] = nil }},
			{name: "invalid-identifier", change: func(r *cursorBatchFixtureRows) { r.values[0][0] = "not-an-integer" }},
			{name: "unexpected-width", change: func(r *cursorBatchFixtureRows) { r.width++ }},
			{name: "changed-text", change: func(r *cursorBatchFixtureRows) {
				if r.width == 1 {
					r.values[0][0] = int64(41)
				} else {
					r.values[0][1] = "different"
				}
			}},
		} {
			t.Run(map[bool]string{false: "table-cursor", true: "ref-cursor"}[singleColumn]+"/"+tc.name, func(t *testing.T) {
				id, text := 1, "one"
				values := []driver.Value{int64(1), "one"}
				if singleColumn {
					id, text, values = 42, "", []driver.Value{int64(42)}
				}
				r := &cursorBatchFixtureRows{fixtureRows: fixtureRows{width: len(values), values: [][]driver.Value{values}}}
				if tc.change != nil {
					tc.change(r)
				}
				s := fixtureScenario(t, []fixtureStep{
					{query: "CREATE TABLE OBT_OFFLINE_T (ID INTEGER PRIMARY KEY,V VARCHAR(40))"},
					{query: "cursor batch fixture", rows: r},
					{query: fixtureDropTableSQL},
				})
				result := runScenario(context.Background(), s, func(ctx context.Context, s *scenario) error {
					if _, err := s.table(ctx, "(ID INTEGER PRIMARY KEY,V VARCHAR(40))"); err != nil {
						return err
					}
					rows, err := s.db.QueryContext(ctx, "cursor batch fixture")
					if err != nil {
						return err
					}
					return checkCursorBatch(rows, id, text)
				})
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 || !r.closed {
					t.Fatalf("cursor batch: %+v closed=%t; want %s and cleanup", result, r.closed, want)
				}
			})
		}
	}
}

type cursorBatchFixtureRows struct {
	fixtureRows
	nextErr, closeErr error
	closed            bool
}

func (r *cursorBatchFixtureRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 && r.nextErr != nil {
		return r.nextErr
	}
	return r.fixtureRows.Next(dest)
}

func (r *cursorBatchFixtureRows) Close() error {
	r.closed = true
	return r.closeErr
}
