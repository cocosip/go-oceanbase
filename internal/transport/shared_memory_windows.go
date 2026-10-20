//go:build windows

package transport

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sharedMemoryPayload = 16000

var openSharedMapping = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenFileMappingW")

func sharedEvent(name string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	return windows.OpenEvent(windows.SYNCHRONIZE|windows.EVENT_MODIFY_STATE, false, p)
}

func sharedMap(name string, access uint32, size uintptr) (uintptr, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, _, callErr := openSharedMapping.Call(uintptr(access), 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		return 0, callErr
	}
	defer func() { _ = windows.CloseHandle(windows.Handle(h)) }()
	return windows.MapViewOfFile(windows.Handle(h), access, 0, 0, size)
}

func sharedContextWait(ctx context.Context, handle windows.Handle) (uint32, error) {
	wake, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = windows.SetEvent(wake)
		close(done)
	})
	defer func() {
		if !stop() {
			<-done
		}
		_ = windows.CloseHandle(wake)
	}()
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	result, err := windows.WaitForMultipleObjects([]windows.Handle{wake, handle}, false, windows.INFINITE)
	if err == nil && result == windows.WAIT_OBJECT_0 {
		err = ctx.Err()
	}
	return result, err
}

func dialSharedMemory(ctx context.Context, name string) (net.Conn, error) {
	raw, err := openSharedMemory(ctx, name)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Net: networkSharedMemory, Err: err}
	}
	return raw, nil
}

