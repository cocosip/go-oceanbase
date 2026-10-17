package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ErrBind identifies SQL placeholder or argument validation failures before sending.
var ErrBind = errors.New("oceanbase: invalid SQL binding")

func bindArguments(plan bindPlan, args []driver.NamedValue) ([]boundValue, error) {
	if len(args) != plan.numInput {
		return nil, fmt.Errorf("%w: argument count", ErrBind)
	}
	values := make([]any, len(args))
	names := make(map[string]int, len(args))
	for i, arg := range args {
		if plan.named {
			if arg.Name == "" {
				return nil, fmt.Errorf("%w: named argument required", ErrBind)
			}
			key := strings.ToUpper(arg.Name)
			if _, ok := names[key]; ok {
				return nil, fmt.Errorf("%w: duplicate named argument", ErrBind)
			}
			names[key] = i
		} else if arg.Name != "" {
			return nil, fmt.Errorf("%w: unexpected named argument", ErrBind)
		}
		v, err := convertArgument(arg.Value)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	out := make([]boundValue, len(plan.slots))
	for i, slot := range plan.slots {
		index := slot.index
		if plan.named {
			var ok bool
			index, ok = names[slot.name]
			if !ok {
				return nil, fmt.Errorf("%w: missing named argument", ErrBind)
			}
		}
		out[i].value = values[index]
	}
	return out, nil
}

func convertArgument(value any) (any, error) {
	switch v := value.(type) {
	case *LongData:
		return v, v.validate()
	case CursorOut:
		return makeOutput(Out{Dest: v.Dest, Type: typeNameRefCursor})
	case sql.Out:
		return makeOutput(Out{Dest: v.Dest, In: v.In})
	case Out:
		return makeOutput(v)
	case *outBinding:
		return v, nil
	}
	// Preserve driver scalar identity and unsigned range inside standard nullable values.
	if nullable := reflect.ValueOf(value); nullable.IsValid() {
		typ := nullable.Type()
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ.PkgPath() == "database/sql" && strings.HasPrefix(typ.Name(), "Null[") {
			if nullable.Kind() == reflect.Pointer {
				if nullable.IsNil() {
					return nil, nil
				}
				nullable = nullable.Elem()
			}
			if !nullable.FieldByName("Valid").Bool() {
				return nil, nil
			}
			return convertArgument(nullable.FieldByName("V").Interface())
		}
	}
	switch value.(type) {
	case Object, Array, *Object, *Array, Number, NString, Raw, RowID, *Number, *NString, *Raw, *RowID,
		TimestampTZ, TimestampLTZ, IntervalYM, IntervalDS, *TimestampTZ, *TimestampLTZ, *IntervalYM, *IntervalDS:
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return nil, nil
			}
			return convertArgument(rv.Elem().Interface())
		}
		_, err := value.(driver.Valuer).Value()
		return value, err
	}
	if value == nil || driver.IsValue(value) {
		return value, nil
	}
	if v, ok := value.(driver.Valuer); ok {
		rv := reflect.ValueOf(v)
		// Match database/sql: a nil pointer to a value-receiver Valuer is SQL NULL.
		if rv.Kind() == reflect.Pointer && rv.IsNil() && rv.Type().Elem().Implements(reflect.TypeFor[driver.Valuer]()) {
			return nil, nil
		}
		result, err := v.Value()
		if err != nil {
			return nil, fmt.Errorf("oceanbase: parameter conversion: %w", err)
		}
		if !driver.IsValue(result) {
			return nil, fmt.Errorf("%w: Valuer returned unsupported value", ErrBind)
		}
		return result, nil
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, nil
		}
		return convertArgument(rv.Elem().Interface())
	}
	if rv.Kind() == reflect.Uint64 || rv.Kind() == reflect.Uint || rv.Kind() == reflect.Uintptr {
		return rv.Uint(), nil
	}
	result, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported parameter type", ErrBind)
	}
	return result, nil
}

func (*conn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case localInfileRequest, batchRequest, cursorMutationRequest, cursorOpenRequest, cursorFetchRequest, savepointRequest, executionRequest, metadataRequest, resourceRequest, lobOpenRequest, lobRequest, tableBlobOpenRequest:
		return nil
	}
	v, err := convertArgument(value.Value)
	if err == nil {
		value.Value = v
	}
	return err
}

var _ driver.NamedValueChecker = (*conn)(nil)
