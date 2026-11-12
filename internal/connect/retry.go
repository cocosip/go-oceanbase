package connect

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// HandshakeTransportError distinguishes retryable TLS transport failures from fatal alerts.
type HandshakeTransportError struct{ error }

func (e *HandshakeTransportError) Unwrap() error { return e.error }

// RetryHandshakeError permits retry before initialization has started.
func RetryHandshakeError(err error) bool {
	// crypto/tls wraps fatal protocol/credential alerts in net.OpError too.
	var network *net.OpError
	if errors.As(err, &network) && (network.Op == "remote error" || network.Op == "local error") {
		return false
	}
	// Login-phase connection exceptions (class 08: overloaded, draining, shutting
	// down) belong to one endpoint, so another candidate may still serve the login.
	// Credential and database rejections use class 28/42 and stay terminal.
	var server *dberrors.Error
	if errors.As(err, &server) {
		return strings.HasPrefix(server.SQLState, "08")
	}
	var transport *wire.IOError
	var handshake *HandshakeTransportError
	return errors.As(err, &transport) || errors.As(err, &handshake)
}

// RetryDialError permits timeouts and network failures except permanent DNS errors.
func RetryDialError(err error) bool {
	var network *net.OpError
	var dns *net.DNSError
	if errors.As(err, &dns) && !dns.IsTimeout && !dns.IsTemporary {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network)
}
