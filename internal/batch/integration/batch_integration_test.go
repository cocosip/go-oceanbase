//go:build integration

package integration

import (
	sql "database/sql"
	os "os"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestBatchArrayBindingExecution(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestBatchArrayBindingProfile(t, p) })
	}
}

func TestBatchRewriteExecution(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.AllowMultiStatements = true
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				t.Fatal("invalid batch rewrite config")
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			db.SetMaxOpenConns(1)
			integrationTestBatchDatabase(t, p, db, true)
		})
	}
}

func TestBatchExecution(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestBatchProfile(t, p) })
	}
}
