package oceanbase

import (
	"database/sql/driver"
	"errors"
	"slices"
	"unicode/utf8"
)

// ErrType indicates an invalid scalar representation or conversion.
var ErrType = errors.New("oceanbase: invalid scalar value")

// NString binds an Oracle national character string.
type NString string

// Raw binds Oracle RAW data; nil and empty have distinct values.
type Raw []byte

// RowID binds an Oracle row identifier.
type RowID string

// Scan reads UTF-8 text.
func (n *NString) Scan(src any) error {
	if n == nil {
		return ErrType
	}
	text, err := scalarText(src)
	if err != nil {
		return err
	}
	*n = NString(text)
	return nil
}

// Value implements driver.Valuer.
func (n NString) Value() (driver.Value, error) { return scalarText(string(n)) }

// Scan copies binary data.
func (r *Raw) Scan(src any) error {
	if r == nil {
		return ErrType
	}
	switch value := src.(type) {
	case nil:
		*r = nil
	case []byte:
		*r = slices.Clone(value)
	case string:
		*r = []byte(value)
	default:
		return ErrType
	}
	return nil
}

// Value implements driver.Valuer.
func (r Raw) Value() (driver.Value, error) { return []byte(r), nil }

// Scan reads a row identifier.
func (r *RowID) Scan(src any) error {
	if r == nil {
		return ErrType
	}
	text, err := scalarText(src)
	if err != nil {
		return err
	}
	if _, err = RowID(text).Value(); err != nil {
		return err
	}
	*r = RowID(text)
	return nil
}

// Value implements driver.Valuer.
func (r RowID) Value() (driver.Value, error) {
	if r == "" {
		return nil, ErrType
	}
	for _, c := range r {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			continue
		}
		return nil, ErrType
	}
	return string(r), nil
}

func scalarText(src any) (string, error) {
	var text string
	switch value := src.(type) {
	case string:
		text = value
	case []byte:
		if value == nil {
			return "", ErrType
		}
		text = string(value)
	default:
		return "", ErrType
	}
	if !utf8.ValidString(text) {
		return "", ErrType
	}
	return text, nil
}

func scanTyped[T ~string](dest *T, src any, validate func(T) (driver.Value, error)) error {
	if dest == nil {
		return ErrType
	}
	text, err := scalarText(src)
	if err != nil {
		return err
	}
	if _, err = validate(T(text)); err != nil {
		return err
	}
	*dest = T(text)
	return nil
}
