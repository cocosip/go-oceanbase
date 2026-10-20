//go:build integration

package integration

import (
	context "context"
	tls "crypto/tls"
	sql "database/sql"
	errors "errors"
	os "os"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"

	oceanbase "github.com/cocosip/go-oceanbase"
	integrationtest "github.com/cocosip/go-oceanbase/internal/integrationtest"
)

func TestTLSDisabledEndpoint(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			if os.Getenv(strings.TrimSuffix(p.Env, "_DSN")+"_TLS_DISABLED") != "1" {
				t.Skip("endpoint without SSL not configured")
			}
			cfg := integrationtest.ProfileConfig(t, p)
			cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			var credentials atomic.Int32
			cfg.Credentials = func(context.Context) (oceanbase.Credentials, error) {
				credentials.Add(1)
				return oceanbase.Credentials{}, errors.New("credentials requested before verified TLS")
			}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				integrationtest.FailOperation(t, "TLS-required config", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			if err = db.PingContext(integrationtest.TestContext(t)); !errors.Is(err, oceanbase.ErrTLSRequired) || credentials.Load() != 0 {
				t.Fatalf("TLS refusal did not precede credentials: error=%T calls=%d", err, credentials.Load())
			}
		})
	}
}

// TestPacketModes verifies required wire modes against explicitly configured profiles.
func TestPacketModes(t *testing.T) {
	selected, err := integrationtest.Profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.Name, func(t *testing.T) {
			base := integrationtest.ProfileConfig(t, p)
			for _, tc := range []struct {
				name           string
				protocol       oceanbase.ProtocolOptions
				requireSuccess bool
			}{
				{"standard", oceanbase.ProtocolOptions{Compression: oceanbase.FeatureOff, OB20: oceanbase.FeatureOff, Checksum: oceanbase.FeatureOff, NewMetadata: oceanbase.FeatureOff, ProxyChecksum: oceanbase.FeatureOff}, true},
				{"auto", oceanbase.ProtocolOptions{}, true},
				{"compression", oceanbase.ProtocolOptions{Compression: oceanbase.FeatureRequired, OB20: oceanbase.FeatureOff}, true},
				{"ob20", oceanbase.ProtocolOptions{OB20: oceanbase.FeatureRequired}, strings.HasPrefix(p.Name, "proxy-")},
				{"checksum", oceanbase.ProtocolOptions{OB20: oceanbase.FeatureRequired, Checksum: oceanbase.FeatureRequired}, strings.HasPrefix(p.Name, "proxy-")},
				{"metadata", oceanbase.ProtocolOptions{NewMetadata: oceanbase.FeatureRequired}, p.Mode == oceanbase.ModeOracle},
				{"proxy-checksum", oceanbase.ProtocolOptions{ProxyChecksum: oceanbase.FeatureRequired}, strings.HasPrefix(p.Name, "proxy-")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := base
					cfg.Protocol = tc.protocol
					connector, err := oceanbase.NewConnector(cfg)
					if err != nil {
						integrationtest.FailOperation(t, "packet config", err)
					}
					db := sql.OpenDB(connector)
					db.SetMaxOpenConns(1)
					defer func() { _ = db.Close() }()
					ctx := integrationtest.TestContext(t)
					if err = db.PingContext(ctx); err != nil {
						if errors.Is(err, oceanbase.ErrUnsupported) && tc.name != "standard" && tc.name != "auto" && (!tc.requireSuccess || os.Getenv("OB_TEST_REQUIRE") != "1") {
							t.Skip("required capability explicitly rejected; framed business execution not verified")
						}
						integrationtest.FailOperation(t, "packet negotiation", err)
					}
					query := integrationtest.SelectParameterSQL
					if p.Mode == oceanbase.ModeOracle {
						query += integrationtest.OracleDualSuffix
					}
					for range 3 {
						var got string
						if err = db.QueryRowContext(ctx, query, "中🙂").Scan(&got); err != nil {
							integrationtest.FailOperation(t, "framed prepared query", err)
						}
						if got != "中🙂" {
							t.Fatal("packet round trip changed value")
						}
					}
				})
			}
		})
	}
}
