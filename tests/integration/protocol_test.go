//go:build integration

package integration

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

// TestPacketModes is opt-in against the four configured profiles; local validation only compiles it.
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
						if !tc.requireSuccess && errors.Is(err, oceanbase.ErrUnsupported) {
							t.Log("required capability explicitly rejected before business execution")
							return
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
