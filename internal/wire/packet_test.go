package wire

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testutil"
)

type bufferConn struct {
	in    *bytes.Reader
	out   bytes.Buffer
	reads int
}

func newBufferConn(data []byte) *bufferConn          { return &bufferConn{in: bytes.NewReader(data)} }
func (c *bufferConn) Read(b []byte) (int, error)     { c.reads++; return c.in.Read(b) }
func (c *bufferConn) Write(b []byte) (int, error)    { return c.out.Write(b) }
func (*bufferConn) Close() error                     { return nil }
func (*bufferConn) LocalAddr() net.Addr              { return nil }
func (*bufferConn) RemoteAddr() net.Addr             { return nil }
func (*bufferConn) SetDeadline(time.Time) error      { return nil }
func (*bufferConn) SetReadDeadline(time.Time) error  { return nil }
func (*bufferConn) SetWriteDeadline(time.Time) error { return nil }

func TestPacketTransportUpgrade(t *testing.T) {
	first := newBufferConn([]byte{1, 0, 0, 0, 'a'})
	p := NewPacketIO(first, 10)
	if _, err := p.ReadMessage(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := newBufferConn([]byte{1, 0, 0, 1, 'b'})
	if err := p.ReplaceConn(second); err != nil {
		t.Fatal(err)
	}
	if b, err := p.ReadMessage(context.Background()); err != nil || string(b) != "b" {
		t.Fatalf("sequence lost at upgrade: %q %v", b, err)
	}
	if _, err := p.ReadMessage(context.Background()); err == nil {
		t.Fatal("expected EOF")
	}
	if err := p.ReplaceConn(first); !errors.Is(err, ErrDesynchronized) {
		t.Fatal("upgrade revived failed stream", err)
	}
}

func TestPacketRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name             string
		payload, encoded []byte
	}{
		{"empty", nil, []byte{0, 0, 0, 0}},
		{"bytes", []byte{0xaa, 0xbb, 0xcc}, []byte{3, 0, 0, 0, 0xaa, 0xbb, 0xcc}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := newBufferConn(nil)
			if err := NewPacketIO(conn, 64<<20).WriteMessage(ctx, tc.payload); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(conn.out.Bytes(), tc.encoded) {
				t.Fatalf("wrong packet: %x", conn.out.Bytes())
			}
			got, err := NewPacketIO(newBufferConn(tc.encoded), 64<<20).ReadMessage(ctx)
			if err != nil || !bytes.Equal(got, tc.payload) {
				t.Fatalf("read %x: %v", got, err)
			}
		})
	}
	for _, size := range []int{0xffffff, 0x1000000} {
		payload := bytes.Repeat([]byte{0x5a}, size)
		conn := newBufferConn(nil)
		if err := NewPacketIO(conn, 64<<20).WriteMessage(ctx, payload); err != nil {
			t.Fatal(err)
		}
		encoded := conn.out.Bytes()
		if !bytes.Equal(encoded[:4], []byte{255, 255, 255, 0}) {
			t.Fatal("wrong full packet header")
		}
		wantTail := []byte{0, 0, 0, 1}
		if size == 0x1000000 {
			wantTail = []byte{1, 0, 0, 1, 0x5a}
		}
		if !bytes.Equal(encoded[4+0xffffff:], wantTail) {
			t.Fatal("missing terminal packet or wrong continuation sequence")
		}
		got, err := NewPacketIO(newBufferConn(encoded), 64<<20).ReadMessage(ctx)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("large message: %v", err)
		}
	}
}

func TestPacketLimit(t *testing.T) {
	conn := newBufferConn([]byte{5, 0, 0, 0})
	p := NewPacketIO(conn, 4)
	if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("want length rejection, got %v", err)
	}
	if conn.reads != 1 {
		t.Fatal("payload read before checking limit")
	}
	conn = newBufferConn(nil)
	p = NewPacketIO(conn, 4)
	if err := p.WriteMessage(context.Background(), []byte{1, 2, 3, 4, 5}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal(err)
	}
	if conn.out.Len() != 0 {
		t.Fatal("oversized message partially sent")
	}
	if err := p.WriteMessage(context.Background(), []byte{9}); err != nil {
		t.Fatal("pre-send rejection poisoned connection", err)
	}
	if !bytes.Equal(conn.out.Bytes(), []byte{1, 0, 0, 0, 9}) {
		t.Fatal("pre-send rejection changed sequence")
	}
	if _, err := messageSize(math.MaxInt, 1, math.MaxInt64); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatal("native int overflow not rejected")
	}
}

