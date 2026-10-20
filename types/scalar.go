// Package types defines lossless OceanBase values for database/sql.
package types

import (
	"database/sql/driver"
	"slices"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// NString binds an Oracle national character string.
type NString string

// Raw binds Oracle RAW data; nil and empty have distinct values.
type Raw []byte

// RowID binds an Oracle row identifier.
type RowID string

// Scan reads UTF-8 text.
func (n *NString) Scan(src any) error {
	if n == nil {
		return dberrors.ErrType
	}
	text, err := valueformat.ScalarText(src)
	if err != nil {
		return err
	}
	*n = NString(text)
	return nil
}

// Value implements driver.Valuer.
func (n NString) Value() (driver.Value, error) { return valueformat.ScalarText(string(n)) }

// Scan copies binary data.
func (r *Raw) Scan(src any) error {
	if r == nil {
		return dberrors.ErrType
	}
	switch value := src.(type) {
	case nil:
		*r = nil
	case []byte:
		*r = slices.Clone(value)
	case string:
		*r = []byte(value)
	default:
		return dberrors.ErrType
	}
	return nil
}

// Value implements driver.Valuer.
func (r Raw) Value() (driver.Value, error) { return []byte(r), nil }

// Scan reads a row identifier.
func (r *RowID) Scan(src any) error {
	if r == nil {
		return dberrors.ErrType
	}
	text, err := valueformat.ScalarText(src)
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
		return nil, dberrors.ErrType
	}
	text := r
	if text[0] == '*' {
		text = text[1:]
		if text == "" {
			return nil, dberrors.ErrType
		}
	}
	for _, c := range text {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
			continue
		}
		return nil, dberrors.ErrType
	}
	return string(r), nil
}

func scanTyped[T ~string](dest *T, src any, validate func(T) (driver.Value, error)) error {
	if dest == nil {
		return dberrors.ErrType
	}
	text, err := valueformat.ScalarText(src)
	if err != nil {
		return err
	}
	if _, err = validate(T(text)); err != nil {
		return err
	}
	*dest = T(text)
	return nil
}
