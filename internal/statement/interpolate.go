package statement

import (
	"fmt"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
)

// Interpolate encodes already-normalized client-prepared values using the shared bounded builder.
func Interpolate(op *session.Operation, plan sqltext.Plan, values []any) ([]byte, error) {
	for _, value := range values {
		if _, ok := value.(*streaminput.Source); ok {
			return nil, fmt.Errorf("%w: long data requires server preparation", dberrors.ErrUnsupported)
		}
	}
	return command.InterpolatePlan(op, plan, values)
}
