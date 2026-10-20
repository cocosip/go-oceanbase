package call

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/types"
)

func (b *Binding) parameterNumber(fallback int) int {
	if b.parameter != 0 {
		return b.parameter
	}
	return fallback
}

func newScalarBinding(out Output) (*Binding, error) {
	v := reflect.ValueOf(out.Dest)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() || !v.Elem().CanSet() {
		return nil, dberrors.ErrOut
	}

	_, scanner := out.Dest.(sql.Scanner)
	inferred := inferOutputType(v.Elem().Type())
	if inferred == "" && !scanner {
		return nil, dberrors.ErrOut
	}
	out.Type = strings.ToUpper(strings.TrimSpace(out.Type))
	if out.Type == "" {
		out.Type = inferred
	}
	if !validOutputType(out.Type) || out.Length < 0 || out.Precision < 0 || out.Precision > 65 || out.Scale < -84 || out.Scale > 127 {
		return nil, dberrors.ErrOut
	}
	if out.Precision == 0 && out.Scale != 0 {
		return nil, dberrors.ErrOut
	}
	if out.Precision > 0 && out.Type != valueformat.TypeNameNumber && out.Type != valueformat.TypeNameDecimal {
		return nil, dberrors.ErrOut
	}
	bind := &Binding{Output: out, custom: scanner && inferred == ""}
	if out.In {
		if bind.custom {
			if _, ok := out.Dest.(driver.Valuer); !ok {
				return nil, dberrors.ErrOut
			}
		}

	}
	return bind, nil
}

func validOutputType(name string) bool {
	switch name {
	case valueformat.TypeNameObject, valueformat.TypeNameArray:
		return true
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameBigInt, "BIGINT UNSIGNED", valueformat.TypeNameBoolean, valueformat.TypeNameFloat, valueformat.TypeNameDouble, valueformat.TypeNameBinaryFloat, valueformat.TypeNameBinaryDouble,
		valueformat.TypeNameVarchar, valueformat.TypeNameVarchar2, valueformat.TypeNameChar, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameRaw, valueformat.TypeNameRowID, valueformat.TypeNameVarbinary, valueformat.TypeNameDate, valueformat.TypeNameDateTime, valueformat.TypeNameTimestamp,
		valueformat.TypeNameTimestampTZ, valueformat.TypeNameTimestampLTZ, valueformat.TypeNameIntervalYM, valueformat.TypeNameIntervalDS, valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return true
	}
	return false
}

func (b *Binding) validateMode(mode config.Mode) error {
	if b.Length > 0 {
		switch b.Type {
		case valueformat.TypeNameVarchar, valueformat.TypeNameVarchar2, valueformat.TypeNameChar, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameRaw, valueformat.TypeNameRowID, valueformat.TypeNameVarbinary, valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		default:
			return fmt.Errorf("%w: length requires a text or binary output type", dberrors.ErrOut)
		}
	}
	if b.resource != 0 && mode != config.ModeOracle {
		return dberrors.ErrUnsupported
	}
	if mode == config.ModeOracle {
		if b.Precision > 38 {
			return fmt.Errorf("%w: Oracle NUMBER precision exceeds 38", dberrors.ErrOut)
		}
		return nil
	}
	switch b.Type {
	case valueformat.TypeNameVarchar2, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameRaw, valueformat.TypeNameRowID, valueformat.TypeNameBinaryFloat, valueformat.TypeNameBinaryDouble, valueformat.TypeNameTimestampTZ, valueformat.TypeNameTimestampLTZ, valueformat.TypeNameIntervalYM, valueformat.TypeNameIntervalDS, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return fmt.Errorf("%w: output type in MySQL compatibility mode", dberrors.ErrUnsupported)
	}
	if b.Precision > 0 && (b.Scale < 0 || b.Scale > 30 || b.Scale > b.Precision) {
		return fmt.Errorf("%w: MySQL DECIMAL precision or scale", dberrors.ErrOut)
	}
	return nil
}

func inferOutputType(t reflect.Type) string {
	if t == reflect.TypeFor[types.Object]() {
		return valueformat.TypeNameObject
	}
	if t == reflect.TypeFor[types.Array]() {
		return valueformat.TypeNameArray
	}
	switch t {
	case reflect.TypeFor[types.Number]():
		return valueformat.TypeNameNumber
	case reflect.TypeFor[types.NString]():
		return valueformat.TypeNameNVarchar2
	case reflect.TypeFor[types.Raw]():
		return valueformat.TypeNameRaw
	case reflect.TypeFor[types.RowID]():
		return valueformat.TypeNameRowID
	case reflect.TypeFor[types.TimestampTZ]():
		return valueformat.TypeNameTimestampTZ
	case reflect.TypeFor[types.TimestampLTZ]():
		return valueformat.TypeNameTimestampLTZ
	case reflect.TypeFor[types.IntervalYM]():
		return valueformat.TypeNameIntervalYM
	case reflect.TypeFor[types.IntervalDS]():
		return valueformat.TypeNameIntervalDS
	case reflect.TypeFor[time.Time]():
		return valueformat.TypeNameTimestamp
	}
	if t.PkgPath() == "database/sql" {
		if strings.HasPrefix(t.Name(), "Null[") {
			if field, ok := t.FieldByName("V"); ok {
				return inferOutputType(field.Type)
			}
		}
		switch t.Name() {
		case "NullBool":
			return valueformat.TypeNameBoolean
		case "NullString":
			return valueformat.TypeNameVarchar
		case "NullInt64", "NullInt32", "NullInt16", "NullByte":
			return valueformat.TypeNameBigInt
		case "NullFloat64":
			return valueformat.TypeNameDouble
		case "NullTime":
			return valueformat.TypeNameTimestamp
		}
	}
	// A user Scanner owns its mutations; a scalar underlying type does not make it stageable.
	if reflect.PointerTo(t).Implements(reflect.TypeFor[sql.Scanner]()) {
		return ""
	}
	switch t.Kind() {
	case reflect.Bool:
		return valueformat.TypeNameBoolean
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return valueformat.TypeNameBigInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "BIGINT UNSIGNED"
	case reflect.Float32:
		return valueformat.TypeNameFloat
	case reflect.Float64:
		return valueformat.TypeNameDouble
	case reflect.String:
		return valueformat.TypeNameVarchar
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return valueformat.TypeNameVarbinary
		}
	}
	return ""
}

