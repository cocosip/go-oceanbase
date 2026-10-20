package lifetime

import (
	"context"

	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// Owners passes concrete resource owners to operations that require shared cleanup.
type Owners struct {
	Cursors *cursor.Manager
	LOBs    *lob.Manager
}

// Cleanup applies the same cleanup policy as the execution coordinator.
func (o *Owners) Cleanup(ctx context.Context, op *session.Operation, all bool, retained []session.ResourceRef) error {
	return Cleanup(ctx, op, o.Cursors, o.LOBs, all, retained)
}
