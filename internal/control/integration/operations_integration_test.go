//go:build integration

package integration

import (
	sql "database/sql"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestChangeUserSessions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestChangeUserSession(t, p) })
	}
}

func TestMySQLNumericStringVariable(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) {
			db := integrationtest.OpenProfile(t, p)
			ctx := integrationtest.TestContext(t)
			c, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "pin string variable", err)
			}
			defer func() { _ = c.Close() }()
			if _, err = c.ExecContext(ctx, "SET SESSION ob_trace_info='000123'"); err != nil {
				integrationtest.FailOperation(t, "quoted numeric string baseline", err)
			}
			before, err := oceanbase.SessionVariable(ctx, c, "ob_trace_info")
			if err != nil || !before.Valid || before.String != "000123" {
				t.Fatal("server did not retain quoted numeric string", before, err)
			}
			if err = oceanbase.SetSessionVariable(ctx, c, "ob_trace_info", "000123"); err != nil {
				integrationtest.FailOperation(t, "set numeric string variable", err)
			}
			after, err := oceanbase.SessionVariable(ctx, c, "ob_trace_info")
			if err != nil || after != before {
				t.Fatal("session setter changed string value", after, before, err)
			}
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.Session.Variables = map[string]string{"ob_trace_info": "000123", "wait_timeout": "600"}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				integrationtest.FailOperation(t, "typed variable initialization config", err)
			}
			initialized := sql.OpenDB(connector)
			defer func() { _ = initialized.Close() }()
			var trace string
			var timeout int
			if err = initialized.QueryRowContext(ctx, "SELECT @@SESSION.ob_trace_info,@@SESSION.wait_timeout").Scan(&trace, &timeout); err != nil {
				integrationtest.FailOperation(t, "typed variable initialization", err)
			}
			if trace != "000123" || timeout != 600 {
				t.Fatal("initialization changed typed variable values", trace, timeout)
			}
		})
	}
}

func TestSessionOperations(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestSessionOperations(t, p) })
	}
}
