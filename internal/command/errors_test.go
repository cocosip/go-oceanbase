package command

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
)

// TestPreSendNotStarted locks that a command which never began is reported as
// unsent, while PreSend itself only claims what it can prove: bytes never left
// the client and the connection is still usable.
func TestPreSendNotStarted(t *testing.T) {
	refused := &notStarted{dberrors.ErrUnsupported}
	if !Unsent(refused) || !Unsent(context.Canceled) {
		t.Fatal("unsent command not classified as unsent")
	}
	if PreSend(refused) {
		t.Fatal("PreSend must not claim an unusable connection is reusable")
	}
	if !PreSend(context.Canceled) {
		t.Fatal("cancellation before sending must be pre-send")
	}
	if Unsent(errors.New("connection reset")) {
		t.Fatal("post-send failure must not be classified as unsent")
	}
}
