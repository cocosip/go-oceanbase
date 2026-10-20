package oceanbase

import (
	dberrors "github.com/cocosip/go-oceanbase/dberrors"
)

// Error preserves the server error code, SQLSTATE and message.
type Error = dberrors.Error

// UnsupportedTypeError identifies a type unsupported in this compatibility mode.
type UnsupportedTypeError = dberrors.UnsupportedTypeError

// ErrBind identifies SQL placeholder or argument validation failures before sending.
var ErrBind = dberrors.ErrBind

// ErrResource identifies a closed resource or a handle whose owning session has ended.
var ErrResource = dberrors.ErrResource
