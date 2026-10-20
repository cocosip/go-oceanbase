package parameters

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/types"
)

func normalize(value any) (any, error) {
	for {
		next, again, err := NormalizeStep(value)
		if err != nil || !again {
			return next, err
		}
		value = next
	}
}

func TestNullableScalarIdentity(t *testing.T) {
	values := []any{sql.Null[types.Number]{V: "1.25", Valid: true}, sql.Null[types.NString]{V: "中", Valid: true}, sql.Null[types.TimestampTZ]{V: "2026-01-02 03:04:05 +00:00", Valid: true}}
	want := []any{types.Number("1.25"), types.NString("中"), types.TimestampTZ("2026-01-02 03:04:05 +00:00")}
	for i, value := range values {
		got, err := normalize(value)
		if err != nil || !reflect.DeepEqual(got, want[i]) {
			t.Fatal("sql.Null lost scalar protocol identity", got, err)
		}
	}
	invalid := sql.Null[types.NString]{}
	if got, err := normalize(&invalid); err != nil || got != nil {
		t.Fatal(got, err)
	}
}

type countingValuer struct{ calls *int }

func (v countingValuer) Value() (driver.Value, error) { *v.calls++; return int64(42), nil }

type resultValuer struct {
	value driver.Value
	err   error
}

func (v resultValuer) Value() (driver.Value, error) { return v.value, v.err }

func TestNormalizeStepOrder(t *testing.T) {
	n := uint64(math.MaxUint64)
	if v, err := normalize(&n); err != nil || v != n {
		t.Fatal(v, err)
	}
	var nilValuer *countingValuer
	if v, err := normalize(nilValuer); err != nil || v != nil {
		t.Fatal(v, err)
	}
	var calls int
	if v, again, err := NormalizeStep(countingValuer{&calls}); err != nil || again || v != int64(42) || calls != 1 {
		t.Fatal(v, again, calls, err)
	}
	value := "deferred"
	if next, again, err := NormalizeStep(sql.Null[*string]{V: &value, Valid: true}); err != nil || !again || next != &value {
		t.Fatal(next, again, err)
	}
	if next, again, err := NormalizeStep(&value); err != nil || !again || next != value {
		t.Fatal(next, again, err)
	}
	// A custom Valuer cannot return a driver-specific named scalar.
	if v, again, err := NormalizeStep(resultValuer{value: types.NString("中")}); !errors.Is(err, dberrors.ErrBind) || again || v != nil {
		t.Fatal(v, again, err)
	}
	wantErr := errors.New("value failed")
	if _, again, err := NormalizeStep(resultValuer{err: wantErr}); !errors.Is(err, wantErr) || again {
		t.Fatal(again, err)
	}
	if _, _, err := NormalizeStep(make(chan int)); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal(err)
	}
}
