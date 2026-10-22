package configspec

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	oceanBaseMarker  = regexp.MustCompile(`(?i)oceanbase`)
	oceanBaseVersion = regexp.MustCompile(`(?i)oceanbase[^0-9]*v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)
	plainVersion     = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)
)

// SupportsPrepareExecute reports support for the combined prepare/execute command.
func SupportsPrepareExecute(version string) bool { return SupportsVersion(version, 2, 2, 76) }

// SupportsVersion compares the OceanBase version with the required minimum.
//
// ob_version() is authoritative. Some server builds include a MySQL-compatible
// prefix before the actual OceanBase version, so an OceanBase marker changes
// parsing to the version following that marker. Unknown or malformed values
// conservatively report false.
func SupportsVersion(version string, major, minor, patch uint32) bool {
	parts := versionParts(version)
	if parts == nil {
		return false
	}
	var numbers [3]uint32
	for i := range numbers {
		n, err := strconv.ParseUint(parts[i+1], 10, 32)
		if err != nil {
			return false
		}
		numbers[i] = uint32(n)
	}
	return slices.Compare(numbers[:], []uint32{major, minor, patch}) >= 0
}

func versionParts(version string) []string {
	if strings.TrimSpace(version) == "" {
		return nil
	}
	if oceanBaseMarker.MatchString(version) {
		return oceanBaseVersion.FindStringSubmatch(version)
	}
	return plainVersion.FindStringSubmatch(version)
}
