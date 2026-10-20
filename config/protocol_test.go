package config

import (
	"testing"
)

func TestCompressionDefaults(t *testing.T) {
	if FeatureDefault != 0 || FeatureOff != 1 || FeatureRequired != 2 {
		t.Fatal("default/off/required numeric policy compatibility changed")
	}
	for _, tc := range []struct {
		query string
		want  FeatureMode
	}{
		{"", FeatureOff},
		{"compression=off", FeatureOff},
		{"compression=auto", FeatureAuto},
		{"compression=required", FeatureRequired},
	} {
		cfg, err := ParseDSN("oceanbase://u@localhost/?" + tc.query)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Protocol.Compression != tc.want || cfg.Protocol.OB20 != FeatureAuto || cfg.Protocol.NewMetadata != FeatureAuto || cfg.Protocol.Checksum != FeatureAuto || cfg.Protocol.ProxyChecksum != FeatureAuto || cfg.Protocol.FullLinkTrace != FeatureAuto {
			t.Fatalf("unexpected defaults for %s: %+v", tc.query, cfg.Protocol)
		}
	}
}

func TestPacketOptions(t *testing.T) {
	for _, opts := range []ProtocolOptions{{Compression: 4}, {Compression: 255}, {OB20: 255}, {Checksum: 255}, {NewMetadata: 255}, {Compression: FeatureRequired, OB20: FeatureRequired}, {OB20: FeatureOff, Checksum: FeatureRequired}} {
		if _, err := Normalize(Config{User: testUser, Protocol: opts}); err == nil {
			t.Fatal(opts)
		}
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?compression=off&ob20=required&checksum=auto&newMetadata=required")
	if err != nil || cfg.Protocol.Compression != FeatureOff || cfg.Protocol.OB20 != FeatureRequired || cfg.Protocol.NewMetadata != FeatureRequired {
		t.Fatal(cfg.Protocol, err)
	}
	if _, err = ParseDSN("oceanbase://u@localhost/?ob20=true"); err == nil {
		t.Fatal("non-policy accepted")
	}
}

func TestTraceOptions(t *testing.T) {
	for _, options := range []ProtocolOptions{{FullLinkTrace: 255}, {FullLinkTrace: FeatureRequired, OB20: FeatureOff}, {FullLinkTrace: FeatureRequired, Compression: FeatureRequired}} {
		if _, err := Normalize(Config{User: testUser, Protocol: options}); err == nil {
			t.Fatal("invalid trace policy accepted", options)
		}
	}
	for _, options := range []DiagnosticOptions{{SlowThreshold: -1}, {MaxRequests: -1}, {MaxRequests: 1025}, {MaxPackets: -1}, {MaxPackets: 1025}} {
		if _, err := Normalize(Config{User: testUser, Diagnostics: options}); err == nil {
			t.Fatal("invalid diagnostic limits accepted")
		}
	}
	cfg, err := ParseDSN("oceanbase://u@localhost/?fullLinkTrace=required")
	if err != nil || cfg.Protocol.FullLinkTrace != FeatureRequired {
		t.Fatal(cfg, err)
	}
	if _, err = ParseDSN("oceanbase://u@localhost/?fullLinkTrace=true"); err == nil {
		t.Fatal("non-policy accepted")
	}
}
