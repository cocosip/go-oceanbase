//go:build integration

package integration

import (
	context "context"
	sql "database/sql"
	errors "errors"
	os "os"
	strconv "strconv"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func integrationTestWarningDetails(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	key := strings.TrimSuffix(p.Env, "_DSN") + "_WARNING_SQL"
	query := os.Getenv(key)
	if query == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required warning fixture missing: " + p.Name)
		}
		t.Skip("warning fixture not configured: " + p.Name)
	}
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin warning session", err)
	}
	defer func() { _ = pinned.Close() }()
	// The deployment supplies a successful statement that produces nonzero warnings.
	if _, err = pinned.ExecContext(ctx, query); err != nil {
		integrationtest.FailOperation(t, "execute warning fixture", err)
	}
	count, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || !count.Known || count.Count == 0 {
		t.Fatal("fixture did not produce a protocol warning count")
	}
	details, err := oceanbase.ReadWarnings(ctx, pinned)
	if p.Mode == oceanbase.ModeOracle {
		if !errors.Is(err, oceanbase.ErrUnsupported) {
			t.Fatal("Oracle warning detail boundary was not explicit")
		}
	} else {
		if err != nil {
			integrationtest.FailOperation(t, "read warning details", err)
		}
		if len(details) == 0 {
			t.Fatal("MySQL fixture must retain server warning details")
		}
		for _, w := range details {
			if w.Level == "" || w.Code == 0 || w.Message == "" {
				t.Fatal("incomplete server warning details")
			}
		}
	}
	after, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || after != count {
		t.Fatal("reading details replaced the ordinary warning count")
	}
	if err = oceanbase.ClearWarnings(ctx, pinned); err != nil {
		integrationtest.FailOperation(t, "clear warning snapshot", err)
	}
	after, err = oceanbase.Warnings(ctx, pinned)
	if err != nil || after != (oceanbase.WarningSummary{Known: true}) {
		t.Fatal("local warning clear failed")
	}
	if p.Mode == oceanbase.ModeMySQL {
		if details, err = oceanbase.ReadWarnings(ctx, pinned); err != nil || len(details) != 0 {
			t.Fatal("cleared warning details reappeared")
		}
	}
}

func integrationTestChangeUserSession(t *testing.T, p integrationtest.Profile) {
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
	identitySQL, probe := "SELECT CURRENT_USER()", "SELECT 42"
	if p.Mode == oceanbase.ModeOracle {
		identitySQL, probe = "SELECT USER FROM dual", "SELECT 42 FROM dual"
	}
	var original string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&original); err != nil {
		integrationtest.FailOperation(t, "read original identity", err)
	}
	old, err := pinned.PrepareContext(ctx, probe)
	if err != nil {
		integrationtest.FailOperation(t, "prepare before change", err)
	}
	defer func() { _ = old.Close() }()
	if err = oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: next.User, Password: next.Password, ProxyUser: next.ProxyUser}, next.Database); err != nil {
		integrationtest.FailOperation(t, "change identity", err)
	}
	var changed string
	if err = pinned.QueryRowContext(ctx, identitySQL).Scan(&changed); err != nil {
		integrationtest.FailOperation(t, "read changed identity", err)
	}
	want, _, _ := strings.Cut(next.User, "@")
	if next.ProxyUser != "" {
		want = next.ProxyUser
	}
	actual := changed
	if p.Mode == oceanbase.ModeMySQL {
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
		integrationtest.FailOperation(t, "query as changed identity", err)
	}
	if err = old.Close(); err != nil {
		integrationtest.FailOperation(t, "close old statement", err)
	}
	if err = pinned.Close(); err != nil || db.Stats().Idle != 0 {
		t.Fatal("changed identity returned to original pool")
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin original pool identity", err)
	}
	defer func() { _ = fresh.Close() }()
	var restored string
	if err = fresh.QueryRowContext(ctx, identitySQL).Scan(&restored); err != nil || restored != original {
		integrationtest.FailOperation(t, "verify original pool identity", err)
	}
}

func integrationTestSessionOperations(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin session", err)
	}
	defer func() { _ = pinned.Close() }()
	schema, err := oceanbase.CurrentSchema(ctx, pinned)
	if err != nil || schema == "" {
		integrationtest.FailOperation(t, "read schema", err)
	}
	catalog, err := oceanbase.CurrentCatalog(ctx, pinned)
	if p.Mode == oceanbase.ModeMySQL && (err != nil || catalog != schema) || p.Mode == oceanbase.ModeOracle && !errors.Is(err, oceanbase.ErrUnsupported) {
		t.Fatal("catalog does not match the compatibility mode")
	}
	variable, err := oceanbase.SessionVariable(ctx, pinned, "wait_timeout")
	if err != nil || !variable.Valid {
		integrationtest.FailOperation(t, "read session variable", err)
	}
	if err = oceanbase.SetSchema(ctx, pinned, schema); err != nil {
		integrationtest.FailOperation(t, "select schema", err)
	}
	if err = oceanbase.SetSessionVariable(ctx, pinned, "wait_timeout", variable.String); err != nil {
		integrationtest.FailOperation(t, "set session variable", err)
	}
	info := oceanbase.ClientInfo{ApplicationName: "go-oceanbase-test", ClientUser: "test-user", ClientHostname: "test-host"}
	if err = oceanbase.SetClientInfo(ctx, pinned, info); err != nil {
		integrationtest.FailOperation(t, "set client info", err)
	}
	got, err := oceanbase.GetClientInfo(ctx, pinned)
	if err != nil || got != info {
		integrationtest.FailOperation(t, "read client info", err)
	}
	query := "SELECT 42"
	if p.Mode == oceanbase.ModeOracle {
		query += integrationtest.OracleDualSuffix
	}
	e, err := oceanbase.Execute(ctx, pinned, query)
	if err != nil {
		integrationtest.FailOperation(t, "execute session query", err)
	}
	if err = e.Close(); err != nil {
		integrationtest.FailOperation(t, "close session query", err)
	}
	snapshot := e.Snapshot()
	warnings, err := oceanbase.Warnings(ctx, pinned)
	if err != nil || len(snapshot.Results) != 1 || !warnings.Known || warnings != snapshot.Results[0].Warnings {
		t.Fatal("protocol warning count was not retained")
	}
	if err = pinned.Close(); err != nil {
		integrationtest.FailOperation(t, "close mutated session", err)
	}
	if db.Stats().Idle != 0 {
		t.Fatal("mutated session was returned to the original pool")
	}
	fresh, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin replacement session", err)
	}
	defer func() { _ = fresh.Close() }()
	got, err = oceanbase.GetClientInfo(ctx, fresh)
	if err != nil {
		integrationtest.FailOperation(t, "read fresh client info", err)
	}
	if got == info {
		t.Fatal("session attributes leaked to a new borrower")
	}
}

