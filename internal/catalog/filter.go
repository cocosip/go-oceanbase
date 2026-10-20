package catalog

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

// ExactMetadataPattern accepts an exact escaped dictionary filter.
func ExactMetadataPattern(pattern string) (string, bool) {
	var out strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '%', '_':
			return "", false
		case '/':
			i++
			if i == len(pattern) {
				return "", false
			}
		}
		out.WriteByte(pattern[i])
	}
	return out.String(), true
}

// ValidateRoutineFallback requires exact schema and routine filters for SHOW CREATE.
func ValidateRoutineFallback(f metadata.MetadataFilter) (schema, name string, err error) {
	schema, schemaOK := ExactMetadataPattern(f.SchemaPattern)
	name, nameOK := ExactMetadataPattern(f.NamePattern)
	if !schemaOK || !nameOK || name == "" {
		return "", "", fmt.Errorf("%w: old-server routine metadata requires exact schema and routine patterns", dberrors.ErrUnsupported)
	}
	if f.Query != "" || f.ReferencedSchemaPattern != "" || f.ReferencedNamePattern != "" || len(f.TableTypes) != 0 {
		return "", "", fmt.Errorf("%w: routine metadata filter", dberrors.ErrBind)
	}
	if strings.ContainsRune(schema+name, 0) || !utf8.ValidString(schema+name) {
		return "", "", fmt.Errorf("%w: routine identifier", dberrors.ErrBind)
	}
	return schema, name, nil
}

// MetadataLike matches a metadata pattern using the dictionary escape rules.
func MetadataLike(value, pattern string) bool {
	v, p := []rune(value), []rune(pattern)
	vi, pi, star, retry := 0, 0, -1, 0
	for vi < len(v) {
		if pi < len(p) && p[pi] == '%' {
			star, retry = pi, vi
			pi++
			continue
		}
		if pi < len(p) {
			ch, next, literal := p[pi], pi+1, false
			if ch == '/' {
				if next == len(p) {
					return false
				}
				ch, next, literal = p[next], next+1, true
			}
			if ch == v[vi] || ch == '_' && !literal {
				vi++
				pi = next
				continue
			}
		}
		if star < 0 {
			return false
		}
		retry++
		vi, pi = retry, star+1
	}
	for pi < len(p) && p[pi] == '%' {
		pi++
	}
	return pi == len(p)
}
