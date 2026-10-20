package sqltext

import (
	"errors"
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strings"
)

// LocalImportStatement checks the supported LOCAL INFILE SQL shape.
func LocalImportStatement(query string, mode config.Mode, noBackslash bool) error {
	bad := fmt.Errorf("%w: Import requires a single LOAD DATA statement", dberrors.ErrUnsupported)
	// A double-quoted backslash must not hide a separator under ANSI_QUOTES.
	for _, ansiQuotes := range []bool{false, true} {
		tokens, err := LexSQL(query, mode, noBackslash, ansiQuotes)
		if err != nil {
			return errors.Join(bad, err)
		}
		if len(tokens) < 2 || tokens[0].Kind != 'w' || tokens[1].Kind != 'w' || !strings.EqualFold(query[tokens[0].Start:tokens[0].End], "LOAD") || !strings.EqualFold(query[tokens[1].Start:tokens[1].End], "DATA") {
			return bad
		}
		for i, token := range tokens {
			if token.Kind == ';' && i != len(tokens)-1 {
				return bad
			}
		}
	}
	return nil
}
