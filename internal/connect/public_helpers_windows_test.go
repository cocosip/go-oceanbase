//go:build windows

package connect_test

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"syscall"
	"testing"

	"github.com/alexbrainman/sspi"
	oceanbase "github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/auth"
	"github.com/cocosip/go-oceanbase/internal/testpeer"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func sharedMemoryDB(t *testing.T, cfg config.Config, serve func(context.Context, *wire.PacketIO)) *sql.DB {
	t.Helper()
	return sharedMemoryDBConn(t, cfg, func(ctx context.Context, _ net.Conn, p *wire.PacketIO) { serve(ctx, p) })
}

func sharedMemoryDBConn(t *testing.T, cfg config.Config, serve func(context.Context, net.Conn, *wire.PacketIO)) *sql.DB {
	t.Helper()
	p := testpeer.NewSharedMemoryPeer(t)
	cfg.Network, cfg.Addr, cfg.User = networkSharedMemory, p.Name, testUser
	created, err := oceanbase.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := &testpeer.SharedMemoryServer{Peer: p}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = server.Close() }()
		<-p.Ready
		serve(context.Background(), server, wire.NewPacketIO(server, 64<<20))
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	db := sql.OpenDB(created)
	t.Cleanup(func() { _ = db.Close() })
	return db
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

const networkSharedMemory = "sharedmemory"

const networkPipe = "pipe"

const testSSPIPassword = "not-an-SSPI-password"
const testNativeFinalUser = "final@tenant"
