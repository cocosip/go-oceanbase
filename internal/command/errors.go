// Package command owns raw command responses without publishing resources or releasing operations.
package command

import (
	"context"
	"errors"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// DecodeServerError preserves the public server error contract.
func DecodeServerError(data []byte) error {
	result, err := wire.DecodeServerError(data)
	if err != nil {
		return err
	}
	return &dberrors.Error{Code: result.Code, SQLState: result.SQLState, Message: result.Message}
}

// notStarted marks a failure that prevented the command from beginning at all.
// Callers that must know whether bytes left the client use Unsent; PreSend keeps
// its narrower meaning because it also drives connection discarding.
type notStarted struct{ err error }

func (e *notStarted) Error() string { return e.err.Error() }
func (e *notStarted) Unwrap() error { return e.err }

// PreSend identifies failures for which no command bytes were sent and the
// connection is known to remain usable.
func PreSend(err error) bool {
	return err == wire.ErrMessageTooLarge || err == context.Canceled || err == context.DeadlineExceeded
}

// Unsent reports whether a command certainly never left the client, including
// refusals before any write. Unlike PreSend it makes no claim about whether the
// connection is reusable.
func Unsent(err error) bool {
	if PreSend(err) {
		return true
	}
	var refused *notStarted
	return errors.As(err, &refused)
}
