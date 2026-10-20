// Package testpeer provides shared protocol and transport fixtures for tests.
package testpeer

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Shared authentication and protocol fixture values.
const (
	User                  = "app"
	Password              = "secret"
	ServerName            = "db.example"
	Standard              = "standard"
	Compression           = "compression"
	OB20                  = "ob20"
	AuthNative            = "mysql_native_password"
	AuthClear             = "mysql_clear_password"
	testProxyCapability   = "ob_capability_flag"
	networkUnix           = "unix"
	sessionReadDualSuffix = " FROM dual"
)

// AuthGreeting builds a server greeting with the requested authentication plugin and TLS capability.
func AuthGreeting(plugin string, ssl bool) []byte {
	data := []byte{10}
	data = append(data, []byte("5.7.25-OceanBase")...)
	data = append(data, 0)
	data = binary.LittleEndian.AppendUint32(data, 42)
	data = append(data, []byte("12345678")...)
	data = append(data, 0)
	var caps uint32 = 0x00aa820f
	if ssl {
		caps |= 0x800
	}
	data = binary.LittleEndian.AppendUint16(data, uint16(caps&0xffff))
	data = append(data, 45)
	data = binary.LittleEndian.AppendUint16(data, 2)
	data = binary.LittleEndian.AppendUint16(data, uint16(caps>>16))
	data = append(data, 21)
	data = append(data, make([]byte, 10)...)
	data = append(data, []byte("901234567890")...)
	data = append(data, 0)
	data = append(data, plugin...)
	return append(data, 0)
}

// TLSConfig creates a client trust configuration and its matching local server certificate.
func TLSConfig(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: ServerName}, DNSNames: []string{ServerName}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	return &tls.Config{ServerName: ServerName, RootCAs: roots}, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// ModeFromStatus selects the compatibility mode represented by a fixture's server status.
func ModeFromStatus(status byte) config.Mode {
	if status&4 != 0 {
		return config.ModeOracle
	}
	return config.ModeMySQL
}

// ReadAuthResponse consumes one authentication response from the client.
func ReadAuthResponse(ctx context.Context, t *testing.T, p *wire.PacketIO) bool {
	t.Helper()
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return false
	}
	return true
}

// ServeCreateDatabaseAuth authenticates before database selection and enables the requested framing.
func ServeCreateDatabaseAuth(ctx context.Context, t *testing.T, p *wire.PacketIO, framing string, status byte) bool {
	t.Helper()
	if !SendPackets(ctx, t, p, ProtocolGreeting(framing == Compression)) {
		return false
	}
	data, err := p.ReadMessage(ctx)
	if err != nil || len(data) < 4 || binary.LittleEndian.Uint32(data)&uint32(wire.ClientConnectWithDB) != 0 {
		t.Error("database selected before creation", err)
		return false
	}
	var variables map[string]string
	if framing == OB20 {
		variables = map[string]string{testProxyCapability: "256"}
	}
	if !SendPackets(ctx, t, p, ProtocolOK(status, variables)) {
		return false
	}
	switch framing {
	case Compression:
		err = p.EnableCompression()
	case OB20:
		err = p.EnableOB20(wire.OB20Options{ConnectionID: 42})
	}
	if err != nil {
		t.Error(err)
		return false
	}
	return true
}

// ProtocolGreeting builds a greeting with session tracking and optional compression.
func ProtocolGreeting(compression bool) []byte {
	b := AuthGreeting(AuthNative, false)
	start := bytes.IndexByte(b[1:], 0) + 2 + 4 + 8 + 1
	low := binary.LittleEndian.Uint16(b[start:])
	if compression {
		low |= 32
	}
	binary.LittleEndian.PutUint16(b[start:], low)
	high := binary.LittleEndian.Uint16(b[start+5:]) | uint16((wire.ClientSessionTrack|wire.ClientConnectAttrs)>>16)
	binary.LittleEndian.PutUint16(b[start+5:], high)
	return b
}

// ProtocolOK builds an OK packet with optional capability session variables.
func ProtocolOK(status byte, values map[string]string) []byte {
	var state []byte
	for _, name := range []string{testProxyCapability, "__ob_client_capability_flag"} {
		if value, ok := values[name]; ok {
			pair := append([]byte{byte(len(name))}, name...)
			pair = append(pair, byte(len(value)))
			pair = append(pair, value...)
			state = append(state, 0, byte(len(pair)))
			state = append(state, pair...)
		}
	}
	if len(state) == 0 {
		return []byte{0, 0, 0, status, 0, 0, 0}
	}
	b := []byte{0, 0, 0, status, 0x40, 0, 0, 0, byte(len(state))}
	return append(b, state...)
}

