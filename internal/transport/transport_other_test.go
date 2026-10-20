//go:build !windows

package transport

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

func TestPlatformTransportUnsupportedPipe(t *testing.T) {
	if raw, err := Dial(context.Background(), networkPipe, `\\.\pipe\oceanbase`); raw != nil || !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("native pipe on non-Windows", err)
	}
}

func TestPlatformTransportUnsupportedSharedMemory(t *testing.T) {
	if raw, err := Dial(context.Background(), networkSharedMemory, "MYSQL"); raw != nil || !errors.Is(err, dberrors.ErrUnsupported) {
		t.Fatal("native shared memory on non-Windows", err)
	}
}
