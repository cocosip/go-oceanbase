package parameters

import (
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/types"
)

// CloneKey validates an already-normalized primary-key value without invoking user conversion.
// Mutable binary keys are copied so the request retains its original value.
func CloneKey(value any) (any, error) {
	switch v := value.(type) {
	case []byte:
		if v == nil {
			return nil, dberrors.ErrLOB
		}
		return slices.Clone(v), nil
	case types.Raw:
		if v == nil {
			return nil, dberrors.ErrLOB
		}
		return slices.Clone(v), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, dberrors.ErrLOB
		}
	case string, int64, uint64, bool, time.Time, types.Number, types.NString, types.RowID, types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
	default:
		return nil, fmt.Errorf("%w: unsupported or NULL primary key value", dberrors.ErrLOB)
	}
	return value, nil
}
