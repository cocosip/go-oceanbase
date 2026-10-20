// Package obproxy owns the optional native OBProxy host and its Unix dialer.
package obproxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	configpkg "github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
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
	spec, err := newOBProxySpec(hostPath, libraryPath, proxyConfig)
	if err != nil {
		return nil, err
	}
	if !obProxyEnabled {
		return nil, fmt.Errorf("%w: embedded OBProxy requires obproxy on Linux amd64/arm64", dberrors.ErrUnsupported)
	}
	return sharedOBProxy.dialer(spec), nil
}

const networkUnix = "unix"

type obProxySpec struct {
	host, library, config, directory, socket string
}

func newOBProxySpec(host, library, config string) (obProxySpec, error) {
	if !filepath.IsAbs(host) || !utf8.ValidString(host) || strings.ContainsRune(host, 0) {
		return obProxySpec{}, &configpkg.ConfigError{Field: "OBProxy host path"}
	}
	if !filepath.IsAbs(library) || !utf8.ValidString(library) || strings.ContainsRune(library, 0) {
		return obProxySpec{}, &configpkg.ConfigError{Field: "OBProxy library path"}
	}
	if len(config) > 4095 || !utf8.ValidString(config) || strings.ContainsRune(config, 0) {
		return obProxySpec{}, &configpkg.ConfigError{Field: "OBProxy proxyConfig"}
	}
	directory, err := os.Getwd()
	if err != nil {
		return obProxySpec{}, err
	}
	socket := filepath.Join(directory, ".obproxy", "obproxy.domain")
	if len(socket) > 107 {
		return obProxySpec{}, &configpkg.ConfigError{Field: "OBProxy Unix endpoint length"}
	}
	return obProxySpec{host: filepath.Clean(host), library: filepath.Clean(library), config: config, directory: directory, socket: socket}, nil
}

var sharedOBProxy = obProxyRuntime{initialize: initializeOBProxy}

type obProxyRuntime struct {
	mu         sync.Mutex
	state      *obProxyInitialization
	initialize func(obProxySpec) error
}

type obProxyInitialization struct {
	spec  obProxySpec
	ready chan struct{}
	err   error // published by closing ready
}

func (r *obProxyRuntime) start(ctx context.Context, spec obProxySpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	if directory != spec.directory {
		return &configpkg.ConfigError{Field: "OBProxy working directory changed"}
	}
	r.mu.Lock()
	if err = ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	if r.state == nil {
		state := &obProxyInitialization{spec: spec, ready: make(chan struct{})}
		r.state = state
		go func() {
			state.err = r.initialize(spec)
			close(state.ready)
		}()
	}
	state := r.state
	r.mu.Unlock()
	if state.spec != spec {
		return &configpkg.ConfigError{Field: "conflicting process OBProxy configuration"}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-state.ready:
		if err = ctx.Err(); err != nil {
			return err
		}
		return state.err
	}
}

func (r *obProxyRuntime) dialer(spec obProxySpec) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		target := address
		if !filepath.IsAbs(target) {
			target = filepath.Join(spec.directory, target)
		}
		if network != networkUnix || filepath.Clean(target) != spec.socket {
			return nil, &configpkg.ConfigError{Field: "OBProxy Unix endpoint"}
		}
		if err := r.start(ctx, spec); err != nil {
			return nil, err
		}
		return (&net.Dialer{}).DialContext(ctx, networkUnix, spec.socket)
	}
}
