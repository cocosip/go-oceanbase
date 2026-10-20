package execution

import (
	"context"
	"math"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
)

// LOB executes a published resource operation under the owning physical session.
func (e *Engine) LOB(ctx context.Context, ref session.ResourceRef, operation lob.Operation, offset, count int64, data []byte, text string, transactionSession bool) (lob.Result, error) {
	if offset < 0 || count < 0 || offset > math.MaxInt64-count {
		return lob.Result{}, dberrors.ErrLOB
	}
	op, err := e.acquire(ctx)
	if err != nil {
		return lob.Result{}, err
	}
	defer e.release(op)
	return e.lobs.Execute(ctx, op, ref, operation, offset, count, data, text, e.writable(op, transactionSession))
}
