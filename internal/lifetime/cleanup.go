// Package lifetime coordinates concrete resource cleanup and parent lease release.
package lifetime

import (
	"context"
	"errors"
	"slices"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/lob"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
)

// Close retires a resource before releasing the statement that may own its server identity.
func Close(ctx context.Context, op *session.Operation, cursors *cursor.Manager, lobs *lob.Manager, ref session.ResourceRef) error {
	var parent *statement.Use
	var err error
	switch ref.Kind() {
	case session.CursorResource:
		parent, err = cursors.Close(ctx, op, ref)
	case session.LOBResource:
		parent, err = lobs.Close(ctx, op, ref)
	default:
		return dberrors.ErrResource
	}
	op.Forget(ref)
	return errors.Join(err, parent.Release(ctx, op))
}

// Cleanup preserves pending outputs until their assignment, and includes expired published handles.
func Cleanup(ctx context.Context, op *session.Operation, cursors *cursor.Manager, lobs *lob.Manager, all bool, retained []session.ResourceRef) error {
	var err error
	for _, meta := range op.Resources(all) {
		if !all && slices.Contains(retained, meta.Ref) {
			continue
		}
		err = errors.Join(err, Close(ctx, op, cursors, lobs, meta.Ref))
	}
	return err
}
