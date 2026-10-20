package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type readStartedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *readStartedConn) Read(data []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Read(data)
}

func TestReadTimeoutDeadlineUpdates(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	started := make(chan struct{})
	bounded := WithReadTimeout(&readStartedConn{Conn: client, started: started}, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := wire.NewPacketIO(bounded, 1024).ReadMessage(ctx); done <- err }()
	<-started
	// These are the concurrent writer's refresh and a caller's clear, while Read is blocked.
	if err := bounded.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := bounded.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) || ctx.Err() != nil {
			t.Fatal("active read lost its independent budget", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deadline refresh extended an active read")
	}
}

func TestReadTimeoutContext(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "cancel"}[canceled], func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			started := make(chan struct{})
			bounded := WithReadTimeout(&readStartedConn{Conn: client, started: started}, time.Second)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := wire.NewPacketIO(bounded, 1024).ReadMessage(ctx); done <- err }()
			<-started
			want := context.DeadlineExceeded
			if canceled {
				cancel()
				want = context.Canceled
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatal("read timeout displaced earlier context", err)
			}
		})
	}
}

func TestReadTimeoutFreshRead(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	bounded := WithReadTimeout(client, 50*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	packet := wire.NewPacketIO(bounded, 1024)
	peer := wire.NewPacketIO(server, 1024)
	done := make(chan error, 2)
	go func() { done <- peer.WriteMessage(ctx, []byte{'a'}) }()
	if data, err := packet.ReadMessage(ctx); err != nil || string(data) != "a" {
		t.Fatal(data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Idle application time must not consume the next network read's budget.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	go func() { done <- peer.WriteMessage(ctx, []byte{'b'}) }()
	if data, err := packet.ReadMessage(ctx); err != nil || string(data) != "b" {
		t.Fatal("old read deadline leaked into the next read", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadTimeoutDoesNotLimitWrites(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	bounded := WithReadTimeout(client, time.Nanosecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- wire.NewPacketIO(bounded, 1024).WriteMessage(ctx, []byte{'w'}) }()
	if data, err := wire.NewPacketIO(server, 1024).ReadMessage(ctx); err != nil || string(data) != "w" {
		t.Fatal("read timeout altered the write budget", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadTimeoutPartialMessage(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bounded := WithReadTimeout(client, 100*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		for _, b := range []byte{1, 0, 0, 0, 'x'} {
			timer := time.NewTimer(35 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				done <- ctx.Err()
				return
			}
			if _, err := server.Write([]byte{b}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if data, err := wire.NewPacketIO(bounded, 1024).ReadMessage(ctx); err != nil || string(data) != "x" {
		t.Fatal("read timeout incorrectly bounded the whole logical message", data, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
