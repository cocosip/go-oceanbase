package testutil

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"
)

type memoryConn struct{ data bytes.Buffer }

func (c *memoryConn) Read(b []byte) (int, error)     { return c.data.Read(b) }
func (c *memoryConn) Write(b []byte) (int, error)    { return c.data.Write(b) }
func (*memoryConn) Close() error                     { return nil }
func (*memoryConn) LocalAddr() net.Addr              { return nil }
func (*memoryConn) RemoteAddr() net.Addr             { return nil }
func (*memoryConn) SetDeadline(time.Time) error      { return nil }
func (*memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (*memoryConn) SetWriteDeadline(time.Time) error { return nil }

func TestFaultConn(t *testing.T) {
	base := &memoryConn{}
	c := NewFaultConn(base, Faults{MaxRead: 1, MaxWrite: 2, ReadFailAt: 2, WriteFailAt: 2})
	if n, err := c.Write([]byte{1, 2, 3}); n != 2 || err != nil {
		t.Fatalf("short write: %d %v", n, err)
	}
	if _, err := c.Write([]byte{9}); !errors.Is(err, ErrInjected) {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if n, err := c.Read(buf); n != 1 || err != nil || buf[0] != 1 {
		t.Fatalf("short read: %d %v", n, err)
	}
	if _, err := c.Read(buf); !errors.Is(err, ErrInjected) {
		t.Fatal(err)
	}
	reads, writes := c.Counts()
	if reads != 2 || writes != 2 || base.data.Len() != 1 {
		t.Fatal("fault counters or byte effects incorrect")
	}
}

func TestPeerScript(t *testing.T) {
	conn := &memoryConn{}
	_, err := conn.Write([]byte{3, 0, 0, 0, 0x03, 'o', 'k'})
	if err != nil {
		t.Fatal(err)
	}
	count, err := RunScript(conn, []Step{{Want: []byte{3, 0, 0, 0, 0x03, 'o', 'k'}, Reply: []byte{8, 9}}})
	if err != nil || count != 1 {
		t.Fatalf("expected command not observed: %d %v", count, err)
	}
	if !bytes.Equal(conn.data.Bytes(), []byte{8, 9}) {
		t.Fatal("script did not write expected reply")
	}
	conn = &memoryConn{}
	_, err = conn.Write([]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = RunScript(conn, []Step{{Want: []byte{8}}}); err == nil {
		t.Fatal("script accepted wrong command bytes")
	}
}

func TestFaultDelay(t *testing.T) {
	conn := NewFaultConn(&memoryConn{}, Faults{WriteDelay: time.Millisecond, ReadDelay: time.Millisecond})
	start := time.Now()
	if _, err := conn.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Millisecond {
		t.Fatal("write delay was ignored")
	}
	start = time.Now()
	if _, err := conn.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < time.Millisecond {
		t.Fatal("read delay was ignored")
	}
}
