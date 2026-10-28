package command

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestDecodeServerError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		packet  []byte
		state   string
		message string
	}{
		{"protocol41", []byte{255, 0x15, 4, '#', '2', '8', '0', '0', '0', 'n', 'o'}, "28000", "no"},
		{"legacy", []byte{255, 0x15, 4, 'n', 'o'}, "HY000", "no"},
		{"empty message", []byte{255, 0x15, 4}, "HY000", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var server *dberrors.Error
			if err := DecodeServerError(tc.packet); !errors.As(err, &server) || server.Code != 1045 || server.SQLState != tc.state || server.Message != tc.message {
				t.Fatal(err)
			}
		})
	}
	for _, packet := range [][]byte{nil, {255}, {255, 0x15}, {0, 0x15, 4}, {255, 0x15, 4, '#'}, {255, 0x15, 4, '#', '2', '8', '0', '0'}} {
		if err := DecodeServerError(packet); !errors.Is(err, wire.ErrMalformed) {
			t.Fatalf("packet=%x err=%v", packet, err)
		}
	}
}

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
