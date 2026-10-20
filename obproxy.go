package oceanbase

import (
	"context"
	"net"

	"github.com/cocosip/go-oceanbase/obproxy"
)

// NewOBProxyDialer returns a DialContext for embedded OBProxy at the Unix endpoint
// .obproxy/obproxy.domain in the current directory. It requires the obproxy build
// tag on Linux amd64/arm64. hostPath is the absolute path to the host built from
// tools/obproxy-host/main.c; libraryPath is an absolute trusted OBProxy library path.
// proxyConfig is the native -o value (at most 4095 UTF-8 bytes, without NUL).
// Construction does not start a process. First use starts one dedicated native
// host, isolating its signal handlers, locale, allocator and logs from Go.
// Initialization may continue after context cancellation. DB.Close does not stop
// the host; closing the parent process's control pipe terminates it at process exit.
// A process shares one host/configuration; failures are cached. The host owns
// .obproxy and replaces its endpoint. Separate configurations require separate
// processes and working directories; do not chdir while the dialer is in use.
// The absolute socket path must fit Linux's 107-byte sockaddr_un limit.
func NewOBProxyDialer(hostPath, libraryPath, proxyConfig string) (func(context.Context, string, string) (net.Conn, error), error) {
	return obproxy.NewOBProxyDialer(hostPath, libraryPath, proxyConfig)
}
