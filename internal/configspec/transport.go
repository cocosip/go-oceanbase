package configspec

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ValidSharedMemoryName validates an optional Windows namespace and UTF-16 length.
func ValidSharedMemoryName(name string) bool {
	var base string
	if strings.HasPrefix(name, `Local\`) {
		base = strings.TrimPrefix(name, `Local\`)
	} else {
		base = strings.TrimPrefix(name, `Global\`)
	}
	return base != "" && utf8.ValidString(name) && len(utf16.Encode([]rune(name))) <= 220 &&
		strings.TrimSpace(base) == base && !strings.ContainsAny(base, "\\\x00")
}

// ValidPipeAddress accepts a UTF-8 UNC named-pipe endpoint without traversal segments.
func ValidPipeAddress(addr string) bool {
	if !utf8.ValidString(addr) || !strings.HasPrefix(addr, `\\`) {
		return false
	}
	parts := strings.SplitN(addr[2:], `\`, 3)
	if len(parts) != 3 || parts[0] == "" || !strings.EqualFold(parts[1], "pipe") || parts[2] == "" || strings.ContainsAny(parts[0], "/\x00?:*<>|\"") {
		return false
	}
	for _, component := range strings.FieldsFunc(parts[2], func(r rune) bool { return r == '\\' || r == '/' }) {
		if component == "." || component == ".." {
			return false
		}
	}
	return !strings.ContainsRune(parts[2], 0)
}
