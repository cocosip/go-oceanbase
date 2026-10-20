package sqltext

import (
	"strings"

	"github.com/cocosip/go-oceanbase/config"
)

// QuoteIdentifier escapes an already validated identifier for the actual server mode.
func QuoteIdentifier(mode config.Mode, name string) string {
	quote := "`"
	if mode == config.ModeOracle {
		quote = "\""
	}
	return quote + strings.ReplaceAll(name, quote, quote+quote) + quote
}
