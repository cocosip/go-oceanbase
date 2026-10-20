package valuecodec_test

import (
	testing "testing"
)

func TestComplexDateRepresentation(t *testing.T) {
	const date = "2025-10-17 12:34:56"
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{date, true},
		{date + ".000000", true},
		{date + ".000001", false},
		{date + ".000000garbage", false},
		{date + ".0000000", false},
		{"2025-10-17 12:34:57", false},
		{"2025-10-17", false},
	} {
		if got := complexDateMatches(tc.value, date); got != tc.want {
			t.Errorf("complex DATE %q: got %v, want %v", tc.value, got, tc.want)
		}
	}
}
