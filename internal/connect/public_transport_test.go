package connect_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
)

func TestPlatformTransportUnixTLSGate(t *testing.T) {
	listener, path := testpeer.ListenUnix(t, testpeer.UnixTempDir(t), "tls.sock")
	runPlatformTLSGate(t, listener, config.Config{User: testUser, Password: testPassword, Network: networkUnix, Addr: path})
}

func TestPlatformTransportUnixDeadline(t *testing.T) {
	listener, path := testpeer.ListenUnix(t, testpeer.UnixTempDir(t), "timeout.sock")
	defer func() { _ = listener.Close() }()
	created, err := oceanbase.NewConnector(config.Config{User: testUser, Network: networkUnix, Addr: path, ConnectTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	clientDone := make(chan error, 1)
	go func() { _, e := created.Connect(context.Background()); clientDone <- e }()
	raw, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	if err = <-clientDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Unix handshake budget", err)
	}
	if err = raw.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = raw.Read(b[:]); !errors.Is(err, io.EOF) {
		t.Fatal("expired handshake retained transport", err)
	}
}
