package types

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestTimestampScanTime(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range []*time.Location{time.UTC, newYork, time.FixedZone("", -30*60), time.FixedZone("UTC", 8*3600), time.FixedZone("invalid label", 8*3600), time.FixedZone("", -12*3600), time.FixedZone("", 14*3600)} {
		for _, nanos := range []int{0, 123456789} {
			t.Run(loc.String()+time.Duration(nanos).String(), func(t *testing.T) {
				want := time.Date(2024, 11, 3, 5, 30, 0, nanos, time.UTC).In(loc)
				var tz TimestampTZ
				var ltz TimestampLTZ
				for _, scanner := range []interface {
					sql.Scanner
					Time() (time.Time, error)
				}{&tz, &ltz} {
					if err := scanner.Scan(want); err != nil {
						t.Fatal(err)
					}
					got, err := scanner.Time()
					_, gotOffset := got.Zone()
					_, wantOffset := want.Zone()
					if err != nil || !got.Equal(want) || gotOffset != wantOffset {
						t.Fatalf("got %v (%v), want %v", got, err, want)
					}
					if loc == newYork && got.Location().String() != loc.String() {
						t.Fatal("known region lost", got)
					}
				}
				var nullableTZ sql.Null[TimestampTZ]
				var nullableLTZ sql.Null[TimestampLTZ]
				for _, scanner := range []sql.Scanner{&nullableTZ, &nullableLTZ} {
					if err := scanner.Scan(want); err != nil {
						t.Fatal(err)
					}
					if err := scanner.Scan(nil); err != nil {
						t.Fatal(err)
					}
				}
				if nullableTZ.Valid || nullableLTZ.Valid {
					t.Fatal("NULL became valid")
				}
			})
		}
	}
}

func TestTimestampScanTimeRejectsUnrepresentable(t *testing.T) {
	for _, value := range []time.Time{
		time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("seconds", 1)),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("east", 14*3600+60)),
		time.Date(2024, 1, 1, 0, 0, 0, 0, time.FixedZone("west", -12*3600-60)),
	} {
		tz, ltz := TimestampTZ("unchanged"), TimestampLTZ("unchanged")
		for _, scanner := range []sql.Scanner{&tz, &ltz} {
			if err := scanner.Scan(value); !errors.Is(err, dberrors.ErrType) {
				t.Fatalf("accepted %v: %v", value, err)
			}
		}
		if tz != "unchanged" || ltz != "unchanged" {
			t.Fatal("failed scan mutated destination")
		}
	}
}
