//go:build windows

package testpeer

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	testSharedAnswer      = "_CONNECT_ANSWER"
	testSharedConnectData = "_CONNECT_DATA"
	networkSharedMemory   = "sharedmemory"
)

// SharedMemoryPeer owns the native events and mappings of an independent allocation server.
type SharedMemoryPeer struct {
	Name      string
	Handles   map[string]windows.Handle
	Views     map[string]uintptr
	Ready     chan struct{}
	Requested chan struct{}
}

// NewSharedMemoryPeer creates an allocation peer with one connection identity.
func NewSharedMemoryPeer(t *testing.T) *SharedMemoryPeer {
	return NewDelayedSharedMemoryPeer(t, nil)
}

// NewDelayedSharedMemoryPeer gates the allocation answer until the channel is released.
func NewDelayedSharedMemoryPeer(t *testing.T, gate <-chan struct{}) *SharedMemoryPeer {
	t.Helper()
	return NewSharedMemoryAllocator(t, gate, []int32{42})
}

// NewSharedMemoryAllocator creates a peer that allocates the supplied connection identities.
func NewSharedMemoryAllocator(t *testing.T, gate <-chan struct{}, ids []int32) *SharedMemoryPeer {
	t.Helper()
	p := &SharedMemoryPeer{Name: fmt.Sprintf("go-oceanbase-%d-%d", os.Getpid(), time.Now().UnixNano()), Handles: map[string]windows.Handle{}, Views: map[string]uintptr{}, Ready: make(chan struct{}), Requested: make(chan struct{})}
	stop := make(chan struct{})
	t.Cleanup(func() {
		for _, view := range p.Views {
			_ = windows.UnmapViewOfFile(view)
		}
		for _, handle := range p.Handles {
			_ = windows.CloseHandle(handle)
		}
	})
	events := []string{"_CONNECT_REQUEST", testSharedAnswer, "_TEST_STOP"}
	mappings := map[string]uint32{testSharedConnectData: 4}
	for _, id := range ids {
		prefix := fmt.Sprintf("_%d", id)
		for _, suffix := range []string{"_CLIENT_READ", "_SERVER_READ", "_SERVER_WROTE", "_CLIENT_WROTE", "_CONNECTION_CLOSED"} {
			events = append(events, prefix+suffix)
		}
		mappings[prefix+"_DATA"] = 16004
	}
	for _, suffix := range events {
		manual := uint32(0)
		if strings.HasSuffix(suffix, "_CONNECTION_CLOSED") || suffix == "_TEST_STOP" {
			manual = 1
		}
		name, err := windows.UTF16PtrFromString(p.Name + suffix)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateEvent(nil, manual, 0, name)
		if err != nil {
			t.Fatal(err)
		}
		p.Handles[suffix] = handle
	}
	for suffix, size := range mappings {
		name, err := windows.UTF16PtrFromString(p.Name + suffix)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateFileMapping(windows.InvalidHandle, nil, windows.PAGE_READWRITE, 0, size, name)
		if err != nil {
			t.Fatal(err)
		}
		p.Handles[suffix] = handle
		view, err := windows.MapViewOfFile(handle, windows.FILE_MAP_READ|windows.FILE_MAP_WRITE, 0, 0, uintptr(size))
		if err != nil {
			t.Fatal(err)
		}
		p.Views[suffix] = view
	}
	requestHandle, answerHandle, stopHandle := p.Handles["_CONNECT_REQUEST"], p.Handles[testSharedAnswer], p.Handles["_TEST_STOP"]
	connectView := p.Views[testSharedConnectData]
	go func() {
		defer close(p.Ready)
		for i, id := range ids {
			result, err := windows.WaitForMultipleObjects([]windows.Handle{stopHandle, requestHandle}, false, 3000)
			if result == windows.WAIT_OBJECT_0 && err == nil {
				return
			}
			if err != nil || result != windows.WAIT_OBJECT_0+1 {
				t.Error("allocation peer wait", result, err)
				return
			}
			if i == 0 {
				close(p.Requested)
			}
			if gate != nil {
				select {
				case <-gate:
				case <-stop:
				}
			}
			var data [4]byte
			binary.LittleEndian.PutUint32(data[:], uint32(id))
			if err := windows.WriteProcessMemory(windows.CurrentProcess(), connectView, &data[0], 4, nil); err != nil {
				t.Error(err)
				return
			}
			if err := windows.SetEvent(answerHandle); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	t.Cleanup(func() { close(stop); _ = windows.SetEvent(stopHandle); <-p.Ready })
	return p
}

// SharedMemoryServer exchanges data through the peer native events and mappings.
// It is independent of the client implementation.
type SharedMemoryServer struct {
	Peer    *SharedMemoryPeer
	pending []byte
	wrote   bool
}

func (s *SharedMemoryServer) wait(event string) error {
	result, err := windows.WaitForMultipleObjects([]windows.Handle{s.Peer.Handles[event], s.Peer.Handles["_42_CONNECTION_CLOSED"]}, false, 3000)
	if err != nil {
		return err
	}
	if result == windows.WAIT_OBJECT_0 {
		return nil
	}
	if result == windows.WAIT_OBJECT_0+1 {
		return io.EOF
	}
	return os.ErrDeadlineExceeded
}

// Read implements net.Conn for the independent native server.
func (s *SharedMemoryServer) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		if err := s.wait("_42_CLIENT_WROTE"); err != nil {
			return 0, err
		}
		var header [4]byte
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), s.Peer.Views["_42_DATA"], &header[0], 4, nil); err != nil {
			return 0, err
		}
		size := binary.LittleEndian.Uint32(header[:])
		if size == 0 || size > 16000 {
			return 0, fmt.Errorf("peer invalid payload %d", size)
		}
		s.pending = make([]byte, size)
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), s.Peer.Views["_42_DATA"]+4, &s.pending[0], uintptr(size), nil); err != nil {
			return 0, err
		}
		if err := windows.SetEvent(s.Peer.Handles["_42_SERVER_READ"]); err != nil {
			return 0, err
		}
	}
	n := copy(b, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// Write implements net.Conn for the independent native server.
func (s *SharedMemoryServer) Write(b []byte) (int, error) {
	n := 0
	for len(b) > 0 {
		if s.wrote {
			if err := s.wait("_42_CLIENT_READ"); err != nil {
				return n, err
			}
		}
		size := min(len(b), 16000)
		frame := make([]byte, size+4)
		binary.LittleEndian.PutUint32(frame, uint32(size))
		copy(frame[4:], b[:size])
		if err := windows.WriteProcessMemory(windows.CurrentProcess(), s.Peer.Views["_42_DATA"], &frame[0], uintptr(len(frame)), nil); err != nil {
			return n, err
		}
		if err := windows.SetEvent(s.Peer.Handles["_42_SERVER_WROTE"]); err != nil {
			return n, err
		}
		s.wrote = true
		n += size
		b = b[size:]
	}
	return n, nil
}

// Close implements net.Conn for the independent native server.
func (s *SharedMemoryServer) Close() error {
	return windows.SetEvent(s.Peer.Handles["_42_CONNECTION_CLOSED"])
}

// LocalAddr implements net.Conn for the independent native server.
func (s *SharedMemoryServer) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: s.Peer.Name, Net: networkSharedMemory}
}

// RemoteAddr implements net.Conn for the independent native server.
func (s *SharedMemoryServer) RemoteAddr() net.Addr { return s.LocalAddr() }

// SetDeadline implements net.Conn for the independent native server.
func (*SharedMemoryServer) SetDeadline(time.Time) error { return nil }

// SetReadDeadline implements net.Conn for the independent native server.
func (*SharedMemoryServer) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline implements net.Conn for the independent native server.
func (*SharedMemoryServer) SetWriteDeadline(time.Time) error { return nil }

// Wait checks that a peer event becomes signaled within the fixture budget.
func (p *SharedMemoryPeer) Wait(t *testing.T, suffix string) bool {
	t.Helper()
	event, err := windows.WaitForSingleObject(p.Handles[suffix], 2000)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Errorf("shared peer wait %s: %d %v", suffix, event, err)
		return false
	}
	return true
}
