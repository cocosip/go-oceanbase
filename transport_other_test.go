//go:build !windows

package oceanbase

import (
	"context"
	"errors"
	"testing"
)

func TestPlatformTransportUnsupportedPipe(t *testing.T) {
	if raw, err := dialTransport(context.Background(), networkPipe, `\\.\pipe\oceanbase`); raw != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatal("native pipe on non-Windows", err)
	}
}
