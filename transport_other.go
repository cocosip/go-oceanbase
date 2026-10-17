//go:build !windows

package oceanbase

import (
	"context"
	"fmt"
	"net"
)

func dialNamedPipe(context.Context, string) (net.Conn, error) {
	return nil, fmt.Errorf("%w: named pipe transport requires Windows", ErrUnsupported)
}
