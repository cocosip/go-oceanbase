package configspec

import (
	"regexp"
	"slices"
	"strconv"
)

var prepareExecuteVersion = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+)\.([0-9]+)\.([0-9]+)(?:\.[0-9]+)*(?:[^0-9.]|$)`)

// SupportsPrepareExecute reports support for the combined prepare/execute command.
func SupportsPrepareExecute(version string) bool { return SupportsVersion(version, 2, 2, 76) }

// SupportsVersion compares the first complete numeric version with the required minimum.
func SupportsVersion(version string, major, minor, patch uint32) bool {
	parts := prepareExecuteVersion.FindStringSubmatch(version)
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
