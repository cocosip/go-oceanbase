package configspec

import (
	"testing"
	"time"
)

// TestSessionTimeZoneLocal rejects the process-local zone: its offset and name
// vary per host, so it cannot be pinned into a session time_zone value.
func TestSessionTimeZoneLocal(t *testing.T) {
	original := time.Local
	time.Local = time.FixedZone("Business", 8*3600)
	t.Cleanup(func() { time.Local = original })
	if _, ok := SessionTimeZone(time.Local); ok {
		t.Fatal("process-local zone must be rejected")
	}
	if zone, ok := SessionTimeZone(original); !ok || zone == "" {
		t.Fatalf("explicit zone rejected: %q %v", zone, ok)
	}
	if zone, ok := SessionTimeZone(time.UTC); !ok || zone != "+00:00" {
		t.Fatalf("UTC: %q %v", zone, ok)
	}
}
