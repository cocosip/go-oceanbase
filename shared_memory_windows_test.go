//go:build windows

package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
	"golang.org/x/sys/windows"
)

const testSharedAnswer = "_CONNECT_ANSWER"
const testSharedConnectData = "_CONNECT_DATA"
const testSharedMutex = "mutex"

func TestPlatformTransportSharedMemoryNativeACL(t *testing.T) {
	for _, target := range []string{testSharedMutex, testSharedConnectData, "_42_DATA"} {
		t.Run(target, func(t *testing.T) {
			p := newSharedMemoryPeer(t)
			handle := p.handles[target]
			rights := uint32(windows.FILE_MAP_WRITE)
			switch target {
			case testSharedMutex:
				name, err := windows.UTF16PtrFromString(p.name + "_CONNECT_MUTEX")
				if err != nil {
					t.Fatal(err)
				}
				handle, err = windows.CreateMutexEx(nil, name, 0, windows.MUTEX_ALL_ACCESS)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = windows.CloseHandle(handle) })
				rights = windows.SYNCHRONIZE
			case testSharedConnectData:
				rights = windows.FILE_MAP_READ
			}
			sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:(A;;0x%x;;;WD)", rights))
			if err != nil {
				t.Fatal(err)
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err = windows.SetSecurityInfo(handle, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			raw, err := dialTransport(ctx, networkSharedMemory, p.name)
			if err != nil {
				t.Fatal("Java-compatible object rights rejected", err)
			}
			defer func() { _ = raw.Close() }()
			server := &sharedMemoryServer{p: p}
			if _, err = raw.Write([]byte{8}); err != nil {
				t.Fatal(err)
			}
			var data [1]byte
			if _, err = io.ReadFull(server, data[:]); err != nil || data[0] != 8 {
				t.Fatal("restricted mapping write", data, err)
			}
			if _, err = server.Write([]byte{9}); err != nil {
				t.Fatal(err)
			}
			if _, err = io.ReadFull(raw, data[:]); err != nil || data[0] != 9 {
				t.Fatal("restricted mapping read", data, err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}
			if target == testSharedMutex {
				done := make(chan error, 1)
				go func() {
					runtime.LockOSThread()
					defer runtime.UnlockOSThread()
					result, e := windows.WaitForSingleObject(handle, 1000)
					if e == nil && result != windows.WAIT_OBJECT_0 {
						e = fmt.Errorf("mutex release wait: %d", result)
					}
					if e == nil {
						e = windows.ReleaseMutex(handle)
					}
					done <- e
				}()
				if err = <-done; err != nil {
					t.Fatal("restricted mutex was not released", err)
				}
			}
		})
	}
	t.Run("request-denied", func(t *testing.T) {
		p := newSharedMemoryPeer(t)
		sd, err := windows.SecurityDescriptorFromString("D:(A;;0x2;;;WD)")
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err = windows.SetSecurityInfo(p.handles["_CONNECT_REQUEST"], windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
		raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
		if raw != nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if raw != nil {
				_ = raw.Close()
			}
			t.Fatal("permission failure fell back to another namespace", err)
		}
		select {
		case <-p.requested:
			t.Fatal("request sent without permissions")
		default:
		}
	})
}

func TestPlatformTransportSharedMemoryConcurrentAllocation(t *testing.T) {
	ids := []int32{42, 43, -1, 45, 46, 47, 48, 49}
	p := newSharedMemoryAllocator(t, nil, ids)
	addresses := make(chan string, len(ids))
	errorsFound := make(chan error, len(ids))
	var wg sync.WaitGroup
	for range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			raw, err := dialTransport(ctx, networkSharedMemory, p.name)
			if err != nil {
				errorsFound <- err
				return
			}
			addresses <- raw.RemoteAddr().String()
			if err = raw.Close(); err != nil {
				errorsFound <- err
			}
		}()
	}
	wg.Wait()
	close(addresses)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	seen := map[string]bool{}
	for addr := range addresses {
		if seen[addr] {
			t.Error("allocation reused live identity", addr)
		}
		seen[addr] = true
	}
	for _, id := range ids {
		if !seen[fmt.Sprintf("%s_%d", p.name, id)] {
			t.Error("missing allocated identity", id)
		}
		p.wait(t, fmt.Sprintf("_%d_CONNECTION_CLOSED", id))
	}
}

