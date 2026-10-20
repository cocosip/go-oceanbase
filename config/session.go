package config

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/configspec"
)

const (
	charsetUTF8MB4 = "utf8mb4"
	charsetBinary  = "binary"
	fieldNCharset  = "Session.NCharset"
)

func cloneSessionOptions(options SessionOptions) (SessionOptions, error) {
	if options.Charset == "" {
		options.Charset = charsetUTF8MB4
	}
	if options.Location == nil {
		options.Location = time.UTC
	}
	if strings.ContainsRune(options.NCharset, 0) {
		return SessionOptions{}, &ConfigError{Field: fieldNCharset}
	}
	options.Charset = strings.ToLower(options.Charset)
	if options.Collation != "" && (len(options.Collation) > 128 || !configspec.SessionVariableName(options.Collation)) {
		return SessionOptions{}, &ConfigError{Field: "Session.Collation"}
	}
	options.Collation = strings.ToLower(options.Collation)
	options.NCharset = strings.ToLower(options.NCharset)
	if options.NCharset != "" {
		if _, err := charset.Encode(options.NCharset, ""); err != nil || options.NCharset == charsetBinary {
			return SessionOptions{}, &ConfigError{Field: fieldNCharset}
		}
	}
	if _, ok := configspec.SessionTimeZone(options.Location); !ok {
		return SessionOptions{}, &ConfigError{Field: "Session.Location"}
	}
	options.Variables = maps.Clone(options.Variables)
	options.InitSQL = slices.Clone(options.InitSQL)
	for name, value := range options.Variables {
		if !configspec.SessionVariableName(name) || strings.ContainsRune(value, 0) {
			return SessionOptions{}, &ConfigError{Field: "Session.Variables"}
		}
	}
	for _, query := range options.InitSQL {
		if strings.TrimSpace(query) == "" || strings.ContainsRune(query, 0) {
			return SessionOptions{}, &ConfigError{Field: "Session.InitSQL"}
		}
	}
	return options, nil
}
