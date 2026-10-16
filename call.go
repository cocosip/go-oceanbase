package oceanbase

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Out supplies a database type when sql.Out's Go destination is insufficient.
// Length bounds characters (text) or bytes (binary). Precision and Scale constrain NUMBER/DECIMAL.
type Out struct {
	Dest      any
	In        bool
	Type      string
	Length    int64
	Precision int64
	Scale     int64
}

// ErrOut identifies output validation, cardinality and conversion failures.
var ErrOut = errors.New("oceanbase: invalid output binding")

// OutError identifies a logical parameter. Partial warns that a Scanner may have had side effects.
type OutError struct {
	Parameter int
	Partial   bool
	Err       error
}

func (e *OutError) Error() string {
	return fmt.Sprintf("oceanbase: output parameter %d (partial=%t): %v", e.Parameter, e.Partial, e.Err)
}
func (e *OutError) Unwrap() error { return e.Err }

// Is also classifies failures returned by a destination's Scanner as output errors.
func (e *OutError) Is(target error) bool { return target == ErrOut }

type outBinding struct {
	Out
	input     any
	custom    bool
	parameter int
}

func (b *outBinding) parameterNumber(fallback int) int {
	if b.parameter != 0 {
		return b.parameter
	}
	return fallback
}

func makeOutput(out Out) (*outBinding, error) {
	v := reflect.ValueOf(out.Dest)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() || !v.Elem().CanSet() {
		return nil, ErrOut
	}
	_, scanner := out.Dest.(sql.Scanner)
	inferred := inferOutputType(v.Elem().Type())
	if inferred == "" && !scanner {
		return nil, ErrOut
	}
	out.Type = strings.ToUpper(strings.TrimSpace(out.Type))
	if out.Type == "" {
		out.Type = inferred
	}
	if !validOutputType(out.Type) || out.Length < 0 || out.Precision < 0 || out.Precision > 65 || out.Scale < -84 || out.Scale > 127 {
		return nil, ErrOut
	}
	if out.Precision == 0 && out.Scale != 0 {
		return nil, ErrOut
	}
	if out.Precision > 0 && out.Type != typeNameNumber && out.Type != typeNameDecimal {
		return nil, ErrOut
	}
	bind := &outBinding{Out: out, custom: scanner && inferred == ""}
	if out.In {
		if bind.custom {
			if _, ok := out.Dest.(driver.Valuer); !ok {
				return nil, ErrOut
			}
		}
		var err error
		bind.input, err = convertArgument(out.Dest)
		if err != nil {
			return nil, fmt.Errorf("%w: INOUT initial value: %w", ErrOut, err)
		}
	}
	return bind, nil
}

func validOutputType(name string) bool {
	switch name {
	case typeNameNumber, typeNameDecimal, "INTEGER", typeNameBigInt, "BIGINT UNSIGNED", typeNameBoolean, typeNameFloat, typeNameDouble, typeNameBinaryFloat, typeNameBinaryDouble,
		typeNameVarchar, typeNameVarchar2, typeNameChar, typeNameNChar, typeNameNVarchar2, typeNameRaw, typeNameRowID, typeNameVarbinary, typeNameDate, typeNameDateTime, typeNameTimestamp,
		typeNameTimestampTZ, typeNameTimestampLTZ, typeNameIntervalYM, typeNameIntervalDS, typeNameBlob, typeNameClob, typeNameNClob:
		return true
	}
	return false
}

func (b *outBinding) validateMode(mode Mode) error {
	if b.Length > 0 {
		switch b.Type {
		case typeNameVarchar, typeNameVarchar2, typeNameChar, typeNameNChar, typeNameNVarchar2, typeNameRaw, typeNameRowID, typeNameVarbinary, typeNameBlob, typeNameClob, typeNameNClob:
		default:
			return fmt.Errorf("%w: length requires a text or binary output type", ErrOut)
		}
	}
	if mode == ModeOracle {
		if b.Precision > 38 {
			return fmt.Errorf("%w: Oracle NUMBER precision exceeds 38", ErrOut)
		}
		return nil
	}
	switch b.Type {
	case typeNameVarchar2, typeNameNChar, typeNameNVarchar2, typeNameRaw, typeNameRowID, typeNameBinaryFloat, typeNameBinaryDouble, typeNameTimestampTZ, typeNameTimestampLTZ, typeNameIntervalYM, typeNameIntervalDS, typeNameClob, typeNameNClob:
		return fmt.Errorf("%w: output type in MySQL compatibility mode", ErrUnsupported)
	}
	if b.Precision > 0 && (b.Scale < 0 || b.Scale > 30 || b.Scale > b.Precision) {
		return fmt.Errorf("%w: MySQL DECIMAL precision or scale", ErrOut)
	}
	return nil
}

