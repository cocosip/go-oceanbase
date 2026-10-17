package oceanbase

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/proxy"
)

func ExampleConfig_socks5() {
	// Use an explicit proxy, not process-wide proxy environment variables.
	dial, err := socksExampleDialer("127.0.0.1:1080", nil)
	if err != nil {
		panic(err)
	}
	connector, err := NewConnector(Config{
		User: "socks_app", Addr: "database.internal:2881", Mode: ModeMySQL,
		DialContext: dial,
		// Supply the real database's TLSConfig when encryption/identity verification is required.
	})
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		panic(err)
	}
	// No Output: this example is compiled, not run against a real proxy or database.
}

// This application helper keeps SOCKS policy outside the driver; auth is independent of DB credentials.
func socksExampleDialer(address string, auth *proxy.Auth) (func(context.Context, string, string) (net.Conn, error), error) {
	dialer, err := proxy.SOCKS5("tcp", address, auth, &net.Dialer{})
	if err != nil {
		return nil, err
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("SOCKS dialer does not support context")
	}
	return contextDialer.DialContext, nil
}
