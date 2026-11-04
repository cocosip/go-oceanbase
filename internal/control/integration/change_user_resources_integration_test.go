//go:build integration

package integration

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestChangeUserResourceInvalidation opens a cursor before reauthentication and
// verifies the stale cursor handle cannot run under the new identity, while the
// pool still restores the original identity.
func TestChangeUserResourceInvalidation(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) { integrationTestChangeUserResourceInvalidation(t, p) })
	}
}

func integrationTestChangeUserResourceInvalidation(t *testing.T, p integrationtest.Profile) {
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
	table := fmt.Sprintf("OBGCR_%d", time.Now().UnixNano())
	if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create identity resource table", err)
	}
	defer func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop identity resource table", err)
		}
	}()
	for id := 1; id <= 2; id++ {
		if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (?)", id); err != nil {
			integrationtest.FailOperation(t, "seed identity resource table", err)
		}
	}
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin identity resource session", err)
	}
	defer func() { _ = pinned.Close() }()
	identitySQL := identityMySQLSQL
	if p.Mode == oceanbase.ModeOracle {
		identitySQL = identityOracleSQL
	}
	var original string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&original); err != nil {
		integrationtest.FailOperation(t, "read identity resource owner", err)
	}
	cursor, err := oceanbase.OpenCursor(ctx, pinned, "SELECT id FROM "+table+" ORDER BY id", oceanbase.CursorOptions{Mode: oceanbase.CursorForward})
	if err != nil {
		integrationtest.FailOperation(t, "open identity resource cursor", err)
	}
	rows, err := cursor.Fetch(ctx, oceanbase.CursorPosition{}, 1)
	if err != nil {
		integrationtest.FailOperation(t, "fetch identity resource cursor", err)
	}
	if !rows.Next() {
		t.Fatal("identity resource cursor row missing", rows.Err())
	}
	if err = rows.Close(); err != nil {
		integrationtest.FailOperation(t, "close identity resource batch", err)
	}
	if err = oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: next.User, Password: next.Password, ProxyUser: next.ProxyUser}, next.Database); err != nil {
		integrationtest.FailOperation(t, "change identity with open resources", err)
	}
	if _, err = cursor.Fetch(ctx, oceanbase.CursorPosition{}, 1); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("stale cursor survived identity change", err)
	}
	_ = cursor.Close()
	probe := sessionProbeSQL
	if p.Mode == oceanbase.ModeOracle {
		probe += integrationtest.OracleDualSuffix
	}
	var value int
	if err = pinned.QueryRowContext(ctx, probe).Scan(&value); err != nil || value != 42 {
		integrationtest.FailOperation(t, "query as changed identity with stale resources", err)
	}
	if err = pinned.Close(); err != nil {
		integrationtest.FailOperation(t, "close changed identity session", err)
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin original identity after resources", err)
	}
	defer func() { _ = fresh.Close() }()
	var restored string
	if err = fresh.QueryRowContext(ctx, identitySQL).Scan(&restored); err != nil || restored != original {
		t.Fatal("pool did not restore the original identity", restored, original, err)
	}
}

