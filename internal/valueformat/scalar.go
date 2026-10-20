// Package valueformat defines shared scalar grammars and bounded representations.
package valueformat

import (
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// ScalarText accepts a non-NULL UTF-8 string or byte slice.
func ScalarText(src any) (string, error) {
	var text string
	switch value := src.(type) {
	case string:
		text = value
	case []byte:
		if value == nil {
			return "", dberrors.ErrType
		}
		text = string(value)
	default:
		return "", dberrors.ErrType
	}
	if !utf8.ValidString(text) {
		return "", dberrors.ErrType
	}
	return text, nil
}
