//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

type profile struct {
	name, env string
	mode      oceanbase.Mode
}

const oracleDualSuffix = " FROM dual"

func profiles(name string) ([]profile, error) {
	all := []profile{{"mysql", "OB_MYSQL_DSN", oceanbase.ModeMySQL}, {"oracle", "OB_ORACLE_DSN", oceanbase.ModeOracle}, {"proxy-mysql", "OBPROXY_MYSQL_DSN", oceanbase.ModeMySQL}, {"proxy-oracle", "OBPROXY_ORACLE_DSN", oceanbase.ModeOracle}}
	if name == "" || name == "all" {
		return all, nil
	}
	for _, p := range all {
		if p.name == name {
			return []profile{p}, nil
		}
	}
	return nil, errors.New("invalid integration profile")
}

func profileConfig(t *testing.T, p profile) oceanbase.Config {
	t.Helper()
	dsn := os.Getenv(p.env)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required profile missing: " + p.name)
		}
		t.Skip("profile not configured: " + p.name)
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid DSN for profile: " + p.name)
	}
	if cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.mode {
		t.Fatal("DSN mode differs from profile: " + p.name)
	}
	cfg.Mode = p.mode
	return cfg
}

func openProfile(t *testing.T, p profile) *sql.DB {
	t.Helper()
	cfg := profileConfig(t, p)
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal("invalid config for profile: " + p.name)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("close failed for profile: " + p.name)
		}
	})
	t.Logf("profile=%s mode_constraint=%d go=%s os=%s arch=%s", p.name, p.mode, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return db
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func failOperation(t *testing.T, operation string, err error) {
	t.Helper()
	var server *oceanbase.Error
	if errors.As(err, &server) {
		t.Fatalf("%s failed: server code=%d SQLSTATE=%s", operation, server.Code, server.SQLState)
	}
	t.Fatalf("%s failed: %T", operation, err)
}

func TestProfiles(t *testing.T) {
	all, err := profiles("all")
	if err != nil || len(all) != 4 {
		t.Fatal("profile matrix incomplete")
	}
	for _, name := range []string{"mysql", "oracle", "proxy-mysql", "proxy-oracle"} {
		items, err := profiles(name)
		if err != nil || len(items) != 1 || items[0].name != name || items[0].env == "" {
			t.Fatal("incorrect profile selection")
		}
	}
	if _, err = profiles("unexpected"); err == nil {
		t.Fatal("invalid profile silently selected")
	}
}
