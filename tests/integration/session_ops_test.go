//go:build integration

package integration

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestSessionOperations(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testSessionOperations(t, p) })
	}
}

func TestChangeUserSessions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testChangeUserSession(t, p) })
	}
}

func testChangeUserSession(t *testing.T, p profile) {
	t.Helper()
	key := strings.TrimSuffix(p.env, "_DSN") + "_CHANGE_USER_DSN"
	dsn := os.Getenv(key)
	if dsn == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required change-user credentials missing: " + p.name)
		}
		t.Skip("change-user credentials not configured: " + p.name)
	}
	next, err := oceanbase.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid change-user DSN for profile: " + p.name)
	}
	db := openProfile(t, p)
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin identity session", err)
	}
	defer func() { _ = pinned.Close() }()
	identitySQL, probe := "SELECT CURRENT_USER()", "SELECT 42"
	if p.mode == oceanbase.ModeOracle {
		identitySQL, probe = "SELECT USER FROM dual", "SELECT 42 FROM dual"
	}
	var original string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&original); err != nil {
		failOperation(t, "read original identity", err)
	}
	old, err := pinned.PrepareContext(ctx, probe)
	if err != nil {
		failOperation(t, "prepare before change", err)
	}
	defer func() { _ = old.Close() }()
	if err = oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: next.User, Password: next.Password}, next.Database); err != nil {
		failOperation(t, "change identity", err)
	}
	var changed string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&changed); err != nil {
		failOperation(t, "read changed identity", err)
	}
	want, _, _ := strings.Cut(next.User, "@")
	actual := changed
	if p.mode == oceanbase.ModeMySQL {
		actual, _, _ = strings.Cut(changed, "@")
	}
	if changed == original || actual != want {
		t.Fatal("change-user fixture must select and verify a distinct database identity")
	}
	var value int
	if err = old.QueryRowContext(ctx).Scan(&value); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("old statement survived identity change")
	}
	if err = pinned.QueryRowContext(ctx, probe).Scan(&value); err != nil || value != 42 {
		failOperation(t, "query as changed identity", err)
	}
	if err = old.Close(); err != nil {
		failOperation(t, "close old statement", err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("changed identity returned to original pool")
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin original pool identity", err)
	}
	defer func() { _ = fresh.Close() }()
	var restored string
	if err = fresh.QueryRowContext(ctx, identitySQL).Scan(&restored); err != nil || restored != original {
		failOperation(t, "verify original pool identity", err)
	}
}

func testSessionOperations(t *testing.T, p profile) {
	t.Helper()
	db := openProfile(t, p)
	ctx := testContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin session", err)
	}
	defer func() { _ = pinned.Close() }()
	schema, err := oceanbase.CurrentSchema(ctx, pinned)
	if err != nil || schema == "" {
		failOperation(t, "read schema", err)
	}
	catalog, err := oceanbase.CurrentCatalog(ctx, pinned)
	if p.mode == oceanbase.ModeMySQL && (err != nil || catalog != schema) || p.mode == oceanbase.ModeOracle && !errors.Is(err, oceanbase.ErrUnsupported) {
		t.Fatal("catalog does not match the compatibility mode")
	}
	variable, err := oceanbase.SessionVariable(ctx, pinned, "wait_timeout")
	if err != nil || !variable.Valid {
		failOperation(t, "read session variable", err)
	}
	if err = oceanbase.SetSchema(ctx, pinned, schema); err != nil {
		failOperation(t, "select schema", err)
	}
	if err = oceanbase.SetSessionVariable(ctx, pinned, "wait_timeout", variable.String); err != nil {
		failOperation(t, "set session variable", err)
	}
	info := oceanbase.ClientInfo{ApplicationName: "go-oceanbase-test", ClientUser: "test-user", ClientHostname: "test-host"}
	if err = oceanbase.SetClientInfo(ctx, pinned, info); err != nil {
		failOperation(t, "set client info", err)
	}
	got, err := oceanbase.GetClientInfo(ctx, pinned)
	if err != nil || got != info {
		failOperation(t, "read client info", err)
	}
	query := "SELECT 42"
	if p.mode == oceanbase.ModeOracle {
		query += oracleDualSuffix
	}
	e, err := oceanbase.Execute(ctx, pinned, query)
	if err != nil {
		failOperation(t, "execute session query", err)
	}
	if err = e.Close(); err != nil {
		failOperation(t, "close session query", err)
	}
	snapshot := e.Snapshot()
	warnings, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || len(snapshot.Results) != 1 || !warnings.Known || warnings != snapshot.Results[0].Warnings {
		t.Fatal("protocol warning count was not retained")
	}
	if err = pinned.Close(); err != nil {
		failOperation(t, "close mutated session", err)
	}
	if db.Stats().Idle != 0 {
		t.Fatal("mutated session was returned to the original pool")
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin replacement session", err)
	}
	defer func() { _ = fresh.Close() }()
	got, err = oceanbase.GetClientInfo(ctx, fresh)
	if err != nil {
		failOperation(t, "read fresh client info", err)
	}
	if got == info {
		t.Fatal("session attributes leaked to a new borrower")
	}
}
