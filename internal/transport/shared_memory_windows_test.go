//go:build windows

package transport

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"golang.org/x/sys/windows"
)

const testSharedAnswer = "_CONNECT_ANSWER"
const testSharedConnectData = "_CONNECT_DATA"
const testSharedMutex = "mutex"

func TestPlatformTransportSharedMemoryNativeACL(t *testing.T) {
	for _, target := range []string{testSharedMutex, testSharedConnectData, "_42_DATA"} {
		t.Run(target, func(t *testing.T) {
			p := testpeer.NewSharedMemoryPeer(t)
			handle := p.Handles[target]
			rights := uint32(windows.FILE_MAP_WRITE)
			switch target {
			case testSharedMutex:
				name, err := windows.UTF16PtrFromString(p.Name + "_CONNECT_MUTEX")
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
			raw, err := Dial(ctx, networkSharedMemory, p.Name)
			if err != nil {
				t.Fatal("Java-compatible object rights rejected", err)
			}
			defer func() { _ = raw.Close() }()
			server := &testpeer.SharedMemoryServer{Peer: p}
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
		p := testpeer.NewSharedMemoryPeer(t)
		sd, err := windows.SecurityDescriptorFromString("D:(A;;0x2;;;WD)")
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		if err = windows.SetSecurityInfo(p.Handles["_CONNECT_REQUEST"], windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
			t.Fatal(err)
		}
		raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
		if raw != nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			if raw != nil {
				_ = raw.Close()
			}
			t.Fatal("permission failure fell back to another namespace", err)
		}
		select {
		case <-p.Requested:
			t.Fatal("request sent without permissions")
		default:
		}
	})
}

func TestPlatformTransportSharedMemoryConcurrentAllocation(t *testing.T) {
	ids := []int32{42, 43, -1, 45, 46, 47, 48, 49}
	p := testpeer.NewSharedMemoryAllocator(t, nil, ids)
	addresses := make(chan string, len(ids))
	errorsFound := make(chan error, len(ids))
	var wg sync.WaitGroup
	for range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			raw, err := Dial(ctx, networkSharedMemory, p.Name)
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
		if !seen[fmt.Sprintf("%s_%d", p.Name, id)] {
			t.Error("missing allocated identity", id)
		}
		p.Wait(t, fmt.Sprintf("_%d_CONNECTION_CLOSED", id))
	}
}

func TestPlatformTransportSharedMemoryDeadlineUpdates(t *testing.T) {
	for _, read := range []bool{false, true} {
		t.Run(fmt.Sprint(read), func(t *testing.T) {
			p := testpeer.NewSharedMemoryPeer(t)
			raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			if !read && !p.Wait(t, "_42_SERVER_READ") {
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
				_, err = (&testpeer.SharedMemoryServer{Peer: p}).Write([]byte{9})
			} else {
				err = windows.SetEvent(p.Handles["_42_SERVER_READ"])
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

func TestPlatformTransportSharedMemoryFrames(t *testing.T) {
	for _, size := range []uint32{0, 16001, 0xffffffff} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := testpeer.NewSharedMemoryPeer(t)
			raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			var header [4]byte
			binary.LittleEndian.PutUint32(header[:], size)
			if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.Views["_42_DATA"], &header[0], 4, nil); err != nil {
				t.Fatal(err)
			}
			if err = windows.SetEvent(p.Handles["_42_SERVER_WROTE"]); err != nil {
				t.Fatal(err)
			}
			if n, e := raw.Read(make([]byte, 32)); n != 0 || e == nil {
				t.Fatal("invalid frame accepted", n, e)
			}
		})
	}
	t.Run("partial-read", func(t *testing.T) {
		p := testpeer.NewSharedMemoryPeer(t)
		raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = raw.Close() }()
		server := &testpeer.SharedMemoryServer{Peer: p}
		payload := bytes.Repeat([]byte("abcd"), 4000)
		if _, err = server.Write(payload); err != nil {
			t.Fatal(err)
		}
		read := make([]byte, len(payload))
		if n, e := raw.Read(read[:1]); n != 1 || e != nil {
			t.Fatal(n, e)
		}
		if !p.Wait(t, "_42_CLIENT_READ") {
			return
		}
		// Overwriting the shared region after ACK must not change the retained bytes.
		zeros := make([]byte, 16004)
		if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.Views["_42_DATA"], &zeros[0], uintptr(len(zeros)), nil); err != nil {
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
			p := testpeer.NewSharedMemoryPeer(t)
			raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = raw.Close() }()
			if !read && !p.Wait(t, "_42_SERVER_READ") {
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
		p := testpeer.NewSharedMemoryPeer(t)
		raw, err := Dial(t.Context(), networkSharedMemory, p.Name)
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
		if !p.Wait(t, "_42_CLIENT_WROTE") {
			return
		}
	})
}

