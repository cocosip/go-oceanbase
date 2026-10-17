//go:build integration

package integration

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

// TestFullLinkTrace verifies client identities; server-side trace visibility needs separate evidence.
func TestFullLinkTrace(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			cfg := profileConfig(t, p)
			cfg.Logger = nil
			cfg.Protocol.FullLinkTrace = oceanbase.FeatureOff
			if strings.HasPrefix(p.name, "proxy-") {
				cfg.Protocol.FullLinkTrace = oceanbase.FeatureRequired
			}
			var mu sync.Mutex
			var records []oceanbase.RequestDiagnostic
			cfg.Diagnostics.Request = func(_ context.Context, r oceanbase.RequestDiagnostic) {
				mu.Lock()
				records = append(records, r)
				mu.Unlock()
			}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "trace config", err)
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			ctx := testContext(t)
			query := "SELECT 1"
			if p.mode == oceanbase.ModeOracle {
				query += oracleDualSuffix
			}
			for _, app := range []oceanbase.TraceInfo{{Module: "go-driver-flt", Action: "first"}, {}} {
				var got int
				if err = db.QueryRowContext(oceanbase.WithTraceInfo(ctx, app), query).Scan(&got); err != nil {
					failOperation(t, "trace query", err)
				}
				if got != 1 {
					t.Fatal("trace changed query result")
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(records) == 0 {
				t.Fatal("missing client diagnostics")
			}
			for _, r := range records {
				if r.TraceID != [16]byte{} {
					t.Logf("sampled client trace=%x span=%x connection=%d request=%d", r.TraceID, r.SpanID, r.ConnectionID, r.RequestID)
				}
			}
			t.Log("verify app clearing and these IDs in the configured proxy/server trace; client events alone do not prove FLT visibility")
		})
	}
}
