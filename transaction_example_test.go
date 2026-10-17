package oceanbase_test

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func ExampleNewConnector_readOnlyTransactions() {
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: "readonly_app", Addr: "readonly.example:2881",
		Routing: oceanbase.RoutingOptions{Role: oceanbase.RoleReadOnly},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// ReadOnly configures this transaction; the role above selects the service for new connections.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	var count int64
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders").Scan(&count); err != nil {
		fmt.Println(err)
		return
	}
	if err = tx.Commit(); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(count)
}
