package testpeer

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// PeerDial connects a bounded in-memory protocol peer through the normal dial contract.
func PeerDial(t *testing.T, serve func(context.Context, *wire.PacketIO), budget ...time.Duration) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	timeout := 3 * time.Second
	if len(budget) > 0 {
		timeout = budget[0]
	}
	return func(ctx context.Context, _, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		if err := server.SetDeadline(time.Now().Add(timeout)); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { _ = server.Close() }()
			serve(ctx, wire.NewPacketIO(AuthPeerConn{Conn: server}, 64<<20))
		}()
		t.Cleanup(func() { _ = client.Close(); _ = server.Close(); <-done })
		return client, nil
	}
}

// ServeConnect exchanges a handshake with the requested session status and capabilities.
func ServeConnect(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, offers ...uint64) bool {
	t.Helper()
	greeting := AuthGreeting(AuthNative, false)
	offset := bytes.IndexByte(greeting[1:], 0) + 2 + 4 + 8 + 1
	for _, offer := range offers {
		binary.LittleEndian.PutUint16(greeting[offset:], binary.LittleEndian.Uint16(greeting[offset:])|uint16(offer))
		binary.LittleEndian.PutUint16(greeting[offset+5:], binary.LittleEndian.Uint16(greeting[offset+5:])|uint16(offer>>16))
	}
	if err := p.WriteMessage(ctx, greeting); err != nil {
		t.Error(err)
		return false
	}
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return false
	}
	if err := p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
		t.Error(err)
		return false
	}
	return true
}

// ServeQuit requires COM_QUIT followed by the client transport closing.
func ServeQuit(t testing.TB, p *wire.PacketIO) {
	t.Helper()
	data, err := p.ReadMessage(context.Background())
	if err != nil || !bytes.Equal(data, []byte{1}) {
		t.Errorf("missing quit: %x %v", data, err)
		return
	}
	if _, err = p.ReadMessage(context.Background()); err == nil {
		t.Error("missing transport close")
	}
}

// RoutingTLSPeer supplies the buffered, full-duplex transport required by TLS alerts and writes.
func RoutingTLSPeer(ctx context.Context, t *testing.T, serve func(net.Conn)) (net.Conn, error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()
	client, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		return nil, err
	}
	server, err := listener.Accept()
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	if err = server.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() { defer close(done); defer func() { _ = server.Close() }(); serve(server) }()
	t.Cleanup(func() {
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = server.Close()
			<-done
		}
	})
	return client, nil
}
