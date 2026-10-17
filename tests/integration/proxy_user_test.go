//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestProxyUserSessions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			if p.mode == oceanbase.ModeMySQL {
				_, err := oceanbase.NewConnector(oceanbase.Config{User: "app", Mode: p.mode, ProxyUser: "client_user"})
				var invalid *oceanbase.ConfigError
				if !errors.As(err, &invalid) || invalid.Field != "ProxyUser" {
					t.Fatal("MySQL proxy login not explicitly rejected")
				}
				return
			}
			testProxyUserSession(t, p)
		})
	}
}

func testProxyUserSession(t *testing.T, p profile) {
	t.Helper()
	base := strings.TrimSuffix(p.env, "_DSN")
	dsn := os.Getenv(base + "_PROXY_DSN")
	authenticated := os.Getenv(base + "_PROXY_AUTH_USER")
	target := os.Getenv(base + "_PROXY_SESSION_USER")
	if dsn == "" || authenticated == "" || target == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required proxy identity fixture missing: " + p.name)
		}
		t.Skip("proxy identity fixture not configured: " + p.name)
	}
	if authenticated == target {
		t.Fatal("proxy fixture must distinguish authenticating and effective users")
	}
	cfg, err := oceanbase.ParseDSN(dsn)
	if err != nil || cfg.ProxyUser == "" || cfg.Mode != oceanbase.ModeAuto && cfg.Mode != p.mode {
		t.Fatal("invalid proxy identity fixture: " + p.name)
	}
	cfg.Mode = p.mode
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		failOperation(t, "proxy connector", err)
	}
	db := sql.OpenDB(created)
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin proxy session", err)
	}
	defer func() { _ = pinned.Close() }()
	assertProxyIdentity(ctx, t, pinned, target, authenticated)
	credentials := oceanbase.Credentials{User: cfg.User, Password: cfg.Password, ProxyUser: cfg.ProxyUser}
	if err = oceanbase.ChangeUser(ctx, pinned, credentials, cfg.Database); err != nil {
		failOperation(t, "reauthenticate proxy identity", err)
	}
	assertProxyIdentity(ctx, t, pinned, target, authenticated)
	credentials.ProxyUser = ""
	if err = oceanbase.ChangeUser(ctx, pinned, credentials, cfg.Database); err != nil {
		failOperation(t, "clear proxy identity", err)
	}
	assertProxyIdentity(ctx, t, pinned, authenticated, "")
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("changed proxy identity returned to original pool")
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "reopen original proxy identity", err)
	}
	defer func() { _ = fresh.Close() }()
	assertProxyIdentity(ctx, t, fresh, target, authenticated)
}

func assertProxyIdentity(ctx context.Context, t *testing.T, pinned *sql.Conn, target, authenticated string) {
	t.Helper()
	var current, session string
	var proxy sql.NullString
	if err := pinned.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_USER'), SYS_CONTEXT('USERENV','PROXY_USER'), SYS_CONTEXT('USERENV','SESSION_USER') FROM dual").Scan(&current, &proxy, &session); err != nil {
		failOperation(t, "read effective proxy identity", err)
	}
	if current != target || session != target || authenticated != "" && (!proxy.Valid || proxy.String != authenticated) || authenticated == "" && proxy.Valid && proxy.String != "" {
		t.Fatal("server identity differs from explicit proxy fixture")
	}
}
