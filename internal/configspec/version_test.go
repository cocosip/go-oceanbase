package configspec

import "testing"

func TestSupportsVersionParsesOceanBaseVersions(t *testing.T) {
	tests := []struct {
		name                string
		version             string
		major, minor, patch uint32
		want                bool
	}{
		{name: "plain", version: "4.2.1.0", major: 4, minor: 2, patch: 1, want: true},
		{name: "v prefix", version: "v4.2.1.0", major: 4, minor: 2, patch: 1, want: true},
		{name: "mysql prefix uses oceanbase version", version: "5.7.25-OceanBase_CE-v4.2.1.0", major: 4, minor: 2, patch: 1, want: true},
		{name: "mysql prefix does not enable mysql version", version: "5.7.25-OceanBase_CE-v4.2.1.0", major: 5, minor: 0, patch: 0, want: false},
		{name: "future major", version: "v6.1.0.0", major: 4, minor: 2, patch: 1, want: true},
		{name: "prepare execute floor", version: "2.2.5", major: 2, minor: 2, patch: 5, want: true},
		{name: "unknown shorthand", version: "5.x", major: 5, minor: 0, patch: 0, want: false},
		{name: "missing oceanbase suffix", version: "5.7.25-OceanBase_CE", major: 5, minor: 7, patch: 25, want: false},
		{name: "empty", version: "", major: 4, minor: 0, patch: 0, want: false},
		{name: "invalid", version: "OceanBase_CE-unknown", major: 4, minor: 0, patch: 0, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SupportsVersion(tc.version, tc.major, tc.minor, tc.patch); got != tc.want {
				t.Fatalf("SupportsVersion(%q, %d.%d.%d) = %t, want %t", tc.version, tc.major, tc.minor, tc.patch, got, tc.want)
			}
		})
	}
}
