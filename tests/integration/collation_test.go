//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const sessionCollationCharset = "utf8mb4"
const sessionCollationText = "中文😀"

func TestSessionCollation(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			if p.mode == oceanbase.ModeOracle {
				cfg.Session.Collation = "utf8mb4_bin"
				_, err := oceanbase.NewConnector(cfg)
				var configErr *oceanbase.ConfigError
				if !errors.As(err, &configErr) || configErr.Field != "Session.Collation" {
					t.Fatal("Oracle collation not explicitly rejected")
				}
				return
			}
			for _, collation := range []string{"utf8mb4_bin", "utf8mb4_general_ci"} {
				t.Run(collation, func(t *testing.T) { testSessionCollation(t, cfg, collation) })
			}
		})
	}
}

func testSessionCollation(t *testing.T, cfg oceanbase.Config, collation string) {
	t.Helper()
	cfg.Session.Charset, cfg.Session.Collation = sessionCollationCharset, collation
	cfg.Session.InitSQL = append(cfg.Session.InitSQL, "SET NAMES latin1")
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		failOperation(t, "collation connector", err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin collation session", err)
	}
	defer func() { _ = pinned.Close() }()
	for round := range 2 {
		if round == 1 {
			if err := oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: cfg.User, Password: cfg.Password}, cfg.Database); err != nil {
				failOperation(t, "change user with collation", err)
			}
		}
		var client, connection, results, actual string
		if err := pinned.QueryRowContext(ctx, "SELECT @@character_set_client,@@character_set_connection,@@character_set_results,@@collation_connection").Scan(&client, &connection, &results, &actual); err != nil {
			failOperation(t, "read collation baseline", err)
		}
		if client != sessionCollationCharset || connection != client || results != client || actual != collation {
			t.Fatal("character sets or collation not restored after initialization")
		}
		var equal int
		if err := pinned.QueryRowContext(ctx, "SELECT 'a' = 'A'").Scan(&equal); err != nil {
			failOperation(t, "compare using connection collation", err)
		}
		want := 0
		if collation == "utf8mb4_general_ci" {
			want = 1
		}
		if equal != want {
			t.Fatal("connection collation did not control literal comparison")
		}
		var text string
		if err := pinned.QueryRowContext(ctx, "SELECT ?", sessionCollationText).Scan(&text); err != nil || text != sessionCollationText {
			failOperation(t, "collation parameter round trip", err)
		}
	}
}
