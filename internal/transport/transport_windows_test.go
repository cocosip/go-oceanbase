//go:build windows

package transport

import (
	"context"
	"errors"
	"testing"
)

func TestPlatformTransportPipeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if raw, err := Dial(ctx, networkPipe, `\\.\pipe\go-oceanbase-missing`); !errors.Is(err, context.Canceled) || raw != nil {
		t.Fatal("canceled pipe dial", err)
	}
}
