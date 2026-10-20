package command

import (
	"context"

	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// RestoreAutocommit verifies a completed transaction before restoring the baseline.
func RestoreAutocommit(ctx context.Context, op *session.Operation) error {
	if op.Protocol().OK.Status&1 != 0 {
		return wire.ErrMalformed
	}
	if op.Protocol().OK.Status&2 == 0 {
		if _, err := ExecOK(ctx, op, []byte("\x03SET autocommit=1")); err != nil {
			return err
		}
	}
	if op.Protocol().OK.Status&3 != 2 {
		return wire.ErrMalformed
	}
	return nil
}