func TestPlatformTransportSharedMemoryDeadlineUpdates(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			p := newSharedMemoryPeer(t)
			raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			if !read && !p.wait(t, "_42_SERVER_READ") {
				return
			}
			done := make(chan error, 1)
			go func() {
				var e error
				if read {
					_, e = raw.Read(make([]byte, 1))
				} else {
					_, e = raw.Write([]byte{1})
				}
				done <- e
			}()
			if err = raw.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if err = raw.SetDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err = raw.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			select {
			case e := <-done:
				t.Fatal("deadline update returned early", e)
			case <-time.After(250 * time.Millisecond):
			}
			if read {
				_, err = (&sharedMemoryServer{p: p}).Write([]byte{9})
			} else {
				err = windows.SetEvent(p.handles["_42_SERVER_READ"])
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal("cleared deadline remained active", err)
			}
		})
	}
}

type sharedMemoryPeer struct {
	name      string
	handles   map[string]windows.Handle
	views     map[string]uintptr
	ready     chan struct{}
	requested chan struct{}
}

func TestPlatformTransportSharedMemoryFrames(t *testing.T) {
	for _, size := range []uint32{0, 16001, 0xffffffff} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := newSharedMemoryPeer(t)
			raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			var header [4]byte
			binary.LittleEndian.PutUint32(header[:], size)
			if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.views["_42_DATA"], &header[0], 4, nil); err != nil {
				t.Fatal(err)
			}
			if err = windows.SetEvent(p.handles["_42_SERVER_WROTE"]); err != nil {
				t.Fatal(err)
			}
			if n, e := raw.Read(make([]byte, 32)); n != 0 || e == nil {
				t.Fatal("invalid frame accepted", n, e)
			}
		})
	}
	t.Run("partial-read", func(t *testing.T) {
		p := newSharedMemoryPeer(t)
		raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = raw.Close() }()
		server := &sharedMemoryServer{p: p}
		payload := bytes.Repeat([]byte("abcd"), 4000)
		if _, err = server.Write(payload); err != nil {
			t.Fatal(err)
		}
		read := make([]byte, len(payload))
		if n, e := raw.Read(read[:1]); n != 1 || e != nil {
			t.Fatal(n, e)
		}
		if !p.wait(t, "_42_CLIENT_READ") {
			return
		}
		// Overwriting the shared region after ACK must not change the retained bytes.
		zeros := make([]byte, 16004)
		if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.views["_42_DATA"], &zeros[0], uintptr(len(zeros)), nil); err != nil {
			t.Fatal(err)
		}
		if err = raw.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		if n, e := raw.Read(read[1:2]); n != 0 || !errors.Is(e, os.ErrDeadlineExceeded) {
			t.Fatal("cached bytes ignored read deadline", n, e)
		}
		if err = raw.SetReadDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		if _, err = io.ReadFull(raw, read[1:]); err != nil || !bytes.Equal(read, payload) {
			t.Fatal("partial read changed", err)
		}
		if n, e := raw.Read(nil); n != 0 || e != nil {
			t.Fatal(n, e)
		}
		if n, e := raw.Write(nil); n != 0 || e != nil {
			t.Fatal(n, e)
		}
	})
}

func TestPlatformTransportSharedMemoryDeadlines(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			p := newSharedMemoryPeer(t)
			raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			if !read && !p.wait(t, "_42_SERVER_READ") {
				return
			}
			call := func() error {
				if read {
					_, e := raw.Read(make([]byte, 1))
					return e
				}
				_, e := raw.Write([]byte{1})
				return e
			}
			done := make(chan error, 1)
			go func() { done <- call() }()
			if err = raw.SetDeadline(time.Now().Add(-time.Second)); err != nil {
				t.Fatal(err)
			}
			if e := <-done; !errors.Is(e, os.ErrDeadlineExceeded) {
				t.Fatal("deadline failed to wake wait", e)
			}
			if err = raw.SetDeadline(time.Time{}); err != nil {
				t.Fatal(err)
			}
			go func() { done <- call() }()
			if err = raw.Close(); err != nil {
				t.Fatal(err)
			}
			if e := <-done; !errors.Is(e, net.ErrClosed) {
				t.Fatal("close failed to interrupt", e)
			}
			if err = raw.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
				t.Fatal("closed deadline", err)
			}
			if err = raw.Close(); err != nil {
				t.Fatal("repeat close", err)
			}
		})
	}
	t.Run("partial-write", func(t *testing.T) {
		p := newSharedMemoryPeer(t)
		raw, err := dialTransport(t.Context(), networkSharedMemory, p.name)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = raw.Close() }()
		if err = raw.SetWriteDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		n, err := raw.Write(make([]byte, 32000))
		var ne net.Error
		if n != 16000 || !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatal(n, err)
		}
		if !p.wait(t, "_42_CLIENT_WROTE") {
			return
		}
	})
}

