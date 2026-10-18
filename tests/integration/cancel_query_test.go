//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func TestCancelQuerySessions(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) { testCancelQuerySession(t, p) })
	}
}

func testCancelQuerySession(t *testing.T, p profile) {
	t.Helper()
	key := strings.TrimSuffix(p.env, "_DSN") + "_CANCEL_SQL"
	query := os.Getenv(key)
	if query == "" {
		if os.Getenv("OB_TEST_REQUIRE") == "1" {
			t.Fatal("required long-running read-only SQL missing: " + p.name)
		}
		t.Skip("cancellation SQL not configured: " + p.name)
	}
	expected, err := strconv.ParseUint(os.Getenv(strings.TrimSuffix(p.env, "_DSN")+"_CANCEL_CODE"), 10, 32)
	if err != nil || expected == 0 {
		t.Fatal("a nonzero expected cancellation error code is required: " + p.name)
	}
	configured, err := oceanbase.NewConnector(profileConfig(t, p))
	if err != nil {
		t.Fatal("invalid cancel connector for profile: " + p.name)
	}
	db := sql.OpenDB(configured)
	defer func() { _ = db.Close() }()
	ctx, stop := context.WithCancel(testContext(t))
	defer stop()
	pinned, err := db.Conn(ctx)
	if err != nil {
		failOperation(t, "pin cancellation session", err)
	}
	defer func() { _ = pinned.Close() }()
	target, err := oceanbase.QueryTarget(ctx, pinned)
	if err != nil {
		failOperation(t, "arm cancellation target", err)
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
			failOperation(t, "wait for cancellable command", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err != nil {
		failOperation(t, "cancel through auxiliary connection", err)
	}
	<-done
	if !onlyServerErrorCode(outcome, uint32(expected)) {
		t.Fatal("fixture did not produce a server cancellation error")
	}
	if err = pinned.PingContext(ctx); err != nil {
		failOperation(t, "reuse explicitly canceled session", err)
	}
	if err = oceanbase.CancelQuery(ctx, configured, target); !errors.Is(err, oceanbase.ErrResource) {
		t.Fatal("completed target accepted another cancellation")
	}
}
