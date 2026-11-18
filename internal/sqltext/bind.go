package sqltext

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// BindValues maps named or positional arguments onto plan slot order. resolve runs
// once per argument, so a slot reused by name still resolves its source once.
func BindValues(plan Plan, args []driver.NamedValue, resolve func(any) (any, error)) ([]any, error) {
	if len(args) != plan.NumInput {
		return nil, fmt.Errorf("%w: argument count", dberrors.ErrBind)
	}
	logical := make([]any, len(args))
	names := make(map[string]int, len(args))
	for i, arg := range args {
		if plan.Named {
			if arg.Name == "" {
				return nil, fmt.Errorf("%w: named argument required", dberrors.ErrBind)
			}
			key := strings.ToUpper(arg.Name)
			if _, ok := names[key]; ok {
				return nil, fmt.Errorf("%w: duplicate named argument", dberrors.ErrBind)
			}
			names[key] = i
		} else if arg.Name != "" {
			return nil, fmt.Errorf("%w: unexpected named argument", dberrors.ErrBind)
		}
		value := arg.Value
		if resolve != nil {
			resolved, err := resolve(value)
			if err != nil {
				return nil, err
			}
			value = resolved
		}
		logical[i] = value
	}
	values := make([]any, len(plan.Slots))
	for i, slot := range plan.Slots {
		index := slot.Index
		if plan.Named {
			var ok bool
			index, ok = names[slot.Name]
			if !ok {
				return nil, fmt.Errorf("%w: missing named argument", dberrors.ErrBind)
			}
		}
		values[i] = logical[index]
	}
	return values, nil
}
