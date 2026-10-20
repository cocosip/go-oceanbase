package streaminput

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// Infile retains the caller's authorization and original one-use state.
type Infile struct {
	Name     string
	Reader   io.Reader
	Open     func(context.Context, string) (io.ReadCloser, error)
	MaxBytes int64
	Timeout  time.Duration
	State    *atomic.Bool
}

// ValidInfileName checks a source name before any reader access.
func ValidInfileName(name string) bool {
	return len(name) > 0 && len(name) <= 4096 && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

// Validate rejects invalid or consumed input without reading or claiming it.
func (v *Infile) Validate() error {
	if v == nil || v.MaxBytes < 0 || v.Timeout < 0 || v.State == nil || v.State.Load() || v.Name != "" && !ValidInfileName(v.Name) {
		return dberrors.ErrLocalInfile
	}
	if v.Open != nil {
		if v.Reader != nil {
			return dberrors.ErrLocalInfile
		}
	} else if !ValidInfileName(v.Name) || NilReader(v.Reader) {
		return dberrors.ErrLocalInfile
	}
	return nil
}

// Claim atomically consumes the original one-use state at upload.
func (v *Infile) Claim() bool {
	return v != nil && v.State != nil && v.State.CompareAndSwap(false, true)
}

// InfileError prevents database/sql from replaying a consumed input after ErrBadConn.
func InfileError(err error) error {
	if err == nil {
		return nil
	}
	// database/sql must not replay a command after a callback or source failed.
	if errors.Is(err, driver.ErrBadConn) {
		return fmt.Errorf("%w: %v", dberrors.ErrLocalInfile, err)
	}
	return errors.Join(dberrors.ErrLocalInfile, err)
}
