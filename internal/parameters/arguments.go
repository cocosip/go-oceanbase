// Package parameters normalizes and validates logical SQL arguments.
package parameters

import (
	"database/sql/driver"
	"fmt"
	"reflect"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/types"
)

// ArgumentSource preserves an argument until its original conversion point.
type ArgumentSource interface {
	Resolve() (any, error)
}

// NormalizeStep performs one generic conversion step. An again result must pass
// through the caller's extension recognition before the next generic step.
func NormalizeStep(value any) (next any, again bool, err error) {
	// Preserve driver scalar identity and unsigned range inside standard nullable values.
	if nullable := reflect.ValueOf(value); nullable.IsValid() {
		typ := nullable.Type()
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.PkgPath() == "database/sql" && strings.HasPrefix(typ.Name(), "Null[") {
			if nullable.Kind() == reflect.Pointer {
				if nullable.IsNil() {
					return nil, false, nil
				}
				nullable = nullable.Elem()
			}
			if !nullable.FieldByName("Valid").Bool() {
				return nil, false, nil
			}
			return nullable.FieldByName("V").Interface(), true, nil
		}
	}
	switch value.(type) {
	case types.Object, types.Array, *types.Object, *types.Array,
		types.Number, types.NString, types.Raw, types.RowID, *types.Number, *types.NString, *types.Raw, *types.RowID,
		types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS,
		*types.TimestampTZ, *types.TimestampLTZ, *types.IntervalYM, *types.IntervalDS:
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return nil, false, nil
			}
			return rv.Elem().Interface(), true, nil
		}
		_, err := value.(driver.Valuer).Value()
		return value, false, err
	}
	if value == nil || driver.IsValue(value) {
		return value, false, nil
	}
	if v, ok := value.(driver.Valuer); ok {
		rv := reflect.ValueOf(v)
		// Match database/sql: a nil pointer to a value-receiver Valuer is SQL NULL.
		if rv.Kind() == reflect.Pointer && rv.IsNil() && rv.Type().Elem().Implements(reflect.TypeFor[driver.Valuer]()) {
			return nil, false, nil
		}
		result, err := v.Value()
		if err != nil {
			return nil, false, fmt.Errorf("oceanbase: parameter conversion: %w", err)
		}
		if !driver.IsValue(result) {
			return nil, false, fmt.Errorf("%w: Valuer returned unsupported value", dberrors.ErrBind)
		}
		return result, false, nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false, nil
		}
		return rv.Elem().Interface(), true, nil
	}
	if rv.Kind() == reflect.Uint64 || rv.Kind() == reflect.Uint || rv.Kind() == reflect.Uintptr {
		return rv.Uint(), false, nil
	}
	result, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return nil, false, fmt.Errorf("%w: unsupported parameter type", dberrors.ErrBind)
	}
	return result, false, nil
}
