// Package transport opens raw driver connections and applies transport deadlines.
package transport

import (
	"context"
	"net"
)

const (
	networkPipe         = "pipe"
	networkSharedMemory = "sharedmemory"
)

// Dial opens a raw TCP, Unix socket, named pipe, or shared memory connection.
func Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if network == networkPipe {
		return dialNamedPipe(ctx, addr)
	}
	if network == networkSharedMemory {
		return dialSharedMemory(ctx, addr)
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}