func (c *assignment) scanOutput(dest, source any) error {
	if scanner, ok := dest.(sql.Scanner); ok {
		if source != nil && inferOutputType(reflect.TypeOf(dest).Elem()) == valueformat.TypeNameTimestamp {
			var instant time.Time
			if err := c.scanOutput(&instant, source); err != nil {
				return err
			}
			source = instant
		}
		return scanner.Scan(source)
	}
	target := reflect.ValueOf(dest).Elem()
	if target.Type() == reflect.TypeFor[time.Time]() {
		if t, ok := source.(time.Time); ok {
			target.Set(reflect.ValueOf(t))
			return nil
		}
		text, err := outputText(source)
		if err != nil {
			return err
		}
		t, err := valuecodec.ParseRowTime(text, c.op.Config().Session.Location)
		if err != nil {
			if p, e := valueformat.ParseTimestamp(text); e == nil && !p.Local {
				t, err = p.Instant, nil
			}
		}
		if err != nil {
			return err
		}
		target.Set(reflect.ValueOf(t))
		return nil
	}
	if source == nil {
		if target.Kind() == reflect.Slice {
			target.SetZero()
			return nil
		}
		return fmt.Errorf("%w: NULL requires a nullable destination", dberrors.ErrOut)
	}
	text, err := outputText(source)
	if err != nil {
		return err
	}
	switch target.Kind() {
	case reflect.Bool:
		value, err := driver.Bool.ConvertValue(source)
		if err != nil {
			return err
		}
		target.SetBool(value.(bool))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value, err := strconv.ParseInt(text, 10, target.Type().Bits())
		if err != nil {
			return fmt.Errorf("%w: signed integer range or fraction", dberrors.ErrOut)
		}
		target.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(text, 10, target.Type().Bits())
		if err != nil {
			return fmt.Errorf("%w: unsigned integer range or fraction", dberrors.ErrOut)
		}
		target.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(text, target.Type().Bits())
		if err != nil {
			return fmt.Errorf("%w: floating point range", dberrors.ErrOut)
		}
		target.SetFloat(value)
	case reflect.String:
		target.SetString(text)
	case reflect.Slice:
		if b, ok := source.([]byte); ok {
			target.SetBytes(slices.Clone(b))
		} else {
			target.SetBytes([]byte(text))
		}
	default:
		return dberrors.ErrOut
	}
	return nil
}

func outputText(value any) (string, error) {
	switch v := value.(type) {
	case types.Number, types.NString, types.Raw, types.RowID, types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
		standard, err := v.(driver.Valuer).Value()
		if err != nil {
			return "", err
		}
		return outputText(standard)
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	}
	return "", dberrors.ErrOut
}

func (c *assignment) validateOutputValue(b *Binding, value driver.Value) error {
	if value == nil {
		return nil
	}
	if b.Length > 0 {
		text, err := outputText(value)
		if err != nil {
			return err
		}
		size := len(text)
		switch b.Type {
		case valueformat.TypeNameRaw, valueformat.TypeNameVarbinary, valueformat.TypeNameBlob:
		default:
			if !utf8.ValidString(text) {
				return fmt.Errorf("%w: output text is not UTF-8", dberrors.ErrOut)
			}
			size = utf8.RuneCountInString(text)
		}
		if int64(size) > b.Length {
			return fmt.Errorf("%w: output length", dberrors.ErrOut)
		}
	}
	if b.Type == valueformat.TypeNameNumber || b.Type == valueformat.TypeNameDecimal {
		text, err := outputText(value)
		if err != nil {
			return err
		}
		text, err = valueformat.PlainNumber(text, c.op.Config().Limits.MaxMessageBytes)
		if err != nil {
			return err
		}
		if b.Precision == 0 {
			return nil
		}
		integer, fraction, _ := strings.Cut(strings.TrimLeft(text, "+-"), ".")
		integer = strings.TrimLeft(integer, "0")
		fraction = strings.TrimRight(fraction, "0")
		if b.Scale >= 0 {
			digits := int64(len(integer)) + b.Scale
			if integer == "" {
				digits = int64(len(strings.TrimLeft(fraction, "0"))) + b.Scale - int64(len(fraction))
			}
			if int64(len(fraction)) > b.Scale || (integer != "" || fraction != "") && digits > b.Precision {
				return fmt.Errorf("%w: decimal precision or scale", dberrors.ErrOut)
			}
		} else {
			places := int(-b.Scale)
			if fraction != "" || len(integer) > 0 && (len(integer) < places || strings.Trim(integer[len(integer)-places:], "0") != "") || int64(max(0, len(integer)-places)) > b.Precision {
				return fmt.Errorf("%w: decimal precision or scale", dberrors.ErrOut)
			}
		}
	}
	return nil
}
