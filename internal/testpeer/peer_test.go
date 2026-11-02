package testpeer

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAuthPeerConnPreservesDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	if err := server.SetDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	// Bound the regression even if PacketIO clears the fixture deadline.
	timer := time.AfterFunc(time.Second, func() { _ = server.Close() })
	defer timer.Stop()
	p := wire.NewPacketIO(AuthPeerConn{Conn: server}, 0)
	if _, err := p.ReadMessage(context.Background()); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("fixture read deadline lost: %v", err)
	}
}
