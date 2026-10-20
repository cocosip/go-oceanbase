// Package command owns raw command responses without publishing resources or releasing operations.
package command

import (
	"context"

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

// PreSend identifies failures for which no command bytes were sent.
func PreSend(err error) bool {
	return err == wire.ErrMessageTooLarge || err == context.Canceled || err == context.DeadlineExceeded
}
