package command

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// EncodeSQL validates SQL text and bounds its encoded bytes before command construction.
func EncodeSQL(query, charsetName string, limit int64) ([]byte, error) {
	if int64(utf8.RuneCountInString(query)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, fmt.Errorf("%w: invalid SQL text", dberrors.ErrBind)
	}
	if charsetName == "" {
		charsetName = "utf8mb4"
	}
	data, err := charset.Encode(charsetName, query)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	return data, nil
}
