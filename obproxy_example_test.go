package oceanbase

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"time"
)

func ExampleNewOBProxyDialer() {
	// Build the C host, then use -tags=obproxy on Linux amd64/arm64 with trusted absolute paths.
	dial, err := NewOBProxyDialer(os.Getenv("OB_PROXY_HOST"), os.Getenv("OB_PROXY_LIBRARY"), "")
	if err != nil {
		panic(err)
	}
	connector, err := NewConnector(Config{
		User: "vc_app", Mode: ModeMySQL, Network: "unix",
		Addr: filepath.Join(".obproxy", "obproxy.domain"), DialContext: dial,
		// Configure deployment credentials and TLSConfig with an explicit ServerName.
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
	// No Output: compiled only. Native startup can outlive ctx; DB.Close does not stop the proxy.
}
