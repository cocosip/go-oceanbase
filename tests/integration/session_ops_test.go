//go:build integration

package integration

import (
	"errors"
	"os"
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