func TestPlatformTransportSharedMemoryCancellation(t *testing.T) {
	t.Run("before-request", func(t *testing.T) {
		p := newSharedMemoryPeer(t)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		name, err := windows.UTF16PtrFromString(p.name + "_CONNECT_MUTEX")
		if err != nil {
			t.Fatal(err)
		}
		mutex, err := windows.CreateMutexEx(nil, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = windows.CloseHandle(mutex) }()
		if _, err = windows.WaitForSingleObject(mutex, 1000); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = windows.ReleaseMutex(mutex) }()
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			raw, e := dialTransport(ctx, networkSharedMemory, p.name)
			if raw != nil {
				_ = raw.Close()
			}
			done <- e
		}()
		if err = <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("mutex cancellation", err)
		}
		select {
		case <-p.requested:
			t.Fatal("request sent without mutex")
		default:
		}
	})
	t.Run("after-request", func(t *testing.T) {
		gate := make(chan struct{})
		p := newDelayedSharedMemoryPeer(t, gate)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			raw, e := dialTransport(ctx, networkSharedMemory, p.name)
			if raw != nil {
				_ = raw.Close()
			}
			done <- e
		}()
		<-p.requested
		cancel()
		secondCtx, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer secondCancel()
		if raw, e := dialTransport(secondCtx, networkSharedMemory, p.name); raw != nil || !errors.Is(e, context.DeadlineExceeded) {
			if raw != nil {
				_ = raw.Close()
			}
			t.Fatal("cancelled allocation released ownership", e)
		}
		select {
		case e := <-done:
			t.Fatal("abandoned uncorrelated answer", e)
		default:
		}
		close(gate)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal("late answer ignored cancellation", err)
		}
		if !p.wait(t, "_42_CONNECTION_CLOSED") {
			return
		}
		if err := windows.ResetEvent(p.handles["_42_CONNECTION_CLOSED"]); err != nil {
			t.Fatal(err)
		}
		// The next request needs a fresh answer and the mutex must be released on its owning thread.
		answered := make(chan struct{})
		go func() {
			defer close(answered)
			if p.wait(t, "_CONNECT_REQUEST") {
				if err := windows.SetEvent(p.handles[testSharedAnswer]); err != nil {
					t.Error(err)
				}
			}
		}()
		thirdCtx, thirdCancel := context.WithTimeout(context.Background(), time.Second)
		defer thirdCancel()
		raw, err := dialTransport(thirdCtx, networkSharedMemory, p.name)
		if err != nil {
			t.Fatal("next allocation failed", err)
		}
		_ = raw.Close()
		<-answered
	})
}

