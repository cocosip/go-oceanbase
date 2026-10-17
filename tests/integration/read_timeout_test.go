//go:build integration

package integration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestReadTimeoutSessions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			key := strings.TrimSuffix(p.env, "_DSN") + "_READ_TIMEOUT_SQL"
			query := os.Getenv(key)
			if query == "" {
				if os.Getenv("OB_TEST_REQUIRE") == "1" {
					t.Fatal("required long-running read-only SQL missing: " + p.name)
				}
				t.Skip("read timeout SQL not configured: " + p.name)
			}
			cfg := profileConfig(t, p)
			cfg.ReadTimeout = time.Second
			configured, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "read timeout connector", err)
			}
			db := sql.OpenDB(configured)
			defer func() { _ = db.Close() }()
			ctx, cancel := context.WithTimeout(testContext(t), 10*time.Second)
			defer cancel()
			pinned, err := db.Conn(ctx)
			if err != nil {
				failOperation(t, "initialize read timeout session", err)
			}
			defer func() { _ = pinned.Close() }()
			// The fixture must block for at least three seconds before its first response.
			_, err = pinned.ExecContext(ctx, query)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || errors.Is(err, driver.ErrBadConn) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				t.Fatal("fixture did not produce an independent network read timeout")
			}
			if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
				failOperation(t, "release timed out session", err)
			}
			if db.Stats().Idle != 0 {
				t.Fatal("read timeout returned an unusable session to the pool")
			}
			if err = db.PingContext(ctx); err != nil {
				failOperation(t, "replace timed out physical session", err)
			}
		})
	}
}
