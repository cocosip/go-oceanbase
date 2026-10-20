//go:build integration

package diagnostic_test

import (
	context "context"
	sql "database/sql"
	os "os"
	strings "strings"
	sync "sync"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

// TestFullLinkTrace verifies client identities; server-side trace visibility needs separate evidence.
func TestFullLinkTrace(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.Logger = nil
			cfg.Protocol.FullLinkTrace = oceanbase.FeatureOff
			if strings.HasPrefix(p.Name, "proxy-") {
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
				integrationtest.FailOperation(t, "trace config", err)
			}
			db := sql.OpenDB(connector)
			db.SetMaxOpenConns(1)
			defer func() { _ = db.Close() }()
			ctx := integrationtest.TestContext(t)
			query := "SELECT 1"
			if p.Mode == oceanbase.ModeOracle {
				query += integrationtest.OracleDualSuffix
			}
			for _, app := range []oceanbase.TraceInfo{{Module: "go-driver-flt", Action: traceFirst}, {}} {
				var got int
				if err = db.QueryRowContext(oceanbase.WithTraceInfo(ctx, app), query).Scan(&got); err != nil {
					integrationtest.FailOperation(t, "trace query", err)
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
