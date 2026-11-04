//go:build integration

package integration

import (
	context "context"
	errors "errors"
	os "os"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestChangeUserGuardSessions verifies that ChangeUser refuses an open
// transaction or an already-canceled context without changing the identity or
// invalidating the session.
func TestChangeUserGuardSessions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestChangeUserGuard(t, p) })
	}
}

func integrationTestChangeUserGuard(t *testing.T, p integrationtest.Profile) {
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
		integrationtest.FailOperation(t, "pin guarded identity session", err)
	}
	defer func() { _ = pinned.Close() }()
	identitySQL := "SELECT CURRENT_USER()"
	if p.Mode == oceanbase.ModeOracle {
		identitySQL = "SELECT USER FROM dual"
	}
	var original string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&original); err != nil {
		integrationtest.FailOperation(t, "read guarded identity", err)
	}
	credentials := oceanbase.Credentials{User: next.User, Password: next.Password}
	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin guarded identity transaction", err)
	}
	probe := "SELECT 42"
	if p.Mode == oceanbase.ModeOracle {
		probe += integrationtest.OracleDualSuffix
	}
	if _, err = tx.ExecContext(ctx, probe); err != nil {
		integrationtest.FailOperation(t, "probe guarded identity transaction", err)
	}
	if err = oceanbase.ChangeUser(ctx, pinned, credentials, next.Database); !errors.Is(err, oceanbase.ErrUnsupported) {
		t.Fatal("ChangeUser inside a transaction was not rejected", err)
	}
	var current string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&current); err != nil || current != original {
		t.Fatal("rejected in-transaction ChangeUser changed identity", current, original, err)
	}
	if err = tx.Rollback(); err != nil {
		integrationtest.FailOperation(t, "rollback guarded identity transaction", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = oceanbase.ChangeUser(canceled, pinned, credentials, next.Database); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled ChangeUser was not rejected", err)
	}
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&current); err != nil || current != original {
		t.Fatal("canceled ChangeUser changed identity", current, original, err)
	}
	if err = pinned.PingContext(ctx); err != nil {
		t.Fatal("guarded identity session unusable", err)
	}
}
