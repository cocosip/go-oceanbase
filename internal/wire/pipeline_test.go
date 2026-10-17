package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestPipeline(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(map[bool]string{false: "backpressure", true: "continuation"}[large], func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close(); _ = server.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			commands := [][]byte{[]byte("first"), []byte("second"), []byte("third")}
			if large {
				commands[1] = bytes.Repeat([]byte{7}, 0xffffff)
			}
			done := make(chan error, 1)
			go func() {
				peer := NewPacketIO(server, 32<<20)
				for i, command := range commands {
					peer.ResetSequence()
					b, err := peer.ReadMessage(ctx)
					if err == nil && !bytes.Equal(b, command) {
						err = ErrMalformed
					}
					if err == nil {
						err = peer.WriteMessage(ctx, []byte{byte(i)})
					}
					if err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			p := NewPacketIO(client, 32<<20)
			seen := 0
			writes, err := p.Pipeline(ctx, commands, func(i int) error {
				b, e := p.ReadMessage(ctx)
				if e != nil {
					return e
				}
				if !bytes.Equal(b, []byte{byte(i)}) {
					return ErrMalformed
				}
				seen++
				return nil
			})
			if err != nil || seen != 3 || len(writes) != 3 {
				t.Fatal(writes, seen, err)
			}
			for _, w := range writes {
				if !w.Attempted || w.Err != nil {
					t.Fatal(w)
				}
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestPipelineFailureJoinsWriter(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p := NewPacketIO(server, 64)
		_, err := p.ReadMessage(ctx)
		if err == nil {
			err = p.WriteMessage(ctx, []byte{0})
		}
		done <- err
	}()
	p := NewPacketIO(client, 64)
	writes, err := p.Pipeline(ctx, [][]byte{{1}, {2}, {3}}, func(int) error {
		_, e := p.ReadMessage(ctx)
		if e != nil {
			return e
		}
		return io.ErrUnexpectedEOF
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(writes) != 3 || !writes[0].Attempted {
		t.Fatal(writes, err)
	}
	if _, e := p.ReadMessage(ctx); !errors.Is(e, ErrDesynchronized) {
		t.Fatal(e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestPipelinePreflight(t *testing.T) {
	c := newBufferConn(nil)
	p := NewPacketIO(c, 1)
	if _, err := p.Pipeline(context.Background(), [][]byte{{1}, {1, 2}}, func(int) error { return nil }); !errors.Is(err, ErrMessageTooLarge) || c.out.Len() != 0 {
		t.Fatal(err, c.out.Len())
	}
}

func TestPipelineWritesAhead(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close(); _ = server.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p := NewPacketIO(server, 64)
		for range 3 {
			p.ResetSequence()
			if _, err := p.ReadMessage(ctx); err != nil {
				done <- err
				return
			}
		}
		for i := range 3 {
			p.sequence = 1
			if err := p.WriteMessage(ctx, []byte{byte(i)}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	p := NewPacketIO(client, 64)
	_, err := p.Pipeline(ctx, [][]byte{{1}, {2}, {3}}, func(i int) error {
		b, e := p.ReadMessage(ctx)
		if e == nil && !bytes.Equal(b, []byte{byte(i)}) {
			e = ErrMalformed
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
