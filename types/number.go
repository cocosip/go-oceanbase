package types

import (
	"database/sql/driver"
	"strconv"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// Number preserves decimal text without floating point conversion.
type Number string

// Scan reads an exact number.
func (n *Number) Scan(src any) error {
	if n == nil {
		return dberrors.ErrType
	}
	var text string
	var err error
	switch value := src.(type) {
	case int64:
		text = strconv.FormatInt(value, 10)
	case uint64:
		text = strconv.FormatUint(value, 10)
	default:
		text, err = valueformat.ScalarText(src)
	}
	if err != nil {
		return err
	}
	if _, _, err = valueformat.DecimalParts(text); err != nil {
		return err
	}
	*n = Number(text)
	return nil
}

// Value implements driver.Valuer.
func (n Number) Value() (driver.Value, error) {
	_, _, err := valueformat.DecimalParts(string(n))
	if err != nil {
		return nil, err
	}
	return string(n), nil
}
