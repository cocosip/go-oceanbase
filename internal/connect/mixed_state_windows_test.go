//go:build windows

package connect_test

import (
	context "context"
	errors "errors"
	fmt "fmt"
	net "net"
	atomic "sync/atomic"
	testing "testing"
	time "time"
	_ "time/tzdata"

	sspi "github.com/alexbrainman/sspi"
	oceanbase "github.com/cocosip/go-oceanbase"
	config "github.com/cocosip/go-oceanbase/config"
	authentication "github.com/cocosip/go-oceanbase/internal/authentication"
	catalog "github.com/cocosip/go-oceanbase/internal/catalog"
	command "github.com/cocosip/go-oceanbase/internal/command"
	control "github.com/cocosip/go-oceanbase/internal/control"
	cursor "github.com/cocosip/go-oceanbase/internal/cursor"
	lob "github.com/cocosip/go-oceanbase/internal/lob"
	statement "github.com/cocosip/go-oceanbase/internal/statement"
	testpeer "github.com/cocosip/go-oceanbase/internal/testpeer"
)

func TestAuthPluginGSSNativeSessionState(t *testing.T) {
	credentials, maxToken := nativeSSPICredentials(t)
	for _, mode := range []config.Mode{config.ModeMySQL, config.ModeOracle} {
		for _, reject := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/reject=%t", mode, reject), func(t *testing.T) { checkNativeSSPISessionState(t, credentials, maxToken, mode, reject) })
		}
	}
}
func checkNativeSSPISessionState(t *testing.T, credentials *sspi.Credentials, maxToken uint32, mode config.Mode, reject bool) {
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
	s, op := ownedMixedSession(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := control.New()
	cache, resolver, cursors, lobs := &statement.Cache{}, &catalog.Resolver{}, &cursor.Manager{}, &lob.Manager{}
	for i, user := range []string{testUser, changeUserName, testNativeFinalUser} {
		var err error
		if i != 0 {
			err = m.ChangeUser(ctx, op, cache, resolver, cursors, lobs, config.Credentials{User: user, Password: "different-unused-password"}, "", false)
		}
		if i == 2 && reject {
			var server *oceanbase.Error
			if !errors.As(err, &server) || server.Code != 1045 || server.SQLState != "28000" || !s.Closed() {
				t.Fatal("native authentication final rejection", err)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		p := op.Protocol()
		if !p.TLSVerified || p.Mode != mode || p.User != user || p.AuthPlugin != authentication.GSSAPI || string(p.AuthChallenge) != nativeSSPISeed {
			t.Fatal("native authentication lost verified transport or identity state")
		}
		if i != 0 && (op.Config().Password != "" || !s.Dirty()) {
			t.Fatal("native identity retained password or pool eligibility")
		}
		if _, err = command.ExecOK(ctx, op, []byte{14}); err != nil {
			t.Fatal(err)
		}
	}
	closeOwnedMixedSession(t, s, op)
	if !s.Closed() || dials.Load() != 1 {
		t.Fatal("native identity did not close original session", dials.Load())
	}
	if err := rawTransport.SetDeadline(time.Now()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("native TCP connection left open", err)
	}
	select {
	case <-peerDone:
	case <-ctx.Done():
		t.Fatal("native authentication peer did not finish", ctx.Err())
	}
}