func integrationTestSessionCollation(t *testing.T, cfg oceanbase.Config, collation string) {
	t.Helper()
	cfg.Session.Charset, cfg.Session.Collation = integrationSessionCollationCharset, collation
	cfg.Session.InitSQL = append(cfg.Session.InitSQL, "SET NAMES latin1")
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		integrationtest.FailOperation(t, "collation connector", err)
	}
	db := sql.OpenDB(created)
	defer func() { _ = db.Close() }()
	ctx := integrationtest.TestContext(t)
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin collation session", err)
	}
	defer func() { _ = pinned.Close() }()
	for round := range 2 {
		if round == 1 {
			if err := oceanbase.ChangeUser(ctx, pinned, oceanbase.Credentials{User: cfg.User, Password: cfg.Password}, cfg.Database); err != nil {
				integrationtest.FailOperation(t, "change user with collation", err)
			}
		}
		var client, connection, results, actual string
		if err := pinned.QueryRowContext(ctx, "SELECT @@character_set_client,@@character_set_connection,@@character_set_results,@@collation_connection").Scan(&client, &connection, &results, &actual); err != nil {
			integrationtest.FailOperation(t, "read collation baseline", err)
		}
		if client != integrationSessionCollationCharset || connection != client || results != client || actual != collation {
			t.Fatal("character sets or collation not restored after initialization")
		}
		var equal int
		if err := pinned.QueryRowContext(ctx, "SELECT 'a' = 'A'").Scan(&equal); err != nil {
			integrationtest.FailOperation(t, "compare using connection collation", err)
		}
		want := 0
		if collation == "utf8mb4_general_ci" {
			want = 1
		}
		if equal != want {
			t.Fatal("connection collation did not control literal comparison")
		}
		var text string
		if err := pinned.QueryRowContext(ctx, "SELECT ?", integrationSessionCollationText).Scan(&text); err != nil || text != integrationSessionCollationText {
			integrationtest.FailOperation(t, "collation parameter round trip", err)
		}
	}
}

const integrationSessionCollationText = "中文😀"

const integrationSessionCollationCharset = "utf8mb4"

func integrationTestCancelQuerySession(t *testing.T, p integrationtest.Profile) {
	t.Helper()
	key := strings.TrimSuffix(p.Env, "_DSN") + "_CANCEL_SQL"
	query := os.Getenv(key)
	if query == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required long-running read-only SQL missing: " + p.Name)
		}
		t.Skip("cancellation SQL not configured: " + p.Name)
	}
	expected, err := strconv.ParseUint(os.Getenv(strings.TrimSuffix(p.Env, "_DSN")+"_CANCEL_CODE"), 10, 32)
	if err != nil || expected == 0 {
		t.Fatal("a nonzero expected cancellation error code is required: " + p.Name)
	}
	configured, err := oceanbase.NewConnector(integrationtest.ProfileConfig(t, p))
	if err != nil {
		t.Fatal("invalid cancel connector for profile: " + p.Name)
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	ctx, stop := context.WithCancel(integrationtest.TestContext(t))
	defer stop()
	pinned, err := db.Conn(ctx)
	if err != nil {
		integrationtest.FailOperation(t, "pin cancellation session", err)
	}
	defer func() { _ = pinned.Close() }()
	target, err := oceanbase.QueryTarget(ctx, pinned)
	if err != nil {
		integrationtest.FailOperation(t, "arm cancellation target", err)
	}
	done := make(chan struct{})
	var outcome error
	go func() { _, outcome = pinned.ExecContext(ctx, query); close(done) }()
	defer func() { stop(); <-done }()
	// Wait for the command to enter the sending phase; an idle token never dials.
	for {
		err = oceanbase.CancelQuery(ctx, configured, target)
		if !errors.Is(err, oceanbase.ErrResource) {
			break
		}
		select {
		case <-done:
			t.Fatal("SQL completed before a cancellable operation was observed")
		case <-ctx.Done():
			integrationtest.FailOperation(t, "wait for cancellable command", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err != nil {
		integrationtest.FailOperation(t, "cancel through auxiliary connection", err)
	}
	<-done
	if !integrationtest.OnlyServerErrorCode(outcome, uint32(expected)) {
		t.Fatal("fixture did not produce a server cancellation error")
	}
	if err = pinned.PingContext(ctx); err != nil {
		integrationtest.FailOperation(t, "reuse explicitly canceled session", err)
	}
	if err = oceanbase.CancelQuery(ctx, configured, target); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("completed target accepted another cancellation")
	}
}