func openSharedMemory(ctx context.Context, name string) (*sharedMemoryConn, error) {
	request, err := sharedEvent(name + "_CONNECT_REQUEST")
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !strings.ContainsRune(name, '\\') {
		name = `Global\` + name
		request, err = sharedEvent(name + "_CONNECT_REQUEST")
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(request) }()
	answer, err := sharedEvent(name + "_CONNECT_ANSWER")
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(answer) }()
	view, err := sharedMap(name+"_CONNECT_DATA", windows.FILE_MAP_READ, 4)
	if err != nil {
		return nil, err
	}
	defer func() { _ = windows.UnmapViewOfFile(view) }()
	p, err := windows.UTF16PtrFromString(name + "_CONNECT_MUTEX")
	if err != nil {
		return nil, err
	}
	// Java-created mutexes grant SYNCHRONIZE only; ReleaseMutex requires ownership, not MODIFY_STATE.
	mutex, err := windows.CreateMutexEx(nil, p, 0, windows.SYNCHRONIZE)
	if err != nil && (!errors.Is(err, windows.ERROR_ALREADY_EXISTS) || mutex == 0) {
		return nil, err
	}
	defer func() { _ = windows.CloseHandle(mutex) }()
	// Win32 mutex ownership belongs to an OS thread, not a goroutine.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, err := sharedContextWait(ctx, mutex)
	if err != nil {
		return nil, err
	}
	if result != windows.WAIT_OBJECT_0+1 && result != windows.WAIT_ABANDONED+1 {
		return nil, fmt.Errorf("shared memory mutex wait: %d", result)
	}
	defer func() { _ = windows.ReleaseMutex(mutex) }()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = windows.SetEvent(request); err != nil {
		return nil, err
	}
	// shortcut: answers have no request ID; retain ownership until answered, even after cancellation.
	// A hard allocation timeout requires a server protocol with correlated answers.
	if result, err = windows.WaitForSingleObject(answer, windows.INFINITE); err != nil {
		return nil, err
	}
	if result != windows.WAIT_OBJECT_0 {
		return nil, fmt.Errorf("shared memory allocation wait: %d", result)
	}
	var id [4]byte
	if err = windows.ReadProcessMemory(windows.CurrentProcess(), view, &id[0], 4, nil); err != nil {
		return nil, err
	}
	prefix := name + "_" + strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(id[:]))), 10)
	return attachSharedMemory(ctx, prefix)
}

type sharedMemoryConn struct {
	name                                                           string
	view                                                           uintptr
	serverWrote, clientRead, serverRead, clientWrote, remoteClosed windows.Handle
	localClosed, readChanged, writeChanged                         windows.Handle
	life                                                           sync.RWMutex
	readMu, writeMu, deadlineMu                                    sync.Mutex
	readDeadline, writeDeadline                                    time.Time
	closed                                                         atomic.Bool
	closeOnce                                                      sync.Once
	closeErr                                                       error
	buffer                                                         [sharedMemoryPayload]byte
	start, end                                                     int
}

func attachSharedMemory(ctx context.Context, prefix string) (_ *sharedMemoryConn, err error) {
	c := &sharedMemoryConn{name: prefix}
	c.remoteClosed, err = sharedEvent(prefix + "_CONNECTION_CLOSED")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	for _, event := range []struct {
		suffix string
		target *windows.Handle
	}{
		{"_SERVER_WROTE", &c.serverWrote}, {"_CLIENT_READ", &c.clientRead},
		{"_SERVER_READ", &c.serverRead}, {"_CLIENT_WROTE", &c.clientWrote},
	} {
		*event.target, err = sharedEvent(prefix + event.suffix)
		if err != nil {
			return nil, err
		}
	}
	c.view, err = sharedMap(prefix+"_DATA", windows.FILE_MAP_WRITE, sharedMemoryPayload+4)
	if err != nil {
		return nil, err
	}
	for _, event := range []struct {
		manual uint32
		target *windows.Handle
	}{
		{1, &c.localClosed}, {0, &c.readChanged}, {0, &c.writeChanged},
	} {
		*event.target, err = windows.CreateEvent(nil, event.manual, 0, nil)
		if err != nil {
			return nil, err
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = windows.SetEvent(c.serverRead); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *sharedMemoryConn) handles() []windows.Handle {
	return []windows.Handle{c.serverWrote, c.clientRead, c.serverRead, c.clientWrote, c.remoteClosed, c.localClosed, c.readChanged, c.writeChanged}
}

func (c *sharedMemoryConn) wait(event, changed windows.Handle, read bool) error {
	for {
		if c.closed.Load() {
			return net.ErrClosed
		}
		deadline := c.deadline(read)
		millis := uint32(windows.INFINITE)
		if !deadline.IsZero() {
			left := time.Until(deadline)
			if left <= 0 {
				return os.ErrDeadlineExceeded
			}
			ms := (left-1)/time.Millisecond + 1
			if ms < time.Duration(windows.INFINITE) {
				millis = uint32(ms)
			} else {
				millis--
			}
		}
		result, err := windows.WaitForMultipleObjects([]windows.Handle{c.localClosed, c.remoteClosed, changed, event}, false, millis)
		if err != nil {
			return err
		}
		switch result {
		case windows.WAIT_OBJECT_0:
			return net.ErrClosed
		case windows.WAIT_OBJECT_0 + 1:
			return io.EOF
		case windows.WAIT_OBJECT_0 + 2, uint32(windows.WAIT_TIMEOUT):
			continue
		case windows.WAIT_OBJECT_0 + 3:
			return nil
		default:
			return fmt.Errorf("shared memory data wait: %d", result)
		}
	}
}

func (c *sharedMemoryConn) deadline(read bool) time.Time {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if read {
		return c.readDeadline
	}
	return c.writeDeadline
}

func (c *sharedMemoryConn) opError(op string, err error) error {
	if err == nil {
		return nil
	}
	return &net.OpError{Op: op, Net: networkSharedMemory, Addr: c.RemoteAddr(), Err: err}
}

func (c *sharedMemoryConn) Read(b []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	c.life.RLock()
	defer c.life.RUnlock()
	if c.closed.Load() {
		return 0, c.opError("read", net.ErrClosed)
	}
	if len(b) == 0 {
		return 0, nil
	}
	if deadline := c.deadline(true); !deadline.IsZero() && !time.Now().Before(deadline) {
		return 0, c.opError("read", os.ErrDeadlineExceeded)
	}
	if c.start == c.end {
		if err := c.readChunk(); err != nil {
			return 0, c.opError("read", err)
		}
	}
	n := copy(b, c.buffer[c.start:c.end])
	c.start += n
	return n, nil
}

func (c *sharedMemoryConn) readChunk() error {
	if err := c.wait(c.serverWrote, c.readChanged, true); err != nil {
		return err
	}
	var header [4]byte
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), c.view, &header[0], 4, nil); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > sharedMemoryPayload {
		return fmt.Errorf("invalid shared memory payload size: %d", size)
	}
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), c.view+4, &c.buffer[0], uintptr(size), nil); err != nil {
		return err
	}
	c.start, c.end = 0, int(size)
	return windows.SetEvent(c.clientRead)
}

func (c *sharedMemoryConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.life.RLock()
	defer c.life.RUnlock()
	if c.closed.Load() {
		return 0, c.opError("write", net.ErrClosed)
	}
	written := 0
	for len(b) > 0 {
		size := min(len(b), sharedMemoryPayload)
		if err := c.writeChunk(b[:size]); err != nil {
			return written, c.opError("write", err)
		}
		written += size
		b = b[size:]
	}
	return written, nil
}

func (c *sharedMemoryConn) writeChunk(b []byte) error {
	if err := c.wait(c.serverRead, c.writeChanged, false); err != nil {
		return err
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(b)))
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), c.view, &header[0], 4, nil); err != nil {
		return err
	}
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), c.view+4, &b[0], uintptr(len(b)), nil); err != nil {
		return err
	}
	return windows.SetEvent(c.clientWrote)
}

func (c *sharedMemoryConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		if c.localClosed != 0 {
			c.closeErr = windows.SetEvent(c.localClosed)
		}
		c.life.Lock()
		defer c.life.Unlock()
		if c.remoteClosed != 0 {
			c.closeErr = errors.Join(c.closeErr, windows.SetEvent(c.remoteClosed))
		}
		if c.view != 0 {
			c.closeErr = errors.Join(c.closeErr, windows.UnmapViewOfFile(c.view))
		}
		for _, handle := range c.handles() {
			if handle != 0 {
				c.closeErr = errors.Join(c.closeErr, windows.CloseHandle(handle))
			}
		}
	})
	return c.closeErr
}

type sharedMemoryAddr string

func (a sharedMemoryAddr) Network() string                     { return networkSharedMemory }
func (a sharedMemoryAddr) String() string                      { return string(a) }
func (c *sharedMemoryConn) LocalAddr() net.Addr                { return sharedMemoryAddr(c.name) }
func (c *sharedMemoryConn) RemoteAddr() net.Addr               { return sharedMemoryAddr(c.name) }
func (c *sharedMemoryConn) SetDeadline(t time.Time) error      { return c.setDeadline(t, true, true) }
func (c *sharedMemoryConn) SetReadDeadline(t time.Time) error  { return c.setDeadline(t, true, false) }
func (c *sharedMemoryConn) SetWriteDeadline(t time.Time) error { return c.setDeadline(t, false, true) }

func (c *sharedMemoryConn) setDeadline(t time.Time, read, write bool) error {
	c.life.RLock()
	defer c.life.RUnlock()
	if c.closed.Load() {
		return c.opError("set", net.ErrClosed)
	}
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	var err error
	if read {
		c.readDeadline = t
		err = windows.SetEvent(c.readChanged)
	}
	if write {
		c.writeDeadline = t
		err = errors.Join(err, windows.SetEvent(c.writeChanged))
	}
	return c.opError("set", err)
}
