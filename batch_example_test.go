package oceanbase_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"

	oceanbase "github.com/cocosip/go-oceanbase"
)

func ExampleExecBatch_rewrite() {
	cfg := oceanbase.Config{
		User: "app@tenant#cluster", Addr: "localhost:2883", Mode: oceanbase.ModeMySQL,
		AllowMultiStatements: true,
		Logger:               slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	connector, err := oceanbase.NewConnector(cfg)
	if err != nil {
		return
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	result, err := oceanbase.ExecBatch(context.Background(), db,
		"INSERT INTO events(id, name) VALUES (?, ?)",
		[][]any{{1, "first"}, {2, "second"}},
		oceanbase.BatchOptions{Strategy: oceanbase.BatchRewrite, ChunkRows: 128},
	)
	// Inspect result.Items even on error; an acknowledged prefix can have succeeded.
	_, _ = result, err
}

func ExampleExecBatch_arrayBinding() {
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		User: "app@tenant#cluster", Addr: "localhost:2883", Mode: oceanbase.ModeOracle,
		Logger: slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	})
	if err != nil {
		return
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	result, err := oceanbase.ExecBatch(ctx, tx,
		"INSERT INTO events(id, name) VALUES (?, ?)",
		[][]any{{1, "first"}, {2, "second"}},
		oceanbase.BatchOptions{Strategy: oceanbase.BatchArrayBinding, ContinueOnError: true},
	)
	// Multiple successful inputs have unknown individual counts; inspect result.Items on error.
	_ = result
	if err != nil {
		return
	}
	// Only the application decides whether to commit the original transaction.
	if err = tx.Commit(); err != nil {
		return
	}
}
