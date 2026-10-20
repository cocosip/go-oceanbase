//go:build integration

package integration

import (
	context "context"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
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
				if os.Getenv("OB_TEST_REQUIRE") == "1" {
					t.Fatal("required long-running read-only SQL missing: " + p.Name)
				}
				t.Skip("read timeout SQL not configured: " + p.Name)
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
