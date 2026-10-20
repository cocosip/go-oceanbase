package parameters

import (
	"database/sql/driver"
	"reflect"
	"testing"
	_ "time/tzdata"

	"github.com/cocosip/go-oceanbase/types"
)

func TestTemporalIdentityConversion(t *testing.T) {
	for _, value := range []driver.Valuer{
		types.TimestampTZ("2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT"),
		types.TimestampLTZ("2024-11-03 01:30:00.000000001 -05:00 America/New_York"),
		types.IntervalYM("-999999999-11"),
		types.IntervalDS("-999999999 23:59:59.123456789"),
	} {
		got, err := normalize(value)
		if err != nil || reflect.TypeOf(got) != reflect.TypeOf(value) {
			t.Fatal("lost temporal identity", got, err)
		}
	}
}
