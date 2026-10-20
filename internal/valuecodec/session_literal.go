package valuecodec

import (
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
)

// SessionInteger returns the canonical decimal form of an exact int64 or uint64 input.
func SessionInteger(value string) (string, bool) {
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return strconv.FormatInt(n, 10), true
	}
	if n, err := strconv.ParseUint(value, 10, 64); err == nil {
		return strconv.FormatUint(n, 10), true
	}
	return "", false
}

// QuoteSessionValue quotes a session string using the negotiated MySQL escape mode.
func QuoteSessionValue(mode config.Mode, status uint16, value string) string {
	if mode == config.ModeMySQL && status&512 == 0 {
		value = strings.ReplaceAll(value, "\\", "\\\\")
	}
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// SessionVariableLiteral uses an integer literal only when the current variable is numeric.
func SessionVariableLiteral(mode config.Mode, status uint16, value string, columnType byte) string {
	if integer, ok := SessionInteger(value); ok {
		switch columnType {
		case 0, 1, 2, 3, 4, 5, 8, 9, 13, 246:
			return integer
		}
	}
	return QuoteSessionValue(mode, status, value)
}
