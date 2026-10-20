//go:build windows

package transport

import (
	"context"
	"errors"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func dialNamedPipe(ctx context.Context, addr string) (net.Conn, error) {
	raw, err := winio.DialPipeContext(ctx, addr)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		// An absent pipe can recover on the next configured connection attempt.
		err = &net.OpError{Op: "dial", Net: networkPipe, Err: err}
	}
	return raw, err
}
