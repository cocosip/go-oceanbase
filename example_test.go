package oceanbase_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"

	"github.com/cocosip/go-oceanbase"
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
