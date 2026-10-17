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
		Session: oceanbase.SessionOptions{Variables: map[string]string{
			"ob_enable_batched_multi_statement": "0",
		}},
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