func TestPlatformTransportSharedMemoryOpenFailure(t *testing.T) {
	for _, suffix := range []string{testSharedAnswer, testSharedConnectData, "_42_SERVER_WROTE", "_42_CLIENT_READ", "_42_SERVER_READ", "_42_CLIENT_WROTE", "_42_DATA"} {
		t.Run(suffix, func(t *testing.T) {
			p := newSharedMemoryPeer(t)
			if view, ok := p.views[suffix]; ok {
				if err := windows.UnmapViewOfFile(view); err != nil {
					t.Fatal(err)
				}
				delete(p.views, suffix)
			}
			if err := windows.CloseHandle(p.handles[suffix]); err != nil {
				t.Fatal(err)
			}
			delete(p.handles, suffix)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if raw, err := dialTransport(ctx, networkSharedMemory, p.name); raw != nil || err == nil {
				if raw != nil {
					_ = raw.Close()
				}
				t.Fatal("partial endpoint accepted", err)
			}
			if suffix != testSharedAnswer && suffix != testSharedConnectData {
				p.wait(t, "_42_CONNECTION_CLOSED")
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if raw, err := dialTransport(ctx, networkSharedMemory, fmt.Sprintf("absent-%d", time.Now().UnixNano())); raw != nil || err == nil {
		t.Fatal("absent endpoint accepted", err)
	}
}

func newSharedMemoryPeer(t *testing.T) *sharedMemoryPeer {
	return newDelayedSharedMemoryPeer(t, nil)
}

func newDelayedSharedMemoryPeer(t *testing.T, gate <-chan struct{}) *sharedMemoryPeer {
	t.Helper()
	return newSharedMemoryAllocator(t, gate, []int32{42})
}

func newSharedMemoryAllocator(t *testing.T, gate <-chan struct{}, ids []int32) *sharedMemoryPeer {
	t.Helper()
	p := &sharedMemoryPeer{name: fmt.Sprintf("go-oceanbase-%d-%d", os.Getpid(), time.Now().UnixNano()), handles: map[string]windows.Handle{}, views: map[string]uintptr{}, ready: make(chan struct{}), requested: make(chan struct{})}
	stop := make(chan struct{})
	t.Cleanup(func() {
		for _, view := range p.views {
			_ = windows.UnmapViewOfFile(view)
		}
		for _, handle := range p.handles {
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
		name, err := windows.UTF16PtrFromString(p.name + suffix)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateEvent(nil, manual, 0, name)
		if err != nil {
			t.Fatal(err)
		}
		p.handles[suffix] = handle
	}
	for suffix, size := range mappings {
		name, err := windows.UTF16PtrFromString(p.name + suffix)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := windows.CreateFileMapping(windows.InvalidHandle, nil, windows.PAGE_READWRITE, 0, size, name)
		if err != nil {
			t.Fatal(err)
		}
		p.handles[suffix] = handle
		view, err := windows.MapViewOfFile(handle, windows.FILE_MAP_READ|windows.FILE_MAP_WRITE, 0, 0, uintptr(size))
		if err != nil {
			t.Fatal(err)
		}
		p.views[suffix] = view
	}
	requestHandle, answerHandle, stopHandle := p.handles["_CONNECT_REQUEST"], p.handles[testSharedAnswer], p.handles["_TEST_STOP"]
	connectView := p.views[testSharedConnectData]
	go func() {
		defer close(p.ready)
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
				close(p.requested)
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
	t.Cleanup(func() { close(stop); _ = windows.SetEvent(stopHandle); <-p.ready })
	return p
}

// The test peer uses native events and mappings independently of the client implementation.
type sharedMemoryServer struct {
	p       *sharedMemoryPeer
	pending []byte
	wrote   bool
}

func (s *sharedMemoryServer) wait(event string) error {
	result, err := windows.WaitForMultipleObjects([]windows.Handle{s.p.handles[event], s.p.handles["_42_CONNECTION_CLOSED"]}, false, 3000)
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

func (s *sharedMemoryServer) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if len(s.pending) == 0 {
		if err := s.wait("_42_CLIENT_WROTE"); err != nil {
			return 0, err
		}
		var header [4]byte
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), s.p.views["_42_DATA"], &header[0], 4, nil); err != nil {
			return 0, err
		}
		size := binary.LittleEndian.Uint32(header[:])
		if size == 0 || size > 16000 {
			return 0, fmt.Errorf("peer invalid payload %d", size)
		}
		s.pending = make([]byte, size)
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), s.p.views["_42_DATA"]+4, &s.pending[0], uintptr(size), nil); err != nil {
			return 0, err
		}
		if err := windows.SetEvent(s.p.handles["_42_SERVER_READ"]); err != nil {
			return 0, err
		}
	}
	n := copy(b, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *sharedMemoryServer) Write(b []byte) (int, error) {
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
		if err := windows.WriteProcessMemory(windows.CurrentProcess(), s.p.views["_42_DATA"], &frame[0], uintptr(len(frame)), nil); err != nil {
			return n, err
		}
		if err := windows.SetEvent(s.p.handles["_42_SERVER_WROTE"]); err != nil {
			return n, err
		}
		s.wrote = true
		n += size
		b = b[size:]
	}
	return n, nil
}

func (s *sharedMemoryServer) Close() error {
	return windows.SetEvent(s.p.handles["_42_CONNECTION_CLOSED"])
}
func (s *sharedMemoryServer) LocalAddr() net.Addr {
	return &net.UnixAddr{Name: s.p.name, Net: networkSharedMemory}
}
func (s *sharedMemoryServer) RemoteAddr() net.Addr           { return s.LocalAddr() }
func (*sharedMemoryServer) SetDeadline(time.Time) error      { return nil }
func (*sharedMemoryServer) SetReadDeadline(time.Time) error  { return nil }
func (*sharedMemoryServer) SetWriteDeadline(time.Time) error { return nil }

func sharedMemoryDB(t *testing.T, cfg Config, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	return sharedMemoryDBConn(t, cfg, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

func sharedMemoryDBConn(t *testing.T, cfg Config, serve func(context.Context, net.Conn, *wire.PacketIO)) *sql.DB {
	t.Helper()
	p := newSharedMemoryPeer(t)
	cfg.Network, cfg.Addr, cfg.User = networkSharedMemory, p.name, testUser
	created, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := &sharedMemoryServer{p: p}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		<-p.ready
		serve(context.Background(), server, wire.NewPacketIO(server, 64<<20))
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	db := sql.OpenDB(created)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPlatformTransportSharedMemoryVerifiedTLS(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprint(status, "/", trusted), func(t *testing.T) {
				client, server := testTLSConfig(t)
				if !trusted {
					client.ServerName = "wrong.invalid"
				}
				db := sharedMemoryDBConn(t, Config{Password: testPassword, TLSConfig: client}, func(ctx context.Context, raw net.Conn, p *wire.PacketIO) {
					if !sendPackets(ctx, t, p, authGreeting(authClear, true)) || !readAuthResponse(ctx, t, p) {
						return
					}
					secure := tls.Server(raw, server)
					if err := secure.HandshakeContext(ctx); err != nil {
						if trusted {
							t.Error(err)
						}
						return
					}
					if err := p.ReplaceConn(authPeerConn{secure}); err != nil {
						t.Error(err)
						return
					}
					data, err := p.ReadMessage(ctx)
					if !trusted {
						if err == nil || len(data) != 0 {
							t.Error("untrusted IPC target received DB credentials")
						}
						return
					}
					if err != nil || !bytes.Contains(data, []byte(testPassword+"\x00")) {
						t.Error("verified IPC authentication missing", err)
						return
					}
					if !sendPackets(ctx, t, p, protocolOK(status, nil)) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				if err := db.PingContext(t.Context()); (err == nil) != trusted {
					t.Fatal("IPC TLS identity check", err)
				}
			})
		}
	}
}

func TestPlatformTransportSharedMemorySessions(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprint(status, "/", framing), func(t *testing.T) {
				cfg := Config{}
				if framing == testCompression {
					cfg.Protocol.Compression = FeatureRequired
				}
				if framing == testOB20 {
					cfg.Protocol.OB20 = FeatureRequired
				}
				db := sharedMemoryDB(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
					if !serveCreateDatabaseAuth(ctx, t, p, framing, status) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
						return
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				if err := db.PingContext(t.Context()); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestPlatformTransportSharedMemoryTLSGate(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			cfg := Config{Password: testPassword}
			if configured {
				cfg.TLSConfig = &tls.Config{ServerName: "database.example", MinVersion: tls.VersionTLS12}
			}
			db := sharedMemoryDB(t, cfg, func(ctx context.Context, p *wire.PacketIO) {
				if !sendPackets(ctx, t, p, authGreeting(authClear, false)) {
					return
				}
				if data, err := p.ReadMessage(ctx); err == nil || len(data) != 0 {
					t.Error("shared memory sent unprotected credentials")
				}
			})
			if err := db.PingContext(t.Context()); !errors.Is(err, ErrTLSRequired) {
				t.Fatal(err)
			}
		})
	}
}

func TestPlatformTransportSharedMemoryBatch(t *testing.T) {
	for _, status := range []byte{2, 6} {
		for _, strategy := range []BatchStrategy{BatchAuto, BatchMultiSend} {
			t.Run(fmt.Sprint(status, "/", strategy), func(t *testing.T) {
				db := sharedMemoryDB(t, Config{}, func(ctx context.Context, p *wire.PacketIO) {
					if !serveConnect(ctx, t, p, status) || !serveBaseline(ctx, t, p, status) {
						return
					}
					for i := int64(1); i <= 3; i++ {
						if !servePrepare(ctx, t, p, testBatchSQL, 7, 1, nil) || !readCommand(ctx, t, p, batchExecutePacket(t, i)) || !sendPackets(ctx, t, p, commandPacket(1, 0, status)) || !readCommand(ctx, t, p, stmtPacket(25, 7)) {
							return
						}
					}
					p.ResetSequence()
					serveQuit(t, p)
				})
				r, err := ExecBatch(t.Context(), db, testBatchSQL, [][]any{{1}, {2}, {3}}, BatchOptions{Strategy: strategy, ContinueOnError: true})
				if err != nil || r.Strategy != BatchSequential || len(r.Items) != 3 {
					t.Fatal(r, err)
				}
				for _, item := range r.Items {
					if item.Status != BatchSucceeded || item.RowsAffected != 1 {
						t.Fatal(item)
					}
				}
			})
		}
	}
}

func (p *sharedMemoryPeer) wait(t *testing.T, suffix string) bool {
	t.Helper()
	event, err := windows.WaitForSingleObject(p.handles[suffix], 2000)
	if err != nil || event != windows.WAIT_OBJECT_0 {
		t.Errorf("shared peer wait %s: %d %v", suffix, event, err)
		return false
	}
	return true
}

func TestPlatformTransportSharedMemory(t *testing.T) {
	p := newSharedMemoryPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := dialTransport(ctx, networkSharedMemory, p.name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	<-p.ready
	message := []byte{5, 0, 0, 0, 'h', 'e', 'l', 'l', 'o'}
	if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.views["_42_DATA"], &message[0], uintptr(len(message)), nil); err != nil {
		t.Fatal(err)
	}
	if err = windows.SetEvent(p.handles["_42_SERVER_WROTE"]); err != nil {
		t.Fatal(err)
	}
	read := make([]byte, 5)
	if _, err = io.ReadFull(raw, read); err != nil || string(read) != "hello" {
		t.Fatalf("read: %q %v", read, err)
	}
	if !p.wait(t, "_42_CLIENT_READ") {
		return
	}
	payload := bytes.Repeat([]byte{0x52}, 32001)
	done := make(chan error, 1)
	go func() {
		n, e := raw.Write(payload)
		if e == nil && n != len(payload) {
			e = io.ErrShortWrite
		}
		done <- e
	}()
	var received []byte
	for range 3 {
		if !p.wait(t, "_42_CLIENT_WROTE") {
			return
		}
		var header [4]byte
		if err = windows.ReadProcessMemory(windows.CurrentProcess(), p.views["_42_DATA"], &header[0], 4, nil); err != nil {
			t.Fatal(err)
		}
		size := binary.LittleEndian.Uint32(header[:])
		if size == 0 || size > 16000 {
			t.Fatalf("invalid shared chunk size %d", size)
		}
		chunk := make([]byte, int(size))
		if err = windows.ReadProcessMemory(windows.CurrentProcess(), p.views["_42_DATA"]+4, &chunk[0], uintptr(size), nil); err != nil {
			t.Fatal(err)
		}
		received = append(received, chunk...)
		if err = windows.SetEvent(p.handles["_42_SERVER_READ"]); err != nil {
			t.Fatal(err)
		}
	}
	if err = <-done; err != nil || !bytes.Equal(received, payload) {
		t.Fatal("shared write corruption", err)
	}
	if err = raw.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Read(read); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatal("shared read deadline", err)
	}
	if err = raw.SetDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, e := raw.Read(read); !errors.Is(e, net.ErrClosed) {
			t.Error("shared close did not interrupt read", e)
		}
	}()
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}
