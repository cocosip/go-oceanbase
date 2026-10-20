//go:build windows

package connect_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/authentication"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAuthPluginGSSNativeHandshake(t *testing.T) {
	credentials, maxToken := nativeSSPICredentials(t)
	clientTLS, serverTLS := testpeer.TLSConfig(t)
	result, err := runAuthConnTest(t, config.Config{User: testUser, Password: testSSPIPassword, TLSConfig: clientTLS}, nil, func(ctx context.Context, raw net.Conn, packets *wire.PacketIO) {
		if e := packets.WriteMessage(ctx, testpeer.AuthGreeting(authentication.Native, true)); e != nil {
			t.Error(e)
			return
		}
		if _, e := packets.ReadMessage(ctx); e != nil {
			t.Error(e)
			return
		}
		secure := tls.Server(raw, serverTLS)
		if e := secure.HandshakeContext(ctx); e != nil {
			t.Error(e)
			return
		}
		if e := packets.ReplaceConn(secure); e != nil {
			t.Error(e)
			return
		}
		if _, e := packets.ReadMessage(ctx); e != nil {
			t.Error(e)
			return
		}
		switchData := append(append([]byte{0xfe}, authentication.GSSAPI...), 0)
		if e := packets.WriteMessage(ctx, append(switchData, []byte("service/localhost\x00NTLM\x00")...)); e != nil {
			t.Error(e)
			return
		}
		if !serveNativeSSPI(ctx, t, packets, credentials, maxToken, nil) {
			return
		}
		if e := packets.WriteMessage(ctx, []byte{0, 0, 0, 2, 0, 0, 0}); e != nil {
			t.Error(e)
		}
	})
	if err != nil || result == nil || !result.TLSVerified {
		t.Fatal("native SSPI over verified TLS", err)
	}
}
