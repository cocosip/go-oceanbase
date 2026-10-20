package oceanbase_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func ExampleNewConnector() {
	cfg := oceanbase.Config{
		User:   "app@tenant",
		Mode:   oceanbase.ModeOracle,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	var answer int
	if err = db.QueryRowContext(context.Background(), "SELECT 42 FROM dual").Scan(&answer); err != nil {
		panic(err)
	}
}

func ExampleNewConnector_proxyUser() {
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: "proxy@tenant", Password: "proxy-password", ProxyUser: "client_user",
		Mode: oceanbase.ModeOracle,
	})
	// NewConnector validates configuration without opening a network connection.
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}

func ExampleNewConnector_createDatabase() {
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: "app@tenant", Database: "demo", Mode: oceanbase.ModeMySQL,
		CreateDatabaseIfMissing: true,
	})
	// Creating a Connector validates the policy; it does not create a database.
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}

func ExampleNewConnector_readTimeout() {
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: "timeout_app", ReadTimeout: 5 * time.Second,
	})
	fmt.Println(connector != nil, err == nil)
	// Output: true true
}
