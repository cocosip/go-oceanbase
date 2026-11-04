//go:build integration

package integration

import (
	"database/sql/driver"
	"errors"
	"os"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestChangeUserFailure verifies a rejected reauthentication against a real
// server: the server code is preserved, the pinned session is discarded and the
// pool recovers with a fresh session.
func TestChangeUserFailure(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		if p.Mode != oceanbase.ModeMySQL {
			continue
		}
		t.Run(p.Name, func(t *testing.T) { integrationTestChangeUserFailure(t, p) })
	}
}

func integrationTestChangeUserFailure(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	key := strings.TrimSuffix(p.Env, "_DSN") + "_CHANGE_USER_DSN"
	dsn := os.Getenv(key)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required change-user credentials missing: " + p.Name)
		}
		t.Skip("change-user credentials not configured: " + p.Name)
	}
	next, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid change-user DSN for profile: " + p.Name)
	}
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin identity session", err)
	}
	defer func() { _ = pinned.Close() }()
	var alive int
	if err = pinned.QueryRowContext(ctx, "SELECT 1").Scan(&alive); err != nil {
		integrationtest.FailOperation(t, "probe original session", err)
	}
	err = oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: next.User, Password: next.Password + "-wrong"}, next.Database)
	if !integrationtest.OnlyServerErrorCode(err, 1045) {
		t.Fatal("rejected reauthentication did not preserve the server code:", err)
	}
	if err = pinned.QueryRowContext(ctx, "SELECT 1").Scan(&alive); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal("failed reauthentication kept the pinned session usable:", err)
	}
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("pool did not recover with a fresh session:", err)
	}
}
