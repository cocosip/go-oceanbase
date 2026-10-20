package connect_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func TestPlatformTransportDialContext(t *testing.T) {
	var seen bool
	created, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Network: "unix", Addr: "/socket", DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
		seen = network == "unix" && addr == "/socket"
		return nil, context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = created.Connect(context.Background()); !errors.Is(err, context.Canceled) || !seen {
		t.Fatal("custom network mapping", err)
	}
}

func TestPlatformTransportPipeUTF8Connector(t *testing.T) {
	for _, invalid := range []string{"\xff", "\xc3", "\xc0\xaf", "\xed\xa0\x80", "\xf4\x90\x80\x80"} {
		for _, addr := range []string{`\\.\pipe\oceanbase-` + invalid, `\\db-` + invalid + `\pipe\oceanbase`} {
			t.Run(fmt.Sprintf("%x", addr), func(t *testing.T) {
				created, err := oceanbase.NewConnector(oceanbase.Config{User: testUser, Network: "pipe", Addr: addr})
				var config *oceanbase.ConfigError
				if created != nil || !errors.As(err, &config) || config.Field != "Addr" {
					t.Errorf("invalid UTF-8 pipe Config accepted: %v", err)
				}
				_, err = oceanbase.ParseDSN("oceanbase://user@localhost/?network=pipe&socket=" + url.QueryEscape(addr))
				if !errors.As(err, &config) || config.Field != "Addr" {
					t.Errorf("invalid UTF-8 pipe DSN accepted: %v", err)
				}
			})
		}
	}
}
