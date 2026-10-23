package configspec

import (
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
)

// ProtocolAttributes builds the declared client and proxy capability attributes.
func ProtocolAttributes(metadata, proxyChecksum, ob20, compressionRequired, fullLinkTrace bool) map[string]string {
	client := uint64(1 | 4) // locator-v2 and JDBC binary double (avoid Oracle NUMBER conversion)
	if metadata {
		client |= 2
	}
	// The driver wire dialect makes OceanBase return negotiated capabilities in login OK.
	attrs := map[string]string{"__mysql_client_type": "__ob_jdbc_client", "__ob_client_attribute_capability_flag": strconv.FormatUint(client, 10)}
	// Capability alone never supplies file access; Import still requires an explicit Reader.
	proxy := uint64(1 << 20)
	if proxyChecksum {
		proxy |= 1<<3 | 1<<6
	}
	if ob20 && !compressionRequired {
		// Do not request extra-OK, session synchronization or routing capabilities not yet implemented.
		proxy |= 1<<8 | 1<<15
		if fullLinkTrace {
			proxy |= 1<<14 | 1<<18
		}
	}
	attrs["__proxy_capability_flag"] = strconv.FormatUint(proxy, 10)
	return attrs
}

const proxyUserAttribute = "__ob_client_proxy_user_name"

// ConnectionAttributes encodes identity and application attributes within the wire byte limit.
// It returns a fresh map and an empty field on success, or the invalid configuration field.
func ConnectionAttributes(base, configured map[string]string, proxyUser, charsetName string, mysql bool, limit int64) (map[string]string, string) {
	attrs := maps.Clone(base)
	if attrs == nil {
		attrs = make(map[string]string)
	}
	if proxyUser != "" {
		if mysql || strings.ContainsRune(proxyUser, 0) || int64(utf8.RuneCountInString(proxyUser)) > limit {
			return nil, "ProxyUser"
		}
		encoded, err := charset.Encode(charsetName, proxyUser)
		if err != nil {
			return nil, "ProxyUser"
		}
		attrs[proxyUserAttribute] = string(encoded)
	}
	if len(configured) == 0 && proxyUser == "" {
		return attrs, ""
	}
	invalid := "ConnectionAttributes"
	budget := limit
	// Every nonempty key requires at least three bytes including length prefixes.
	if int64(len(configured)) > budget/3 {
		return nil, invalid
	}
	remaining := budget
	consume := func(key, value string) bool {
		for _, text := range []string{key, value} {
			n := int64(len(text))
			prefix := attributeLengthBytes(n)
			if prefix > remaining || n > remaining-prefix {
				return false
			}
			remaining -= n + prefix
		}
		return true
	}
	for key, value := range attrs {
		if !consume(key, value) {
			return nil, invalid
		}
	}
	for key, value := range configured {
		if key == "" || strings.HasPrefix(key, "_") || key == "ob_capability_flag" || strings.ContainsRune(key, 0) || strings.ContainsRune(value, 0) || int64(utf8.RuneCountInString(key)) > remaining || int64(utf8.RuneCountInString(value)) > remaining {
			return nil, invalid
		}
		encodedKey, err := charset.Encode(charsetName, key)
		if err != nil {
			return nil, invalid
		}
		encodedValue, err := charset.Encode(charsetName, value)
		if err != nil {
			return nil, invalid
		}
		if _, exists := attrs[string(encodedKey)]; exists {
			return nil, invalid
		}
		if !consume(string(encodedKey), string(encodedValue)) {
			return nil, invalid
		}
		attrs[string(encodedKey)] = string(encodedValue)
	}
	if attributeLengthBytes(budget-remaining) > remaining {
		return nil, invalid
	}
	return attrs, ""
}

func attributeLengthBytes(n int64) int64 {
	switch {
	case n < 251:
		return 1
	case n < 1<<16:
		return 3
	case n < 1<<24:
		return 4
	default:
		return 9
	}
}
