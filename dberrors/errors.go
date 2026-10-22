// Package dberrors defines server and runtime errors shared by the driver.
package dberrors

import (
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/routing"
)

// Runtime error classifications retain their original identities through root aliases.
var (
	ErrTLSRequired      = errors.New("oceanbase: authentication requires verified TLS")
	ErrModeMismatch     = errors.New("oceanbase: compatibility mode mismatch")
	ErrIdentityMismatch = errors.New("oceanbase: authenticated identity mismatch")
	ErrUnsupported      = errors.New("oceanbase: unsupported capability")
	ErrBind             = errors.New("oceanbase: invalid SQL binding")
	ErrOut              = errors.New("oceanbase: invalid output binding")
	ErrComplex          = errors.New("oceanbase: invalid complex value or type description")
	ErrCursor           = errors.New("oceanbase: invalid cursor operation")
	ErrCursorBusy       = errors.New("oceanbase: cursor has an active operation or row batch")
	ErrLOB              = errors.New("oceanbase: invalid LOB operation")
	ErrLongData         = errors.New("oceanbase: invalid or failed long data")
	ErrLocalInfile      = errors.New("oceanbase: refused or failed local infile")
	ErrResource         = errors.New("oceanbase: resource is no longer valid")
	ErrType             = errors.New("oceanbase: invalid scalar value")
	ErrCommitUnknown    = errors.New("oceanbase: commit outcome unknown")
	ErrTxCleanup        = errors.New("oceanbase: transaction completed but session restoration failed")
	ErrXA               = errors.New("oceanbase: XA error")
	ErrXAOutcomeUnknown = errors.New("oceanbase: XA outcome unknown; recover on a new connection")
	ErrConnectorClosed  = routing.ErrClosed
)

// Error preserves the server's error code, SQLSTATE and message in either mode.
// Message may contain SQL or sensitive values; the driver does not log it.
type Error struct {
	Code     uint32
	SQLState string
	Message  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("oceanbase: server error %d (%s): %s", e.Code, e.SQLState, e.Message)
}

// UnsupportedTypeError identifies a type whose decoding is not implemented for this mode.
type UnsupportedTypeError struct {
	Type byte
	Mode config.Mode
}

func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("oceanbase: unsupported type %d in compatibility mode %d", e.Type, e.Mode)
}
func (*UnsupportedTypeError) Unwrap() error { return ErrUnsupported }

// BatchError identifies the first input with an error and the number of confirmed successes.
// Index is -1 for a batch-level finalization error without an input index.
type BatchError struct {
	Index     int
	Completed int
	Err       error
}

func (e *BatchError) Error() string {
	return fmt.Sprintf("oceanbase: batch failed at input %d (%d succeeded)", e.Index, e.Completed)
}
func (e *BatchError) Unwrap() error { return e.Err }

// OutError identifies a logical parameter. Partial warns that a Scanner may have had side effects.
type OutError struct {
	Parameter int
	Partial   bool
	Err       error
}

func (e *OutError) Error() string {
	return fmt.Sprintf("oceanbase: output parameter %d (partial=%t): %v", e.Parameter, e.Partial, e.Err)
}
func (e *OutError) Unwrap() error { return e.Err }

// Is also classifies failures returned by a destination's Scanner as output errors.
func (e *OutError) Is(target error) bool { return target == ErrOut }

// XAError preserves the standard XA return code and any underlying server error.
// Codes include protocol -6, invalid -5, duplicate XID -8, RM error -3,
// rollback-only 100, deadlock rollback 102, and timeout rollback 106.
type XAError struct {
	Code int32
	Err  error
}

func (e *XAError) Error() string {
	return fmt.Sprintf("oceanbase: XA return code %d: %v", e.Code, e.Err)
}
func (e *XAError) Unwrap() error { return e.Err }

// Is classifies all XA return codes as ErrXA.
func (*XAError) Is(target error) bool { return target == ErrXA }
