package session

import (
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"net"
)

// Negotiated records transport, authentication and server capabilities for a physical session.
type Negotiated struct {
	Conn          net.Conn
	Raw           net.Conn
	Packets       *wire.PacketIO
	Hello         wire.Handshake
	OK            wire.OK
	Mode          config.Mode
	Capabilities  uint64
	TLSVerified   bool
	User          string
	ProxyUser     string
	OBVersion     string
	NewMetadata   bool
	AuthPlugin    string
	AuthChallenge []byte
}
