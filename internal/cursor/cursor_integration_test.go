//go:build integration

package cursor_test

import (
	sql "database/sql"
	errors "errors"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestCursorUpdates(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestCursorUpdates(t, p, oceanbase.CursorForward) })
		if p.Mode == oceanbase.ModeOracle {
			t.Run(p.Name+"-sensitive", func(t *testing.T) { integrationTestCursorUpdates(t, p, oceanbase.CursorSensitive) })
		}
	}
}

func TestSensitiveCursorRefresh(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeOracle {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			db, table := integrationCursorFixture(t, p)
			c, err := db.Conn(integrationtest.TestContext(t))
			if err != nil {
				integrationtest.FailOperation(t, "pin sensitive session", err)
			}
			defer func() { _ = c.Close() }()
			t.Run("ordinal-order-and-duplicates", func(t *testing.T) { integrationTestSensitiveOrdinalProjection(t, c, table) })
			for _, projection := range []string{"ID,V,B", "*", "x.*", "ID AS RENAMED,V,B"} {
				t.Run(projection, func(t *testing.T) {
					query := "SELECT " + projection + " FROM " + table + " x WHERE ID=:id ORDER BY ID"
					cur, err := oceanbase.OpenCursor(integrationtest.TestContext(t), c, query, oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive}, sql.Named("id", 1))
					if err != nil {
						integrationtest.FailOperation(t, "open sensitive projection", err)
					}
					defer func() { _ = cur.Close() }()
					for _, value := range []string{"fresh-one", "fresh-two"} {
						if _, err = db.ExecContext(integrationtest.TestContext(t), "UPDATE "+table+" SET V=? WHERE ID=1", value); err != nil {
							integrationtest.FailOperation(t, "update from another session", err)
						}
						rows, err := cur.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1)
						if err != nil {
							integrationtest.FailOperation(t, "refresh sensitive projection", err)
						}
						integrationCheckCursorRow(t, rows, 1, value, true)
						if columns, e := rows.Columns(); e != nil || len(columns) != 3 {
							t.Fatal("hidden ROWID leaked", columns, e)
						}
						if err = rows.Close(); err != nil {
							integrationtest.FailOperation(t, "close sensitive projection", err)
						}
					}
				})
			}
			cur, err := oceanbase.OpenCursor(integrationtest.TestContext(t), c, "SELECT V FROM "+table+" WHERE ID=1", oceanbase.CursorOptions{Mode: oceanbase.CursorSensitive})
			if err != nil {
				integrationtest.FailOperation(t, "open sensitive projection without primary key", err)
			}
			defer func() { _ = cur.Close() }()
			if _, err = db.ExecContext(integrationtest.TestContext(t), "DELETE FROM "+table+" WHERE ID=1"); err != nil {
				integrationtest.FailOperation(t, "delete sensitive row", err)
			}
			if rows, err := cur.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{Origin: oceanbase.CursorAbsolute, Offset: 1}, 1); !errors.Is(err, sql.ErrNoRows) {
				if rows != nil {
					_ = rows.Close()
				}
				t.Fatal("deleted sensitive row returned stale data", err)
			}
			if _, err = cur.Fetch(integrationtest.TestContext(t), oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
				t.Fatal("failed refresh left cursor usable", err)
			}
			if err = c.PingContext(integrationtest.TestContext(t)); err != nil {
				integrationtest.FailOperation(t, "reuse session after missing row", err)
			}
		})
	}
}

func TestCursorModes(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestCursorModes(t, p) })
	}
}