// TextColumn builds one text-result column definition.
func TextColumn(name string, typ byte, charset uint16, flags uint16) []byte {
	data := []byte{3, 'd', 'e', 'f', 0, 0, 0, byte(len(name))}
	data = append(data, name...)
	return append(data, 0, 12, byte(charset), byte(charset>>8), 10, 0, 0, 0, typ, byte(flags), byte(flags>>8), 0, 0, 0)
}

// ReadQuery checks the next client query after resetting packet sequencing.
func ReadQuery(ctx context.Context, t testing.TB, p *wire.PacketIO, want string) bool {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil || string(b) != "\x03"+want {
		t.Errorf("expected query %q: %q %v", want, b, err)
		return false
	}
	return true
}

// SendPackets writes each packet and reports the first write failure.
func SendPackets(ctx context.Context, t testing.TB, p *wire.PacketIO, packets ...[]byte) bool {
	t.Helper()
	for _, b := range packets {
		if err := p.WriteMessage(ctx, b); err != nil {
			t.Error(err)
			return false
		}
	}
	return true
}

// ReadCommand checks the next client command after resetting packet sequencing.
func ReadCommand(ctx context.Context, t testing.TB, p *wire.PacketIO, want []byte) bool {
	t.Helper()
	p.ResetSequence()
	b, err := p.ReadMessage(ctx)
	if err != nil || !bytes.Equal(b, want) {
		t.Errorf("command got %x want %x: %v", b, want, err)
		return false
	}
	return true
}

// ServeOK checks one command and replies with an OK packet.
func ServeOK(ctx context.Context, t testing.TB, p *wire.PacketIO, want []byte, status byte) bool {
	t.Helper()
	p.ResetSequence()
	data, err := p.ReadMessage(ctx)
	if err != nil {
		t.Error(err)
		return false
	}
	if !bytes.Equal(data, want) {
		t.Errorf("command mismatch: got %q want %q", data, want)
		return false
	}
	if err = p.WriteMessage(ctx, []byte{0, 0, 0, status, 0, 0, 0}); err != nil {
		t.Error(err)
		return false
	}
	return true
}

// ServeBaseline handles the driver's initial session setup commands.
func ServeBaseline(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, deprecate ...bool) bool {
	return ServeOK(ctx, t, p, append([]byte{3}, []byte("SET NAMES utf8mb4")...), status) && ServeOK(ctx, t, p, append([]byte{3}, []byte("SET autocommit=1")...), status) && ServeVersion(ctx, t, p, status, len(deprecate) > 0 && deprecate[0]) && ServeOK(ctx, t, p, []byte("\x03SET SESSION time_zone = '+00:00'"), status)
}

// ServeVersion handles the server version query using the selected EOF capability.
func ServeVersion(ctx context.Context, t testing.TB, p *wire.PacketIO, status byte, deprecate bool) bool {
	query := "SELECT ob_version()"
	if status&4 != 0 {
		query += sessionReadDualSuffix
	}
	if !ReadQuery(ctx, t, p, query) {
		return false
	}
	if !SendPackets(ctx, t, p, []byte{1}, TextColumn("version", 253, 45, 0)) {
		return false
	}
	if !deprecate && !SendPackets(ctx, t, p, []byte{254, 0, 0, status, 0}) {
		return false
	}
	terminal := []byte{254, 0, 0, status, 0}
	if deprecate {
		terminal = []byte{254, 0, 0, status, 0, 0, 0}
	}
	return SendPackets(ctx, t, p, []byte{7, '4', '.', '3', '.', '5', '.', '1'}, terminal)
}

// Await waits up to two seconds for a fixture lifecycle transition.
func Await(t *testing.T, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-timer.C:
			t.Fatal("pool lifecycle did not settle within budget")
		case <-tick.C:
		}
	}
}

// UnixTempDir creates and cleans up a short temporary path suitable for Unix sockets.
func UnixTempDir(t *testing.T) string {
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

// ListenUnix opens a Unix listener under the supplied fixture directory.
func ListenUnix(t *testing.T, directory, name string) (net.Listener, string) {
	t.Helper()
	path := filepath.Join(directory, name)
	listener, err := net.Listen(networkUnix, path)
	if err != nil {
		t.Fatal(err)
	}
	return listener, path
}

// AuthPeerConn keeps fixture-wide deadlines while PacketIO changes per-message deadlines.
type AuthPeerConn struct{ net.Conn }

// SetReadDeadline keeps the fixture's underlying read deadline unchanged.
func (AuthPeerConn) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline keeps the fixture's underlying write deadline unchanged.
func (AuthPeerConn) SetWriteDeadline(time.Time) error { return nil }
