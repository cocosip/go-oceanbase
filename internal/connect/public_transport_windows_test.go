//go:build windows

package connect_test

import (
	"fmt"
	winio "github.com/Microsoft/go-winio"
	"os"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
)

func TestPlatformTransportNamedPipeTLSGate(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\go-oceanbase-tls-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := winio.ListenPipe(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformTLSGate(t, listener, config.Config{User: testUser, Password: testPassword, Network: networkPipe, Addr: path})
}
