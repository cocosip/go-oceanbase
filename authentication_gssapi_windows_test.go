//go:build windows

package oceanbase

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/alexbrainman/sspi"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestAuthPluginGSSNativeHandshake(t *testing.T) {
	credentials, maxToken := nativeSSPICredentials(t)
	clientTLS, serverTLS := testTLSConfig(t)
	result, err := runAuthConnTest(t, Config{User: testUser, Password: "not-an-SSPI-password", TLSConfig: clientTLS}, nil, func(ctx context.Context, raw net.Conn, packets *wire.PacketIO) {
		if e := packets.WriteMessage(ctx, authGreeting(authNative, true)); e != nil {
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
		switchData := append(append([]byte{0xfe}, authGSSAPI...), 0)
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
	if err != nil || result == nil || !result.tlsVerified {
		t.Fatal("native SSPI over verified TLS", err)
	}
}

func nativeSSPICredentials(t *testing.T) (*sspi.Credentials, uint32) {
	t.Helper()
	probe, err := auth.NewGSSContext("service/localhost", "NTLM")
	if errors.Is(err, syscall.Errno(0x8009030e)) { // SEC_E_NO_CREDENTIALS
		t.Skip("this process has no Windows SSPI identity credentials")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = probe.Close(); err != nil {
		t.Fatal(err)
	}
	credentials, err := sspi.AcquireCredentials("", "NTLM", sspi.SECPKG_CRED_INBOUND, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e := credentials.Release(); e != nil {
			t.Error(e)
		}
	})
	info, err := sspi.QueryPackageInfo("NTLM")
	if err != nil {
		t.Fatal(err)
	}
	return credentials, info.MaxToken
}

func serveNativeSSPI(ctx context.Context, t *testing.T, packets *wire.PacketIO, credentials *sspi.Credentials, maxToken uint32, token []byte) bool {
	t.Helper()
	native := sspi.NewServerContext(credentials, sspi.ASC_REQ_CONNECTION)
	defer func() {
		if native.Handle != nil {
			if e := native.Release(); e != nil {
				t.Error(e)
			}
		}
	}()
	for round := 0; round < 2; round++ {
		if round != 0 || token == nil {
			var err error
			token, err = packets.ReadMessage(ctx)
			if err != nil {
				t.Error(err)
				return false
			}
		}
		input := []sspi.SecBuffer{{}}
		input[0].Set(sspi.SECBUFFER_TOKEN, token)
		output := []sspi.SecBuffer{{}}
		buffer := make([]byte, maxToken)
		output[0].Set(sspi.SECBUFFER_TOKEN, buffer)
		status := native.Update(nil, sspi.NewSecBufferDesc(output), sspi.NewSecBufferDesc(input))
		if round == 0 && status == sspi.SEC_I_CONTINUE_NEEDED {
			if e := packets.WriteMessage(ctx, buffer[:output[0].BufferSize]); e != nil {
				t.Error(e)
				return false
			}
		} else if round != 1 || status != sspi.SEC_E_OK {
			t.Error("native SSPI server did not complete", status)
			return false
		}
	}
	return true
}
