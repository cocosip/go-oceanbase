package parameters

import (
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/types"
)

func TestTypeBoundaryConversion(t *testing.T) {
	for _, text := range []string{"0", "-0.000", "12345678901234567890123456789012345678.123456789", "+.5", "1.", "1e-130", "-9.99E125", "18446744073709551615"} {
		value, err := normalize(types.Number(text))
		if err != nil || reflect.TypeOf(value) != reflect.TypeFor[types.Number]() {
			t.Fatal("lost numeric protocol type", value, err)
		}
	}
}

func TestScalarIdentityConversion(t *testing.T) {
	for _, value := range []any{types.NString("中文😀"), types.Raw{0, 255}, types.RowID("AAAPr9AAEAAAACXAAA")} {
		converted, err := normalize(value)
		if err != nil || !reflect.DeepEqual(value, converted) {
			t.Fatal("lost extension identity", value, converted, err)
		}
	}
	var np *types.Number
	if value, err := normalize(np); err != nil || value != nil {
		t.Fatal("nil extended pointer", value, err)
	}
}