func TestPlatformTransportSharedMemoryCancellation(t *testing.T) {
	t.Run("before-request", func(t *testing.T) {
		p := testpeer.NewSharedMemoryPeer(t)
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		name, err := windows.UTF16PtrFromString(p.Name + "_CONNECT_MUTEX")
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
			raw, e := Dial(ctx, networkSharedMemory, p.Name)
			if raw != nil {
				_ = raw.Close()
			}
			done <- e
		}()
		if err = <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("mutex cancellation", err)
		}
		select {
		case <-p.Requested:
			t.Fatal("request sent without mutex")
		default:
		}
	})
	t.Run("after-request", func(t *testing.T) {
		gate := make(chan struct{})
		p := testpeer.NewDelayedSharedMemoryPeer(t, gate)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			raw, e := Dial(ctx, networkSharedMemory, p.Name)
			if raw != nil {
				_ = raw.Close()
			}
			done <- e
		}()
		<-p.Requested
		cancel()
		secondCtx, secondCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer secondCancel()
		if raw, e := Dial(secondCtx, networkSharedMemory, p.Name); raw != nil || !errors.Is(e, context.DeadlineExceeded) {
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
		if !p.Wait(t, "_42_CONNECTION_CLOSED") {
			return
		}
		if err := windows.ResetEvent(p.Handles["_42_CONNECTION_CLOSED"]); err != nil {
			t.Fatal(err)
		}
		// The next request needs a fresh answer and the mutex must be released on its owning thread.
		answered := make(chan struct{})
		go func() {
			defer close(answered)
			if p.Wait(t, "_CONNECT_REQUEST") {
				if err := windows.SetEvent(p.Handles[testSharedAnswer]); err != nil {
					t.Error(err)
				}
			}
		}()
		thirdCtx, thirdCancel := context.WithTimeout(context.Background(), time.Second)
		defer thirdCancel()
		raw, err := Dial(thirdCtx, networkSharedMemory, p.Name)
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
			p := testpeer.NewSharedMemoryPeer(t)
			if view, ok := p.Views[suffix]; ok {
				if err := windows.UnmapViewOfFile(view); err != nil {
					t.Fatal(err)
				}
				delete(p.Views, suffix)
			}
			if err := windows.CloseHandle(p.Handles[suffix]); err != nil {
				t.Fatal(err)
			}
			delete(p.Handles, suffix)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if raw, err := Dial(ctx, networkSharedMemory, p.Name); raw != nil || err == nil {
				if raw != nil {
					_ = raw.Close()
				}
				t.Fatal("partial endpoint accepted", err)
			}
			if suffix != testSharedAnswer && suffix != testSharedConnectData {
				p.Wait(t, "_42_CONNECTION_CLOSED")
			}
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if raw, err := Dial(ctx, networkSharedMemory, fmt.Sprintf("absent-%d", time.Now().UnixNano())); raw != nil || err == nil {
		t.Fatal("absent endpoint accepted", err)
	}
}

func TestPlatformTransportSharedMemory(t *testing.T) {
	p := testpeer.NewSharedMemoryPeer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := Dial(ctx, networkSharedMemory, p.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	<-p.Ready
	message := []byte{5, 0, 0, 0, 'h', 'e', 'l', 'l', 'o'}
	if err = windows.WriteProcessMemory(windows.CurrentProcess(), p.Views["_42_DATA"], &message[0], uintptr(len(message)), nil); err != nil {
		t.Fatal(err)
	}
	if err = windows.SetEvent(p.Handles["_42_SERVER_WROTE"]); err != nil {
		t.Fatal(err)
	}
	read := make([]byte, 5)
	if _, err = io.ReadFull(raw, read); err != nil || string(read) != "hello" {
		t.Fatalf("read: %q %v", read, err)
	}
	if !p.Wait(t, "_42_CLIENT_READ") {
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
		if !p.Wait(t, "_42_CLIENT_WROTE") {
			return
		}
		var header [4]byte
		if err = windows.ReadProcessMemory(windows.CurrentProcess(), p.Views["_42_DATA"], &header[0], 4, nil); err != nil {
			t.Fatal(err)
		}
		size := binary.LittleEndian.Uint32(header[:])
		if size == 0 || size > 16000 {
			t.Fatalf("invalid shared chunk size %d", size)
		}
		chunk := make([]byte, int(size))
		if err = windows.ReadProcessMemory(windows.CurrentProcess(), p.Views["_42_DATA"]+4, &chunk[0], uintptr(size), nil); err != nil {
			t.Fatal(err)
		}
		received = append(received, chunk...)
		if err = windows.SetEvent(p.Handles["_42_SERVER_READ"]); err != nil {
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
