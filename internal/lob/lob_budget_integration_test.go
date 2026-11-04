//go:build integration

package lob_test

import (
	bytes "bytes"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	os "os"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestResultBudgetLimits verifies that LOB and message budgets reject oversized
// results with an evicted session while default limits still read the value.
func TestResultBudgetLimits(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestResultBudgetLimits(t, p) })
	}
}

func integrationTestResultBudgetLimits(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	setup := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBGB_%d", time.Now().UnixNano())
	typ := "LONGBLOB"
	if p.Mode == oceanbase.ModeOracle {
		typ = "BLOB"
	}
	if _, err := setup.ExecContext(ctx, "CREATE TABLE "+table+" (ID INTEGER PRIMARY KEY,B "+typ+")"); err != nil {
		integrationtest.FailOperation(t, "create budget table", err)
	}
	defer func() {
		if _, err := setup.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop budget table", err)
		}
	}()
	value := bytes.Repeat([]byte{'A'}, 4096)
	if _, err := setup.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1,?)", value); err != nil {
		integrationtest.FailOperation(t, "seed budget LOB", err)
	}
	open := func(configure func(*oceanbase.Config)) *sql.DB {
		t.Helper()
		cfg := integrationtest.ProfileConfig(t, p)
		configure(&cfg)
		connector, err := oceanbase.NewConnector(cfg)
		if err != nil {
			integrationtest.FailOperation(t, "configure budget pool", err)
		}
		db := sql.OpenDB(connector)
		db.SetMaxOpenConns(1)
		return db
	}
	lobDB := open(func(cfg *oceanbase.Config) { cfg.Limits.MaxLOBBytes = 1024 })
	defer func() { _ = lobDB.Close() }()
	lobConn, err := lobDB.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin LOB budget session", err)
	}
	var blob []byte
	if err = lobConn.QueryRowContext(ctx, "SELECT B FROM "+table+" WHERE ID=1").Scan(&blob); err == nil {
		t.Fatal("LOB read over MaxLOBBytes succeeded")
	}
	if err = lobConn.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("LOB budget rejection kept the session usable", err)
	}
	_ = lobConn.Close()
	msgDB := open(func(cfg *oceanbase.Config) { cfg.Limits.MaxMessageBytes = 4096 })
	defer func() { _ = msgDB.Close() }()
	msgConn, err := msgDB.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin message budget session", err)
	}
	var text string
	wide := "SELECT RPAD('x', 8000, 'x')"
	if p.Mode == oceanbase.ModeOracle {
		wide += integrationtest.OracleDualSuffix
	}
	if err = msgConn.QueryRowContext(ctx, wide).Scan(&text); err == nil {
		t.Fatal("row over MaxMessageBytes succeeded")
	}
	if err = msgConn.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("message budget rejection kept the session usable", err)
	}
	_ = msgConn.Close()
	var full []byte
	if err = setup.QueryRowContext(ctx, "SELECT B FROM "+table+" WHERE ID=1").Scan(&full); err != nil || !bytes.Equal(full, value) {
		t.Fatal("full LOB read after budget rejections", len(full), err)
	}
}
