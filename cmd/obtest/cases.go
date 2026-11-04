package main

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/cocosip/go-oceanbase"
)

func caseDefinitions() []caseDefinition {
	return []caseDefinition{
		{name: "connect", description: "authentication, Ping and OceanBase version", run: runConnect},
		{name: "crud", description: "create/seed/read/update/delete, counts and SQL error recovery", run: runCRUD},
		{name: "counts-matched", description: "matched row counts across Exec, Execute and sequential batch", configure: func(cfg *oceanbase.Config) { cfg.RowsAffectedMode = oceanbase.MatchedRows }, run: runRowCounts},
		{name: "counts-changed", description: "MySQL changed row counts, no-op updates and native upsert counts", mysqlOnly: true, configure: func(cfg *oceanbase.Config) { cfg.RowsAffectedMode = oceanbase.ChangedRows }, run: runRowCounts},
		{name: "generated-keys", description: "MySQL generated IDs, uint64 summaries, int64 overflow and absent IDs", mysqlOnly: true, run: runGeneratedKeys},
		{name: "prepare-server", description: "server prepared statement reuse, NULL and named binds", configure: serverPrepare, run: runPrepared},
		{name: "prepare-client", description: "client prepared statement reuse and escaping", configure: clientPrepare, run: runPrepared},
		{name: "prepare-fallback", description: "explicit server PREPARE 1295 falls back to client execution", mysqlOnly: true, run: runPrepareFallback},
		{name: "scalars", description: "exact decimal, Unicode, binary, date/time and NULL", configure: scalarConfig, run: runScalars},
		{name: "mysql-scalars", description: "MySQL TIME/YEAR, integer boundaries, BIT, binary padding and metadata", mysqlOnly: true, configure: scalarConfig, run: runMySQLScalars},
		{name: "oracle-scalars", description: "Oracle national text, TZ/LTZ, intervals, ROWID and typed NULL", oracleOnly: true, configure: oracleScalarConfig, run: runOracleScalars},
		{name: "temporal-time-server", description: "ParseTime date/timestamp/TZ/LTZ scans, precision, metadata and NULL with server preparation", configure: temporalTimeConfig, run: runTemporalTimes},
		{name: "temporal-time-client", description: "ParseTime date/timestamp/TZ/LTZ scans, precision, metadata and NULL with client preparation", configure: func(cfg *oceanbase.Config) { temporalTimeConfig(cfg); clientPrepare(cfg) }, run: runTemporalTimes},
		{name: "transactions", description: "commit, rollback, savepoint and read-only transaction", run: runTransactions},
		{name: "metadata", description: "table/column/primary key dictionary and ColumnTypes", run: runMetadata},
		{name: "metadata-parameters", description: "describe parameters without executing SQL, including Oracle repeated names", run: runParameterMetadata},
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
		{name: "native-lob", description: "Oracle BLOB/CLOB handles and transaction lifetime", oracleOnly: true, configure: nativeLOBConfig, run: runNativeLOB},
		{name: "native-nclob", description: "Oracle NCLOB datatype and Unicode locator reads", oracleOnly: true, configure: nativeLOBConfig, run: runNativeNCLOB},
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
		{name: "floats", description: "MySQL DOUBLE / Oracle BINARY_DOUBLE bit-exact round trips, extremes, negative zero and NULL", run: runFloats},
		{name: "extended-types", description: "MySQL VECTOR/SPARSEVECTOR/ARRAY/ROARINGBITMAP/JSON text, NULL, metadata and rejection", mysqlOnly: true, run: runExtendedTypes},
		{name: "geometry", description: "MySQL GEOMETRY SRID-0 text/bytes round trip and missing-SRS rejection", mysqlOnly: true, run: runGeometry},
		{name: "mysql-charset", description: "MySQL strict legacy charset columns, frozen server bytes and legacy result decoding", mysqlOnly: true, run: runMySQLCharset},
		{name: "mysql-utf16le", description: "MySQL utf16le result conversion, surrogate-pair bytes and VARCHAR metadata", mysqlOnly: true, run: runMySQLUTF16LE},
		{name: "mysql-enum-set", description: "MySQL ENUM/SET text normalization, metadata and invalid member rejection", mysqlOnly: true, run: runMySQLEnumSet},
		{name: "mysql-generated-columns", description: "MySQL STORED/VIRTUAL generated columns, recomputation and write rejection", mysqlOnly: true, run: runMySQLGeneratedColumns},
		{name: "oracle-spatial", description: "Oracle SDO_GEOMETRY literal storage, member access, OBJECT decoding and invalid SRID", oracleOnly: true, run: runOracleSpatial},
		{name: "oracle-urowid", description: "Oracle long UROWID capture, NULL/rebind round trips and IN OUT binding", oracleOnly: true, run: runOracleURowID},
		{name: "oracle-xml", description: "Oracle XMLTYPE serialization, NULL and XMLTYPE/string metadata", oracleOnly: true, run: runOracleXML},
		{name: "oracle-number-scale", description: "Oracle NUMBER(p,-s) rounding and NUMBER(p,s>p) limits with declared precision/scale", oracleOnly: true, run: runOracleNumberScale},
		{name: "oracle-binary-float", description: "Oracle BINARY_FLOAT float32 precision with NaN/Inf and BINARY_DOUBLE exactness", oracleOnly: true, run: runOracleBinaryFloat},
		{name: "oracle-synonym", description: "Oracle view/synonym reads, synonym writes and synonym dictionary identity", oracleOnly: true, run: runOracleSynonym},
		{name: "char-padding", description: "CHAR blank padding per mode, comparisons, VARCHAR and over-length rejection", run: runCharPadding},
		{name: "empty-lob", description: "empty LOB versus SQL NULL for plain scans, bound zero-length values and NULL flags", run: runEmptyLOB},
		{name: "metadata-patterns", description: "dictionary exact-name escaping versus LIKE wildcard patterns", run: runMetadataPatterns},
		{name: "oracle-identity", description: "Oracle identity keys, ALWAYS rejection and dictionary auto-increment flag", oracleOnly: true, run: runOracleIdentity},
		{name: "metadata-indexes", description: "index uniqueness/column order and server-info dictionary identity", run: runMetadataIndexes},
		{name: "metadata-routines", description: "routine kinds and IN/OUT/RETURN parameter dictionaries", run: runMetadataRoutines},
		{name: "statement-parameters", description: "prepared statement placeholder ordinals, types and binding", run: runStatementParameters},
		{name: "metadata-type-info", description: "built-in type families, documented limits and auto-increment flags", run: runMetadataTypeInfo},
		{name: "views", description: "updatable view writes, read-only predicate and view dictionaries", run: runViews},
		{name: "identifier-length", description: "maximum table/column identifiers through DDL, DML and metadata", run: runIdentifierLength},
		{name: "topology-sources", description: "inline TNS, tnsnames file and OCP discovery sources without a database", run: runTopologySources},
	}
}

