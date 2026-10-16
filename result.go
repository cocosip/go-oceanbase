package oceanbase

import (
	"database/sql/driver"
	"errors"
	"math"
)

type result struct {
	affected, id uint64
	known        bool
}

var errCountOverflow = errors.New("oceanbase: result count exceeds int64")

func (r result) RowsAffected() (int64, error) {
	if !r.known {
		return 0, ErrUnsupported
	}
	if r.affected > math.MaxInt64 {
		return 0, errCountOverflow
	}
	return int64(r.affected), nil
}
func (r result) LastInsertId() (int64, error) {
	if !r.known {
		return 0, ErrUnsupported
	}
	if r.id > math.MaxInt64 {
		return 0, errCountOverflow
	}
	return int64(r.id), nil
}

var _ driver.Result = result{}
