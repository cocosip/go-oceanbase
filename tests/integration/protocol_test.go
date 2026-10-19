//go:build integration

package integration

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestTLSDisabledEndpoint(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			if os.Getenv(strings.TrimSuffix(p.env, "_DSN")+"_TLS_DISABLED") != "1" {
				t.Skip("endpoint without SSL not configured")
			}
			cfg := profileConfig(t, p)
			cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			var credentials atomic.Int32
			cfg.Credentials = func(context.Context) (oceanbase.Credentials, error) {
				credentials.Add(1)
				return oceanbase.Credentials{}, errors.New("credentials requested before verified TLS")
			}
			connector, err := oceanbase.NewConnector(cfg)
			if err != nil {
				failOperation(t, "TLS-required config", err)
			}
			db := sql.OpenDB(connector)
			defer func() { _ = db.Close() }()
			if err = db.PingContext(testContext(t)); !errors.Is(err, oceanbase.ErrTLSRequired) || credentials.Load() != 0 {
				t.Fatalf("TLS refusal did not precede credentials: error=%T calls=%d", err, credentials.Load())
			}
		})
	}
}

// TestPacketModes verifies required wire modes against explicitly configured profiles.
func TestPacketModes(t *testing.T) {
	selected, err := profiles(os.Getenv("OB_TEST_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range selected {
		t.Run(p.name, func(t *testing.T) {
			base := profileConfig(t, p)
			for _, tc := range []struct {
				name           string
				protocol       oceanbase.ProtocolOptions
				requireSuccess bool
			}{
				{"standard", oceanbase.ProtocolOptions{Compression: oceanbase.FeatureOff, OB20: oceanbase.FeatureOff, Checksum: oceanbase.FeatureOff, NewMetadata: oceanbase.FeatureOff, ProxyChecksum: oceanbase.FeatureOff}, true},
				{"auto", oceanbase.ProtocolOptions{}, true},
				{"compression", oceanbase.ProtocolOptions{Compression: oceanbase.FeatureRequired, OB20: oceanbase.FeatureOff}, true},
				{"ob20", oceanbase.ProtocolOptions{OB20: oceanbase.FeatureRequired}, strings.HasPrefix(p.name, "proxy-")},
				{"checksum", oceanbase.ProtocolOptions{OB20: oceanbase.FeatureRequired, Checksum: oceanbase.FeatureRequired}, strings.HasPrefix(p.name, "proxy-")},
				{"metadata", oceanbase.ProtocolOptions{NewMetadata: oceanbase.FeatureRequired}, p.mode == oceanbase.ModeOracle},
				{"proxy-checksum", oceanbase.ProtocolOptions{ProxyChecksum: oceanbase.FeatureRequired}, strings.HasPrefix(p.name, "proxy-")},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := base
					cfg.Protocol = tc.protocol
					connector, err := oceanbase.NewConnector(cfg)
					if err != nil {
						failOperation(t, "packet config", err)
					}
					db := sql.OpenDB(connector)
					db.SetMaxOpenConns(1)
					defer func() { _ = db.Close() }()
					ctx := testContext(t)
					if err = db.PingContext(ctx); err != nil {
						if errors.Is(err, oceanbase.ErrUnsupported) && tc.name != "standard" && tc.name != "auto" && (!tc.requireSuccess || os.Getenv("OB_TEST_REQUIRE") != "1") {
							t.Skip("required capability explicitly rejected; framed business execution not verified")
						}
						failOperation(t, "packet negotiation", err)
					}
					query := selectParameterSQL
					if p.mode == oceanbase.ModeOracle {
						query += oracleDualSuffix
					}
					for range 3 {
						var got string
						if err = db.QueryRowContext(ctx, query, "中🙂").Scan(&got); err != nil {
							failOperation(t, "framed prepared query", err)
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
