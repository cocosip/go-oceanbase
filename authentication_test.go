package oceanbase

import (
	"github.com/cocosip/go-oceanbase/internal/testpeer"
)

var authGreeting = testpeer.AuthGreeting

// The peer keeps the test-wide deadline; a client close must not fail deadline cleanup after a complete write.
type authPeerConn = testpeer.AuthPeerConn
