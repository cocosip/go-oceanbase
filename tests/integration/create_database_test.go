//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestCreateDatabaseSessions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			if p.mode == oceanbase.ModeOracle {
				_, err := oceanbase.NewConnector(oceanbase.Config{User: "app", Mode: p.mode, Database: "example", CreateDatabaseIfMissing: true})
				var invalid *oceanbase.ConfigError
				if !errors.As(err, &invalid) || invalid.Field != "CreateDatabaseIfMissing" {
					t.Fatal("Oracle database creation not explicitly rejected")
				}
				return
			}
			testCreateDatabaseSession(t, p)
		})
	}
}

func testCreateDatabaseSession(t *testing.T, p profile) {
	t.Helper()
	fixture := strings.TrimSuffix(p.env, "_DSN") + "_CREATE_DATABASE_DSN"
	dsn := os.Getenv(fixture)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required database creation fixture missing: " + p.name)
		}
		t.Skip("database creation fixture not configured: " + p.name)
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil || cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.mode {
		t.Fatal("invalid database creation fixture: " + p.name)
	}
	cfg.Mode = p.mode
	adminConfig := cfg
	adminConfig.Database, adminConfig.CreateDatabaseIfMissing = "", false
	adminConfig.Session.InitSQL = nil
	adminConnector, err := oceanbase.NewConnector(adminConfig)
	if err != nil {
		failOperation(t, "creation admin connector", err)
	}
	admin := sql.OpenDB(adminConnector)
	defer func() { _ = admin.Close() }()
	var id [8]byte
	if _, err = rand.Read(id[:]); err != nil {
		t.Fatal("generate database fixture name")
	}
	name := fmt.Sprintf("obgdb_%x", id)
	ctx := testContext(t)
	var exists int
	if err = admin.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME=?", name).Scan(&exists); err != nil {
		failOperation(t, "check database fixture absence", err)
	}
	if exists != 0 {
		t.Fatal("generated fixture already exists; refusing to modify it")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanupCtx, "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Errorf("cleanup generated database %s failed: %T", name, err)
		}
	}()
	cfg.Database, cfg.CreateDatabaseIfMissing = name, true
	// This statement must run only after the new database has been selected.
	cfg.Session.InitSQL = []string{"CREATE TABLE IF NOT EXISTS PROBE (ID INTEGER PRIMARY KEY)"}
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		failOperation(t, "creation connector", err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	db.SetMaxIdleConns(0)
	for round := range 2 {
		pinned, err := db.Conn(ctx)
		if err != nil {
			failOperation(t, "create or reuse existing database", err)
		}
		defer func() { _ = pinned.Close() }()
		actual, err := oceanbase.CurrentCatalog(ctx, pinned)
		if err != nil {
			failOperation(t, "read created database", err)
		}
		if actual != name {
			t.Fatal("new physical connection selected the wrong database")
		}
		if round == 0 {
			if _, err = pinned.ExecContext(ctx, "INSERT INTO PROBE VALUES(42)"); err != nil {
				failOperation(t, "write created database", err)
			}
		}
		var value int
		if err = pinned.QueryRowContext(ctx, "SELECT ID FROM PROBE").Scan(&value); err != nil {
			failOperation(t, "read created database after pool replacement", err)
		}
		if value != 42 {
			t.Fatal("CREATE IF NOT EXISTS changed existing database data")
		}
		if err = pinned.Close(); err != nil {
			failOperation(t, "close created database session", err)
		}
	}
}
