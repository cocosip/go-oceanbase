package diagnostic

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ValidateTraceContext rejects invalid application trace fields before network activity.
func ValidateTraceContext(ctx context.Context) error {
	if err := wire.ValidateTraceApp(wire.TraceRequestFromContext(ctx).App); err != nil {
		return fmt.Errorf("%w: invalid trace info", dberrors.ErrBind)
	}
	return nil
}
