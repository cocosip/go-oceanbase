package command

import (
	"errors"
	"math"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// Result preserves unsigned server command counts until driver conversion.
type Result struct {
	Affected, ID uint64
	Known        bool
}

// ErrCountOverflow identifies a count outside the database/sql result range.
var ErrCountOverflow = errors.New("oceanbase: result count exceeds int64")

// RowsAffected converts the affected count when the server supplied one.
func (r Result) RowsAffected() (int64, error) {
	if !r.Known {
		return 0, dberrors.ErrUnsupported
	}
	if r.Affected > math.MaxInt64 {
		return 0, ErrCountOverflow
	}
	return int64(r.Affected), nil
}

// LastInsertId converts a nonzero server insert identity.
func (r Result) LastInsertId() (int64, error) {
	if !r.Known || r.ID == 0 {
		return 0, dberrors.ErrUnsupported
	}
	if r.ID > math.MaxInt64 {
		return 0, ErrCountOverflow
	}
	return int64(r.ID), nil
}
