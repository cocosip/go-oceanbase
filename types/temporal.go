package types

import (
	"database/sql/driver"
	"time"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// TimestampTZ preserves a timestamp's fraction, offset, region and abbreviation.
type TimestampTZ string

// TimestampLTZ retains a timestamp normalized to the explicitly configured session zone.
type TimestampLTZ string

// IntervalYM preserves a signed year and month interval without approximating a duration.
type IntervalYM string

// IntervalDS preserves a signed day and second interval with up to nine fractional digits.
type IntervalDS string

// Scan reads a canonical timestamp.
func (t *TimestampTZ) Scan(src any) error { return scanTyped(t, src, TimestampTZ.Value) }

// Value implements driver.Valuer.
func (t TimestampTZ) Value() (driver.Value, error) {
	_, err := valueformat.ParseTimestamp(string(t))
	if err != nil {
		return nil, err
	}
	return string(t), nil
}

// Time resolves the explicit offset and retains a known region's time zone.
func (t TimestampTZ) Time() (time.Time, error) {
	p, err := valueformat.ParseTimestamp(string(t))
	return p.Instant, err
}

// Scan reads a canonical session timestamp.
func (t *TimestampLTZ) Scan(src any) error { return scanTyped(t, src, TimestampLTZ.Value) }

// Value implements driver.Valuer.
func (t TimestampLTZ) Value() (driver.Value, error) {
	_, err := valueformat.ParseLTZ(string(t))
	if err != nil {
		return nil, err
	}
	return string(t), nil
}

// Time resolves the explicit session offset.
func (t TimestampLTZ) Time() (time.Time, error) {
	p, err := valueformat.ParseLTZ(string(t))
	if err != nil {
		return time.Time{}, err
	}
	if !p.Local {
		return p.Instant, nil
	}
	loc, err := time.LoadLocation(p.Region)
	if err != nil {
		return time.Time{}, dberrors.ErrType
	}
	return valueformat.ResolveLocal(p.Instant, loc)
}

// Scan reads a year and month interval.
func (i *IntervalYM) Scan(src any) error { return scanTyped(i, src, IntervalYM.Value) }

// Value implements driver.Valuer.
func (i IntervalYM) Value() (driver.Value, error) {
	_, err := valueformat.ParseInterval(string(i), false)
	if err != nil {
		return nil, err
	}
	return string(i), nil
}

// Scan reads a day and second interval.
func (i *IntervalDS) Scan(src any) error { return scanTyped(i, src, IntervalDS.Value) }

// Value implements driver.Valuer.
func (i IntervalDS) Value() (driver.Value, error) {
	_, err := valueformat.ParseInterval(string(i), true)
	if err != nil {
		return nil, err
	}
	return string(i), nil
}
