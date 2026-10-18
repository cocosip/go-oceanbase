package oceanbase

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// sockaddr_un.sun_path is 104 bytes on macOS and 108 on Linux, including the
// terminator. t.TempDir derives its directory from the test name, so long names
// and subtest names push a socket path past that limit and bind fails with
// EINVAL ("invalid argument"). A short directory keeps every endpoint inside it.
func unixTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "ob-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(directory); err != nil {
			t.Error(err)
		}
	})
	return directory
}

// listenUnix binds a Unix socket inside directory and returns it with its path.
func listenUnix(t *testing.T, directory, name string) (net.Listener, string) {
	t.Helper()
	path := filepath.Join(directory, name)
	listener, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	return listener, path
}
