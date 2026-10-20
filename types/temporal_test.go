package types

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
	_ "time/tzdata"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestTemporalLossless(t *testing.T) {
	for _, tc := range []struct {
		scanner sql.Scanner
		value   driver.Valuer
		text    string
	}{
		{new(TimestampTZ), TimestampTZ("2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT"), "2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT"},
		{new(TimestampLTZ), TimestampLTZ("2024-11-03 01:30:00.000000001 -05:00 America/New_York"), "2024-11-03 01:30:00.000000001 -05:00 America/New_York"},
		{new(IntervalYM), IntervalYM("-999999999-11"), "-999999999-11"},
		{new(IntervalDS), IntervalDS("-999999999 23:59:59.123456789"), "-999999999 23:59:59.123456789"},
	} {
		if err := tc.scanner.Scan([]byte(tc.text)); err != nil {
			t.Fatal(tc.text, err)
		}
		v, err := tc.scanner.(driver.Valuer).Value()
		if err != nil || v != tc.text {
			t.Fatal(tc.text, v, err)
		}
		if err = tc.scanner.Scan(nil); !errors.Is(err, dberrors.ErrType) {
			t.Fatal(err)
		}
	}
	for _, value := range []driver.Valuer{
		TimestampTZ("2024-02-30 00:00:00 +00:00"), TimestampTZ("2024-01-01 00:00:00.1234567890 +00:00"),
		TimestampTZ("2024-01-01 00:00:00 +14:01"), TimestampTZ("2024-03-10 02:30:00 -05:00 America/New_York"),
		TimestampTZ("2024-01-01 00:00:00 +00:00 x';select"), TimestampLTZ("2024-01-01 00:00:00"),
		IntervalYM("1000000000-0"), IntervalYM("1-12"), IntervalYM("1--1"), IntervalYM("1-256"),
		IntervalYM("1-999999999"), IntervalYM("4294967296-0"), IntervalDS("1 24:00:00"),
		IntervalDS("1 00:00:00.1234567890"), IntervalDS("1000000000 00:00:00"),
		IntervalDS("4294967296 00:00:00"),
	} {
		if _, err := value.Value(); !errors.Is(err, dberrors.ErrType) {
			t.Fatal("invalid temporal accepted", value, err)
		}
	}
	first, err := TimestampTZ("2024-11-03 01:30:00 -04:00 America/New_York").Time()
	if err != nil {
		t.Fatal(err)
	}
	second, err := TimestampTZ("2024-11-03 01:30:00 -05:00 America/New_York").Time()
	if err != nil || second.Sub(first) != time.Hour {
		t.Fatal("DST overlap lost", first, second, err)
	}
}

func TestTemporalYearOne(t *testing.T) {
	value, err := TimestampLTZ("0001-01-01 00:00:00 UTC").Time()
	if err != nil || !value.IsZero() {
		t.Fatal("valid zero time rejected", value, err)
	}
}
