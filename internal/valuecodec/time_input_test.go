package valuecodec

import (
	"errors"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestMySQLTimePrecision(t *testing.T) {
	options := config.SessionOptions{Location: time.UTC}
	for _, tc := range []struct {
		name  string
		value time.Time
		want  string
	}{
		{"whole-second", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), "2026-01-02 03:04:05.000000"},
		{"microseconds", time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), "2026-01-02 03:04:05.123456"},
		{"nanoseconds", time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC), "2026-01-02 03:04:05.123456"},
		{"sub-microsecond", time.Date(2026, 1, 2, 3, 4, 5, 999, time.UTC), "2026-01-02 03:04:05.000000"},
		{"no-round-up", time.Date(2026, 12, 31, 23, 59, 59, 999999999, time.UTC), "2026-12-31 23:59:59.999999"},
		{"before-unix-epoch", time.Date(1969, 12, 31, 23, 59, 59, 123456789, time.UTC), "1969-12-31 23:59:59.123456"},
		{"session-zone", time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.FixedZone("east", 8*3600)), "2026-01-01 19:04:05.123456"},
		{"zero-time", time.Time{}, "0001-01-01 00:00:00.000000"},
		{"maximum-year", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), "9999-12-31 23:59:59.999999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("binary", func(t *testing.T) {
				p, err := EncodeParameter(tc.value, config.ModeMySQL, options, 1024)
				if err != nil {
					t.Fatal(err)
				}
				if p.Type != 12 || p.LengthEncoded || len(p.Data) != 12 || p.Data[0] != 11 {
					t.Fatalf("invalid DATETIME parameter: %#v", p)
				}
				got, err := DecodeBinaryValue(wire.Column{Type: 12}, config.ModeMySQL, p.Data[1:])
				if err != nil || got != tc.want {
					t.Fatalf("got %v (%v), want %s", got, err, tc.want)
				}
			})
			t.Run("client", func(t *testing.T) {
				got, err := ClientLiteral(tc.value, config.ModeMySQL, options, 1024, 1024)
				if err != nil || got != "'"+tc.want+"'" {
					t.Fatalf("got %s (%v), want '%s'", got, err, tc.want)
				}
			})
		})
	}
}

func TestMySQLTimeRange(t *testing.T) {
	options := config.SessionOptions{Location: time.UTC}
	for _, value := range []time.Time{
		time.Date(0, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1, 1, 1, 0, 0, 0, 999, time.FixedZone("east", 8*3600)),
		time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.FixedZone("west", -8*3600)),
	} {
		if _, err := EncodeParameter(value, config.ModeMySQL, options, 1024); !errors.Is(err, dberrors.ErrBind) {
			t.Fatalf("binary accepted out-of-range time %v: %v", value, err)
		}
		if _, err := ClientLiteral(value, config.ModeMySQL, options, 1024, 1024); !errors.Is(err, dberrors.ErrBind) {
			t.Fatalf("client accepted out-of-range time %v: %v", value, err)
		}
	}
}
