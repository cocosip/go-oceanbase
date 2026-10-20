package parameters

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// NamedArguments projects argument names and ordinals without resolving or converting values.
func NamedArguments(args []any) ([]driver.NamedValue, error) {
	values := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		value := driver.NamedValue{Ordinal: i + 1, Value: arg}
		if named, ok := arg.(sql.NamedArg); ok {
			if named.Name != "" {
				r, _ := utf8.DecodeRuneInString(named.Name)
				if !unicode.IsLetter(r) {
					return nil, fmt.Errorf("%w: argument name", dberrors.ErrBind)
				}
			}
			value.Name, value.Value = named.Name, named.Value
		}
		values[i] = value
	}
	return values, nil
}