func inferOutputType(t reflect.Type) string {
	switch t {
	case reflect.TypeFor[Number]():
		return typeNameNumber
	case reflect.TypeFor[NString]():
		return typeNameNVarchar2
	case reflect.TypeFor[Raw]():
		return typeNameRaw
	case reflect.TypeFor[RowID]():
		return typeNameRowID
	case reflect.TypeFor[TimestampTZ]():
		return typeNameTimestampTZ
	case reflect.TypeFor[TimestampLTZ]():
		return typeNameTimestampLTZ
	case reflect.TypeFor[IntervalYM]():
		return typeNameIntervalYM
	case reflect.TypeFor[IntervalDS]():
		return typeNameIntervalDS
	case reflect.TypeFor[time.Time]():
		return typeNameTimestamp
	}
	if t.PkgPath() == "database/sql" {
		if strings.HasPrefix(t.Name(), "Null[") {
			if field, ok := t.FieldByName("V"); ok {
				return inferOutputType(field.Type)
			}
		}
		switch t.Name() {
		case "NullBool":
			return typeNameBoolean
		case "NullString":
			return typeNameVarchar
		case "NullInt64", "NullInt32", "NullInt16", "NullByte":
			return typeNameBigInt
		case "NullFloat64":
			return typeNameDouble
		case "NullTime":
			return typeNameTimestamp
		}
	}
	// A user Scanner owns its mutations; a scalar underlying type does not make it stageable.
	if reflect.PointerTo(t).Implements(reflect.TypeFor[sql.Scanner]()) {
		return ""
	}
	switch t.Kind() {
	case reflect.Bool:
		return typeNameBoolean
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return typeNameBigInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "BIGINT UNSIGNED"
	case reflect.Float32:
		return typeNameFloat
	case reflect.Float64:
		return typeNameDouble
	case reflect.String:
		return typeNameVarchar
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return typeNameVarbinary
		}
	}
	return ""
}

func (c *conn) assignOutputs(bindings []*outBinding, values []driver.Value) error {
	if len(bindings) != len(values) {
		return ErrOut
	}
	staged := make([]reflect.Value, len(bindings))
	for i, b := range bindings {
		if err := c.validateOutputValue(b, values[i]); err != nil {
			return &OutError{Parameter: b.parameterNumber(i + 1), Err: err}
		}
		if b.custom {
			continue
		}
		target := reflect.ValueOf(b.Dest).Elem()
		temp := reflect.New(target.Type())
		temp.Elem().Set(target)
		if err := c.scanOutput(temp.Interface(), values[i]); err != nil {
			return &OutError{Parameter: b.parameterNumber(i + 1), Err: err}
		}
		staged[i] = temp.Elem()
	}
	for i, b := range bindings {
		if b.custom {
			if err := b.Dest.(sql.Scanner).Scan(values[i]); err != nil {
				return &OutError{Parameter: b.parameterNumber(i + 1), Partial: true, Err: err}
			}
		} else {
			reflect.ValueOf(b.Dest).Elem().Set(staged[i])
		}
	}
	return nil
}

func (c *conn) scanOutput(dest, source any) error {
	if scanner, ok := dest.(sql.Scanner); ok {
		if source != nil && inferOutputType(reflect.TypeOf(dest).Elem()) == typeNameTimestamp {
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
		t, err := c.parseRowTime(text)
		if err != nil {
			if p, e := parseTimestamp(text); e == nil && !p.local {
				t, err = p.instant, nil
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
		return fmt.Errorf("%w: NULL requires a nullable destination", ErrOut)
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
			return fmt.Errorf("%w: signed integer range or fraction", ErrOut)
		}
		target.SetInt(value)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value, err := strconv.ParseUint(text, 10, target.Type().Bits())
		if err != nil {
			return fmt.Errorf("%w: unsigned integer range or fraction", ErrOut)
		}
		target.SetUint(value)
	case reflect.Float32, reflect.Float64:
		value, err := strconv.ParseFloat(text, target.Type().Bits())
		if err != nil {
			return fmt.Errorf("%w: floating point range", ErrOut)
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
		return ErrOut
	}
	return nil
}

func outputText(value any) (string, error) {
	switch v := value.(type) {
	case Number, NString, Raw, RowID, TimestampTZ, TimestampLTZ, IntervalYM, IntervalDS:
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
	return "", ErrOut
}

func (c *conn) validateOutputValue(b *outBinding, value driver.Value) error {
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
		case typeNameRaw, typeNameVarbinary, typeNameBlob:
		default:
			if !utf8.ValidString(text) {
				return fmt.Errorf("%w: output text is not UTF-8", ErrOut)
			}
			size = utf8.RuneCountInString(text)
		}
		if int64(size) > b.Length {
			return fmt.Errorf("%w: output length", ErrOut)
		}
	}
	if b.Type == typeNameNumber || b.Type == typeNameDecimal {
		text, err := outputText(value)
		if err != nil {
			return err
		}
		text, err = plainNumber(text, c.cfg.Limits.MaxMessageBytes)
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
				return fmt.Errorf("%w: decimal precision or scale", ErrOut)
			}
		} else {
			places := int(-b.Scale)
			if fraction != "" || len(integer) > 0 && (len(integer) < places || strings.Trim(integer[len(integer)-places:], "0") != "") || int64(max(0, len(integer)-places)) > b.Precision {
				return fmt.Errorf("%w: decimal precision or scale", ErrOut)
			}
		}
	}
	return nil
}
