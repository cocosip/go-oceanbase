package connect_test

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
)

const testSharedMemoryName = "MYSQL"

func TestPlatformTransportSharedMemoryDialContext(t *testing.T) {
	for _, name := range []string{testSharedMemoryName, `Local\MYSQL`, `Global\MYSQL`, "数据库", strings.Repeat("数", 220), strings.Repeat("😀", 110)} {
		cfg, err := oceanbase.ParseDSN("oceanbase://app@localhost/?network=sharedmemory&socket=" + url.QueryEscape(name))
		if err != nil || cfg.Network != "sharedmemory" || cfg.Addr != name {
			t.Fatal("shared memory DSN rejected", err)
		}
		seen := false
		cfg.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
			seen = network == "sharedmemory" && addr == name
			return nil, context.Canceled
		}
		created, err := oceanbase.NewConnector(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = created.Connect(context.Background()); !errors.Is(err, context.Canceled) || !seen {
			t.Fatal("shared memory callback identity lost", err)
		}
	}
}
