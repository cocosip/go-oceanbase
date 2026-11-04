//go:build integration

package integration

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	net "net"
	os "os"
	strings "strings"
	testing "testing"
	time "time"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestReadTimeoutSessions(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			key := strings.TrimSuffix(p.Env, "_DSN") + "_READ_TIMEOUT_SQL"
			query := os.Getenv(key)
			if query == "" {
				query = integrationLockedRead(t, p)
			}
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.ReadTimeout = time.Second
			configured, err := oceanbase.NewConnector(cfg)
			if err != nil {
				integrationtest.FailOperation(t, "read timeout connector", err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(integrationtest.TestContext(t), 10*time.Second)
			defer cancel()
			pinned, err := db.Conn(ctx)
			if err != nil {
				integrationtest.FailOperation(t, "initialize read timeout session", err)
			}
			defer func() { _ = pinned.Close() }()
			// The fixture must block for at least three seconds before its first response.
			_, err = pinned.ExecContext(ctx, query)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || errors.Is(err, driver.ErrBadConn) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				t.Fatal("fixture did not produce an independent network read timeout")
			}
			if err = pinned.PingContext(ctx); !errors.Is(err, driver.ErrBadConn) && !errors.Is(err, sql.ErrConnDone) {
				t.Fatal("timed out physical session remained usable", err)
			}
			if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				integrationtest.FailOperation(t, "release timed out session", err)
			}
			if db.Stats().Idle != 0 {
				t.Fatal("read timeout returned an unusable session to the pool")
			}
			if err = db.PingContext(ctx); err != nil {
				integrationtest.FailOperation(t, "replace timed out physical session", err)
			}
		})
	}
}

func integrationLockedRead(t *testing.T, p integrationtest.Profile) string {
	t.Helper()
	db := integrationtest.OpenProfile(t, p)
	ctx := integrationtest.TestContext(t)
	table := fmt.Sprintf("OBG_RTO_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY)"); err != nil {
		integrationtest.FailOperation(t, "create read timeout fixture", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(integrationtest.TestContext(t), "DROP TABLE "+table); err != nil {
			integrationtest.FailOperation(t, "drop read timeout fixture", err)
		}
	})
	if _, err := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES (1)"); err != nil {
		integrationtest.FailOperation(t, "seed read timeout fixture", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		integrationtest.FailOperation(t, "begin lock holder", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil {
			integrationtest.FailOperation(t, "release read timeout lock", err)
		}
	})
	query := "SELECT id FROM " + table + " WHERE id=1 FOR UPDATE"
	var id int
	if err = tx.QueryRowContext(ctx, query).Scan(&id); err != nil || id != 1 {
		t.Fatal("read timeout fixture did not hold row lock", id, err)
	}
	return query
}
