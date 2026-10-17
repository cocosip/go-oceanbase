//go:build windows

package oceanbase

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexbrainman/sspi"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const nativeSSPISeed = "service/localhost\x00NTLM\x00"

func TestAuthPluginGSSNativeSessions(t *testing.T) {
	credentials, maxToken := nativeSSPICredentials(t)
	for _, mode := range []Mode{ModeMySQL, ModeOracle} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/reject=%t", mode, reject), func(t *testing.T) {
				testNativeSSPISession(t, credentials, maxToken, mode, reject)
			})
		}
	}
}

func testNativeSSPISession(t *testing.T, credentials *sspi.Credentials, maxToken uint32, mode Mode, reject bool) {
	t.Helper()
	clientTLS, serverTLS := testTLSConfig(t)
	status := byte(2)
	if mode == ModeOracle {
		status = 6
	}
	var dials atomic.Int32
	peerDone := make(chan struct{})
	cfg := Config{User: testUser, Password: "not-an-SSPI-password", Mode: mode, TLSConfig: clientTLS,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dials.Add(1)
			return routingTLSPeer(ctx, t, func(raw net.Conn) {
				defer close(peerDone)
				serveNativeSSPISession(t, raw, serverTLS, credentials, maxToken, mode, status, reject)
			})
		}}
	connector, err := NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pinned, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pinned.Close() }()
	var physical *conn
	if err = pinned.Raw(func(raw any) error { physical = raw.(*conn); return nil }); err != nil {
		t.Fatal(err)
	}
	for i, user := range []string{testUser, changeUserName, "final@tenant"} {
		if i != 0 {
			err = ChangeUser(ctx, pinned, Credentials{User: user, Password: "different-unused-password"}, "")
		}
		if i == 2 && reject {
			var serverError *Error
			if !errors.As(err, &serverError) || serverError.Code != 1045 || serverError.SQLState != "28000" || !physical.closed.Load() {
				t.Fatalf("native authentication final rejection: %v", err)
			}
			if pinned.PingContext(ctx) == nil {
				t.Fatal("rejected native identity retained a usable session")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		checkNativeSSPIIdentity(t, physical, mode, user, i != 0)
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) || db.Stats().OpenConnections != 0 || !physical.closed.Load() || dials.Load() != 1 {
		t.Fatalf("native identity did not close its original connection: %v %+v dials=%d", err, db.Stats(), dials.Load())
	}
	if err = physical.session.raw.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("native identity left its underlying TCP connection open", err)
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("native authentication peer did not finish", ctx.Err())
	}
}

func checkNativeSSPIIdentity(t *testing.T, physical *conn, mode Mode, user string, changed bool) {
	t.Helper()
	if !physical.session.tlsVerified || physical.session.mode != mode || physical.session.user != user ||
		physical.session.authPlugin != authGSSAPI || string(physical.session.authChallenge) != nativeSSPISeed {
		t.Fatal("native authentication lost verified transport or identity state")
	}
	if changed && (physical.cfg.Password != "" || !physical.sessionDirty) {
		t.Fatal("native identity retained its password or pool eligibility")
	}
}

func serveNativeSSPISession(t *testing.T, raw net.Conn, serverTLS *tls.Config, credentials *sspi.Credentials, maxToken uint32, mode Mode, status byte, reject bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := wire.NewPacketIO(raw, 64<<20)
	if !sendPackets(ctx, t, p, authGreeting(authNative, true)) {
		return
	}
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return
	}
	secure := tls.Server(raw, serverTLS)
	if err := secure.HandshakeContext(ctx); err != nil {
		t.Error(err)
		return
	}
	if err := p.ReplaceConn(secure); err != nil {
		t.Error(err)
		return
	}
	if _, err := p.ReadMessage(ctx); err != nil {
		t.Error(err)
		return
	}
	switchPacket := append(append([]byte{0xfe}, authGSSAPI...), 0)
	if !sendPackets(ctx, t, p, append(switchPacket, nativeSSPISeed...)) {
		return
	}
	var token []byte
	for i, user := range []string{testUser, changeUserName, "final@tenant"} {
		if i != 0 {
			var ok bool
			token, ok = readNativeSSPIChange(ctx, t, p, mode, user)
			if !ok {
				return
			}
		}
		if !serveNativeSSPI(ctx, t, p, credentials, maxToken, token) {
			return
		}
		if i == 2 && reject {
			if !sendPackets(ctx, t, p, []byte{0xff, 0x15, 0x04, '#', '2', '8', '0', '0', '0', 'x'}) {
				return
			}
			if data, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) {
				t.Errorf("rejected native identity did not close transport: bytes=%d err=%v", len(data), err)
			}
			return
		}
		if !sendPackets(ctx, t, p, commandPacket(0, 0, status)) || !serveBaseline(ctx, t, p, status) || !serveOK(ctx, t, p, []byte{14}, status) {
			return
		}
	}
	p.ResetSequence()
	serveQuit(t, p)
}

func readNativeSSPIChange(ctx context.Context, t *testing.T, p *wire.PacketIO, mode Mode, user string) ([]byte, bool) {
	t.Helper()
	p.ResetSequence()
	data, err := p.ReadMessage(ctx)
	prefix := append(append([]byte{17}, user...), 0)
	if err != nil || !bytes.HasPrefix(data, prefix) || len(data) <= len(prefix) {
		t.Errorf("native change-user prefix: bytes=%d err=%v", len(data), err)
		return nil, false
	}
	data = data[len(prefix):]
	length := int(data[0])
	if length > len(data)-1 || length < 8 {
		t.Error("native change-user token length", length)
		return nil, false
	}
	token, suffix := data[1:1+length], data[1+length:]
	database := ""
	if mode == ModeOracle {
		database, _, _ = strings.Cut(user, "@")
	}
	want := []byte(database + "\x00" + authGSSAPI + "\x00")
	if !bytes.Equal(suffix, want) || !bytes.HasPrefix(token, []byte("NTLMSSP\x00")) {
		t.Error("native change-user plugin/database or initial token mismatch")
		return nil, false
	}
	return token, true
}
