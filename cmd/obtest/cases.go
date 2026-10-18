package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"maps"

	"github.com/cocosip/go-oceanbase"
)

func caseDefinitions() []caseDefinition {
	return []caseDefinition{
		{name: "connect", description: "authentication, Ping and OceanBase version", run: runConnect},
		{name: "crud", description: "create/seed/read/update/delete, counts and SQL error recovery", run: runCRUD},
		{name: "prepare-server", description: "server prepared statement reuse, NULL and named binds", configure: serverPrepare, run: runPrepared},
		{name: "prepare-client", description: "client prepared statement reuse and escaping", configure: clientPrepare, run: runPrepared},
		{name: "scalars", description: "exact decimal, Unicode, binary, date/time and NULL", configure: scalarConfig, run: runScalars},
		{name: "oracle-scalars", description: "Oracle national text, TZ/LTZ, intervals, ROWID and typed NULL", oracleOnly: true, configure: oracleScalarConfig, run: runOracleScalars},
		{name: "transactions", description: "commit, rollback, savepoint and read-only transaction", run: runTransactions},
		{name: "metadata", description: "table/column/primary key dictionary and ColumnTypes", run: runMetadata},
		{name: "execution", description: "execution rows, snapshot and early close", run: runExecution},
		{name: "batch", description: "preferred strategies and fallback, duplicate attribution, stop/continue", run: runBatch},
		{name: "batch-rewrite", description: "require actual MySQL batch rewrite execution", mysqlOnly: true, configure: rewriteConfig, run: runBatchRewrite},
		{name: "batch-array", description: "require actual Oracle native array binding (OceanBase >= 4.2.1)", oracleOnly: true, configure: serverPrepare, run: runBatchArray},
		{name: "optimized-execution", description: "prepare-execute, statement/type caches and duplicate recovery", configure: optimizedConfig, run: runOptimized},
		{name: "reset-full", description: "reuse standard prepared statements across full pool resets", configure: fullResetConfig, run: runFullReset},
		{name: "multi-results", description: "consume multiple result sets and reuse the pinned session", mysqlOnly: true, configure: multiResultsConfig, run: runMultiResults},
		{name: "trace", description: "client request diagnostics and per-request trace context", run: runTrace},
		{name: "long-data", description: "stream binary/text parameters and verify content", configure: serverPrepare, run: runLongData},
		{name: "table-blob", description: "transactional BLOB write/read/truncate/rollback", run: runTableBlob},
		{name: "routines", description: "auto-create procedure/function, INOUT/OUT and failure atomicity", configure: serverPrepare, run: runRoutines},
		{name: "routines-client", description: "procedure/function outputs with client preparation", configure: clientPrepare, run: runRoutines},
		{name: "cursor", description: "forward and static cursor fetching", configure: serverPrepare, run: runCursor},
		{name: "cursor-update", description: "cursor update/refresh/insert/delete and rollback", configure: serverPrepare, run: runCursorUpdate},
		{name: "cursor-sensitive", description: "Oracle sensitive cursor observes another session's committed update", oracleOnly: true, configure: serverPrepare, run: runSensitiveCursor},
		{name: "returning", description: "Oracle RETURNING INTO outputs", oracleOnly: true, run: runReturning},
		{name: "native-lob", description: "Oracle BLOB/CLOB/NCLOB handles and transaction lifetime", oracleOnly: true, configure: nativeLOBConfig, run: runNativeLOB},
		{name: "complex", description: "Oracle OBJECT/ARRAY descriptors and exact values", oracleOnly: true, configure: serverPrepare, run: runComplex},
		{name: "resource-outputs", description: "Oracle REF CURSOR/BLOB/CLOB procedure outputs", oracleOnly: true, configure: serverPrepare, run: runResourceOutputs},
		{name: "xa-one-phase", description: "XA one-phase commit and visibility", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "one-phase") }},
		{name: "xa-two-phase", description: "XA prepare/recover/commit and visibility", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "two-phase") }},
		{name: "xa-rollback", description: "XA rollback and visibility", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "rollback") }},
		{name: "xa-suspend", description: "XA suspend/resume and commit", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "suspend") }},
		{name: "xa-join", description: "XA end/join and commit", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "join") }},
		{name: "xa-recovery", description: "XA recover on a fresh physical connection", run: func(ctx context.Context, s *scenario) error { return runXA(ctx, s, "recovery") }},
		{name: "session", description: "schema/catalog, variables, client info and pool isolation", run: runSession},
		{name: "warnings", description: "protocol warning count, details and clear", run: runWarnings},
		{name: "cancel-context", description: "cancel a lock wait and verify a fresh connection", run: runContextCancel},
		{name: "cancel-query", description: "one-shot server cancellation of an automatically held row lock", run: runCancelQuery},
		{name: "change-user", description: "change identity and verify original pool isolation; requires second DSN", run: runChangeUser},
		{name: "local-import", description: "auto-seeded LOCAL INFILE import, rollback and commit", run: runLocalImport},
	}
}

func serverPrepare(cfg *oceanbase.Config) { cfg.PrepareMode = oceanbase.PrepareServer }
func clientPrepare(cfg *oceanbase.Config) { cfg.PrepareMode = oceanbase.PrepareClient }

func scalarConfig(cfg *oceanbase.Config) {
	cfg.Session.ParseTime = false
	serverPrepare(cfg)
}

func oracleScalarConfig(cfg *oceanbase.Config) {
	scalarConfig(cfg)
	cfg.Session.NCharset = "utf16"
}

func nativeLOBConfig(cfg *oceanbase.Config) {
	cfg.Session.NCharset = "utf16"
	serverPrepare(cfg)
}

func rewriteConfig(cfg *oceanbase.Config) {
	cfg.AllowMultiStatements = true
	cfg.Session.Variables = maps.Clone(cfg.Session.Variables)
	if cfg.Session.Variables == nil {
		cfg.Session.Variables = make(map[string]string)
	}
	cfg.Session.Variables["ob_enable_batched_multi_statement"] = "0"
}

func optimizedConfig(cfg *oceanbase.Config) {
	serverPrepare(cfg)
	cfg.Execution = oceanbase.ExecutionOptions{PrepareExecute: true, StatementCacheSize: 4, ReuseParameterTypes: true, StatementChecksum: true}
}

func fullResetConfig(cfg *oceanbase.Config)    { cfg.ResetMode = oceanbase.ResetFull }
func multiResultsConfig(cfg *oceanbase.Config) { cfg.AllowMultiStatements = true }

func closeInto(c io.Closer, result *error) { *result = errors.Join(*result, c.Close()) }

func rollbackInto(tx *sql.Tx, result *error) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		*result = errors.Join(*result, err)
	}
}

func withSession(ctx context.Context, db *sql.DB, run func(*sql.Conn) error) (err error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer closeInto(c, &err)
	return run(c)
}
