//go:build windows

package connect_test

import (
	bytes "bytes"
	context "context"
	tls "crypto/tls"
	sql "database/sql"
	driver "database/sql/driver"
	errors "errors"
	fmt "fmt"
	io "io"
	net "net"
	os "os"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	winio "github.com/Microsoft/go-winio"
	sspi "github.com/alexbrainman/sspi"
	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
	wire "github.com/cocosip/go-oceanbase/internal/wire"
)

func TestPlatformTransportNamedPipeMixedSessions(t *testing.T) {
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, framing := range []string{testStandard, testCompression, testOB20} {
			t.Run(fmt.Sprintf("%d/%s", mode, framing), func(t *testing.T) {
				path := fmt.Sprintf(`\\.\pipe\go-oceanbase-mixed-%d-%d`, os.Getpid(), time.Now().UnixNano())
				listener, err := winio.ListenPipe(path, nil)
				if err != nil {
					t.Fatal(err)
				}
				runPlatformMixedSession(t, listener, config.Config{Network: networkPipe, Addr: path, Mode: mode}, framing)
			})
		}
	}
}

func TestAuthPluginGSSNativeSessions(t *testing.T) {
	credentials, maxToken := nativeSSPICredentials(t)
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/reject=%t", mode, reject), func(t *testing.T) {
				testNativeSSPISession(t, credentials, maxToken, mode, reject)
			})
		}
	}
}

func testNativeSSPISession(t *testing.T, credentials *sspi.Credentials, maxToken uint32, mode config.Mode, reject bool) {
	t.Helper()
	clientTLS, serverTLS := testpeer.TLSConfig(t)
	status := byte(2)
	if mode == config.ModeOracle {
		status = 6
	}
	var dials atomic.Int32
	var rawTransport net.Conn
	peerDone := make(chan struct{})
	cfg := config.Config{User: testUser, Password: testSSPIPassword, Mode: mode, TLSConfig: clientTLS,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dials.Add(1)
			var err error
			rawTransport, err = testpeer.RoutingTLSPeer(ctx, t, func(raw net.Conn) {
				defer close(peerDone)
				serveNativeSSPISession(t, raw, serverTLS, credentials, maxToken, mode, status, reject)
			})
			return rawTransport, err
		}}
	connector, err := oceanbase.NewConnector(cfg)
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
	var physical driver.Validator
	if err = pinned.Raw(func(raw any) error { physical = raw.(driver.Validator); return nil }); err != nil {
		t.Fatal(err)
	}
	for i, user := range []string{testUser, changeUserName, testNativeFinalUser} {
		if i != 0 {
			err = oceanbase.ChangeUser(ctx, pinned, config.Credentials{User: user, Password: "different-unused-password"}, "")
		}
		if i == 2 && reject {
			var serverError *oceanbase.Error
			if !errors.As(err, &serverError) || serverError.Code != 1045 || serverError.SQLState != "28000" || physical.IsValid() {
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
		if err = pinned.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = pinned.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) || db.Stats().OpenConnections != 0 || physical.IsValid() || dials.Load() != 1 {
		t.Fatalf("native identity did not close its original connection: %v %+v dials=%d", err, db.Stats(), dials.Load())
	}
	if err = rawTransport.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("native identity left its underlying TCP connection open", err)
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("native authentication peer did not finish", ctx.Err())
	}
}

func serveNativeSSPISession(t *testing.T, raw net.Conn, serverTLS *tls.Config, credentials *sspi.Credentials, maxToken uint32, mode config.Mode, status byte, reject bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := wire.NewPacketIO(raw, 64<<20)
	if !testpeer.SendPackets(ctx, t, p, testpeer.AuthGreeting(authentication.Native, true)) {
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
	switchPacket := append(append([]byte{0xfe}, authentication.GSSAPI...), 0)
	if !testpeer.SendPackets(ctx, t, p, append(switchPacket, nativeSSPISeed...)) {
		return
	}
	var token []byte
	for i, user := range []string{testUser, changeUserName, testNativeFinalUser} {
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
			if !testpeer.SendPackets(ctx, t, p, []byte{0xff, 0x15, 0x04, '#', '2', '8', '0', '0', '0', 'x'}) {
				return
			}
			if data, err := p.ReadMessage(ctx); !errors.Is(err, io.EOF) {
				t.Errorf("rejected native identity did not close transport: bytes=%d err=%v", len(data), err)
			}
			return
		}
		if !testpeer.SendPackets(ctx, t, p, commandPacket(0, 0, status)) || !testpeer.ServeBaseline(ctx, t, p, status) || !testpeer.ServeOK(ctx, t, p, []byte{14}, status) {
			return
		}
	}
	p.ResetSequence()
	testpeer.ServeQuit(t, p)
}

func readNativeSSPIChange(ctx context.Context, t *testing.T, p *wire.PacketIO, mode config.Mode, user string) ([]byte, bool) {
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
	if mode == config.ModeOracle {
		database, _, _ = strings.Cut(user, "@")
	}
	want := []byte(database + "\x00" + authentication.GSSAPI + "\x00")
	if !bytes.Equal(suffix, want) || !bytes.HasPrefix(token, []byte("NTLMSSP\x00")) {
		t.Error("native change-user plugin/database or initial token mismatch")
		return nil, false
	}
	return token, true
}

const nativeSSPISeed = "service/localhost\x00NTLM\x00"