func serverPrepare(cfg *oceanbase.Config) { cfg.PrepareMode = oceanbase.PrepareServer }
func clientPrepare(cfg *oceanbase.Config) { cfg.PrepareMode = oceanbase.PrepareClient }

func scalarConfig(cfg *oceanbase.Config) {
	cfg.Session.ParseTime = false
	cfg.Session.CompatibleMysqlVersion = 8
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
}

func optimizedConfig(cfg *oceanbase.Config) {
	serverPrepare(cfg)
	cfg.Execution = oceanbase.ExecutionOptions{PrepareExecute: true, StatementCacheSize: 4, ReuseParameterTypes: true, StatementChecksum: true}
}

func fullResetConfig(cfg *oceanbase.Config)    { cfg.ResetMode = oceanbase.ResetFull }
func multiResultsConfig(cfg *oceanbase.Config) { cfg.AllowMultiStatements = true }

func duplicateKey(err error, mode oceanbase.Mode) bool {
	code := uint32(1062)
	if mode == oceanbase.ModeOracle {
		code = 1
	}
	return onlyServerErrorCode(err, code)
}

func onlyServerErrorCode(err error, code uint32, allowed ...error) bool {
	for _, sentinel := range allowed {
		if err == sentinel {
			return true
		}
	}
	if group, ok := err.(interface{ Unwrap() []error }); ok {
		causes := group.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyServerErrorCode(cause, code, allowed...) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyServerErrorCode(wrapped.Unwrap(), code, allowed...)
	}
	server, ok := err.(*oceanbase.Error)
	return ok && server != nil && server.Code == code
}

func xaFlagRejected(err error, mode oceanbase.Mode) bool {
	var server *oceanbase.Error
	return mode == oceanbase.ModeMySQL && errors.As(err, &server) && server != nil && onlyServerErrorCode(err, 1398, oceanbase.ErrXA)
}

func checkBatchDuplicate(result oceanbase.BatchResult, err error, mode oceanbase.Mode, keepGoing bool) error {
	completed, last := 1, oceanbase.BatchNotExecuted
	if keepGoing {
		completed, last = 2, oceanbase.BatchSucceeded
	}
	var batch *oceanbase.BatchError
	if !errors.As(err, &batch) || batch == nil || batch.Index != 1 || batch.Completed != completed || len(result.Items) != 3 {
		return checkFailure("batch duplicate key attribution mismatch")
	}
	items := result.Items
	if !duplicateKey(err, mode) || !duplicateKey(items[1].Err, mode) {
		return checkFailure("batch duplicate key error code mismatch")
	}
	return expect(items[0].Status == oceanbase.BatchSucceeded && items[0].Err == nil && items[1].Status == oceanbase.BatchFailed && items[2].Status == last && items[2].Err == nil, "batch stop/continue status mismatch")
}

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

const typeNameVarchar = "VARCHAR"
