//go:build !windows

package transport

import (
	"context"
	"fmt"
	"net"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func dialNamedPipe(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: named pipe transport requires Windows", dberrors.ErrUnsupported)
}

func dialSharedMemory(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: shared memory transport requires Windows", dberrors.ErrUnsupported)
}