func TestPacketSequence(t *testing.T) {
	conn := newBufferConn([]byte{0, 0, 0, 1})
	p := NewPacketIO(conn, 64)
	if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrSequence) {
		t.Fatal(err)
	}
	p.ResetSequence()
	if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrDesynchronized) {
		t.Fatal("sequence reset revived corrupt stream", err)
	}
	conn = newBufferConn(nil)
	p = NewPacketIO(conn, 64)
	for i := 0; i < 257; i++ {
		if err := p.WriteMessage(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	data := conn.out.Bytes()
	if data[4*255+3] != 255 || data[4*256+3] != 0 {
		t.Fatal("sequence did not wrap")
	}
	p.ResetSequence()
	if err := p.WriteMessage(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if conn.out.Bytes()[4*257+3] != 0 {
		t.Fatal("new command sequence not reset")
	}
}

func TestPacketShortIO(t *testing.T) {
	ctx := context.Background()
	want := []byte{3, 0, 0, 0, 1, 2, 3}
	conn := newBufferConn(want)
	short := testutil.NewFaultConn(conn, testutil.Faults{MaxRead: 1, MaxWrite: 1})
	p := NewPacketIO(short, 64)
	got, err := p.ReadMessage(ctx)
	if err != nil || !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("short reads: %x %v", got, err)
	}
	p.ResetSequence()
	if err = p.WriteMessage(ctx, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(conn.out.Bytes(), want) {
		t.Fatal("short writes lost bytes")
	}
	conn = newBufferConn(nil)
	p = NewPacketIO(testutil.NewFaultConn(conn, testutil.Faults{MaxWrite: 2, WriteFailAt: 2}), 64)
	err = p.WriteMessage(ctx, []byte{1, 2, 3})
	var transfer *IOError
	if !errors.As(err, &transfer) || transfer.Bytes != 2 || !errors.Is(err, testutil.ErrInjected) {
		t.Fatalf("lost partial-send evidence: %v", err)
	}
	if err = p.WriteMessage(ctx, nil); !errors.Is(err, ErrDesynchronized) {
		t.Fatal("partially sent stream reused", err)
	}
	if conn.out.Len() != 2 {
		t.Fatal("failed operation replayed")
	}
}

func TestPacketTruncated(t *testing.T) {
	for _, data := range [][]byte{{1, 0}, {3, 0, 0, 0, 1}} {
		p := NewPacketIO(newBufferConn(data), 64)
		if _, err := p.ReadMessage(context.Background()); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated response: %v", err)
		}
		if _, err := p.ReadMessage(context.Background()); !errors.Is(err, ErrDesynchronized) {
			t.Fatal(err)
		}
	}
}

func TestPacketCancellation(t *testing.T) {
	for _, write := range []bool{false, true} {
		client, server := net.Pipe()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		p := NewPacketIO(client, 64)
		var err error
		if write {
			err = p.WriteMessage(ctx, []byte{1})
		} else {
			_, err = p.ReadMessage(ctx)
		}
		cancel()
		if closeErr := client.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		if closeErr := server.Close(); closeErr != nil {
			t.Error(closeErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked I/O ignored context: %v", err)
		}
	}
}

type zeroWriteConn struct{ net.Conn }

func (zeroWriteConn) Write([]byte) (int, error) { return 0, nil }

func TestPacketNoProgress(t *testing.T) {
	p := NewPacketIO(zeroWriteConn{newBufferConn(nil)}, 64)
	err := p.WriteMessage(context.Background(), []byte{1})
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("zero write did not terminate: %v", err)
	}
	if err = p.WriteMessage(context.Background(), nil); !errors.Is(err, ErrDesynchronized) {
		t.Fatal(err)
	}
}

func TestPacketCanceledBeforeSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	conn := newBufferConn(nil)
	p := NewPacketIO(conn, 64)
	if err := p.WriteMessage(ctx, []byte{1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if conn.out.Len() != 0 {
		t.Fatal("canceled operation wrote bytes")
	}
	if err := p.WriteMessage(context.Background(), nil); err != nil {
		t.Fatal("pre-send cancellation poisoned stream", err)
	}
}

func TestPacketReadWriteSequence(t *testing.T) {
	conn := newBufferConn([]byte{0, 0, 0, 0})
	p := NewPacketIO(conn, 64)
	if _, err := p.ReadMessage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteMessage(context.Background(), []byte{1}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(conn.out.Bytes(), []byte{1, 0, 0, 1, 1}) {
		t.Fatal("read/write did not share sequence")
	}
}

func TestPacketCancelWithoutDeadline(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := NewPacketIO(client, 64).ReadMessage(ctx); result <- err }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel without deadline did not unblock read")
	}
}
