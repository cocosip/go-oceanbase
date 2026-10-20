# Standalone database acceptance tool

## Purpose and boundaries

`cmd/obtest` is a standalone CLI: it neither invokes go test nor imports test packages. Configure one or more OceanBase DSNs to connect, detect modes, create isolated objects, prepare data, run assertions, clean up in reverse order and produce a JSON report in one run. MySQL/Oracle are OceanBase compatibility modes; separate target DSNs express direct, OBProxy, TLS, compression and other connection combinations.

The tool uses public database/sql and oceanbase APIs. Its single main package is split by responsibility, with dependency direction cmd/obtest → oceanbase. It does not access driver internals, add dependencies or change public driver APIs. It uses no global logger: CLI progress goes to stdout and driver logging defaults to silent.

Each scenario creates its own pool and randomly named objects. It uses no existing application tables, creates no users/tenants and never cleans up another run's objects or XA branches. DDL may commit automatically, so cleanup does not rely on rollback. DROP is registered only after confirmed creation. Unknown creation outcomes retain object names for manual confirmation; application commands are never replayed. Failure, timeout and normal interruption all trigger independently timed cleanup of registered resources; cleanup errors fail the scenario.

All scenarios run by default. Mode-inapplicable cases are recorded separately. Missing second identities or deployment-specific fixtures cause skips, not passes. Insufficient privileges, unsupported features and mismatched data fail. Reports include selected scope, actual mode, elapsed time, assertions and cleanup state, without DSNs, passwords or raw server error text.

## Development steps and progress

- [x] Configuration/runner: strict JSON, DSN environment sources, selection, timeouts, interruption, reports, redaction and reverse cleanup; offline regressions verify failure propagation and cleanup after cancellation.
- [x] Database scenarios: CRUD, preparation/client binding, exact numbers/NULL/Unicode/time, transactions/savepoints, metadata, execution summaries, batches, long parameters, table BLOBs, procedure outputs, cursors, Oracle native LOBs/complex types/RETURNING/resource outputs, XA, sessions, warnings, cancellation and user changes.
- [x] Usage instructions, configuration example and links from development progress.
- [x] Before the baseline upgrade: Go 1.25/1.26 build, offline tool checks, unit tests, vet, lint and dependency guards; review found no remaining confirmed issues.
- [ ] Complete real-target acceptance across the required environments. The [MySQL-mode run and fixes on 2026-10-19](mysql-live-validation-2026-10-19.md) saved baseline/retest reports; the latest full tool run with a disposable second identity returned 35 passed, 0 failed, 2 skipped and 7 not applicable. Skips remain incomplete acceptance; other combinations are outstanding.

Oracle continuation: [Oracle-mode live validation](oracle-live-validation-2026-10-19.md) records **35 pass / 1 fail / 4 skip / 5 not applicable**. Sensitive-cursor freshness remains a failure. Splitting NCLOB from BLOB/CLOB adds one scenario; MySQL results from the earlier 44-case catalog retain their historical counts.

## Running the tool

Use a dedicated test database only. The account needs table creation, read/write and deletion privileges for owned objects. Full coverage also requires procedures, functions, Oracle types, dictionary access and XA privileges. The tool creates no accounts and grants no privileges. The database/schema must exist; createDatabaseIfMissing=true is forbidden.

Build at the repository root, then copy the executable to the test machine; the executable needs no Go toolchain:

```powershell
go build -o bin/obtest.exe ./cmd/obtest
$env:OB_DSN = 'oceanbase://test%40tenant:password@127.0.0.1:2881/test?mode=auto'
./bin/obtest.exe -config docs/obtest.example.json -report obtest-run-001.json
```

Alternatively, set OB_DSN and run `go run ./cmd/obtest -report obtest-run-001.json` without a configuration file. Linux/macOS builds can use `go build -o bin/obtest ./cmd/obtest`. This stage verified Windows builds/offline entry points only, not other platforms or database execution.

DSNs use the driver's existing URL format; encode username characters such as @. Mode comes from the authenticated physical connection: the tool distinguishes modes through the public catalog API, then pins mode constraints for that target's scenarios. An explicit mode=mysql/oracle adds a constraint and fails on mismatch. See [usage](go-driver-usage.md) and [option mapping](go-driver-options-mapping.md) for TLS, protocol, authentication and routing settings.

These commands neither connect nor create database objects:

```powershell
./bin/obtest.exe -list
./bin/obtest.exe -config docs/obtest.example.json -check
```

-list shows built-in scenarios without configuration/credentials. -check validates the file, primary DSN and Connector configuration, not credentials, privileges, networking, version or optional fixtures. Reports require a new writable path: existing files are never overwritten, and report creation is checked before the first connection.

## Configuration

The [minimal example](obtest.example.json) needs only OB_DSN. Configuration is limited to 1 MiB including whitespace; oversized/unreadable files fail before connecting rather than being parsed after truncation. A file must contain exactly one JSON object. Top-level fields:

| Field | Default | Meaning |
| --- | --- | --- |
| `targets` | Required in files | One or more targets, executed sequentially without concurrent target-object modification |
| `cases` | All 45 | Optional scenario-name array; unknown or duplicate names fail instead of silently reducing coverage |
| `timeout` | `2m` | Independent timeout per scenario and target probe; maximum 1h |
| `cleanup_timeout` | `30s` | Independent timeout per cleanup action; maximum 1h; outside the execution budget |
| `require_all` | `false` | Treat missing-fixture skips as failures; mode-inapplicable cases remain separate |

Each target supports:

| Field | Meaning |
| --- | --- |
| `name` | Unique 1–64-character ASCII letter/digit/underscore/hyphen label, starting with a letter/digit |
| `dsn_env` / `dsn` | Exactly one: DSN environment-variable name or inline DSN; prefer environment sources for credentials |
| `change_user_dsn_env` | Optional second-identity DSN environment name; only identity/default schema are read, not address/protocol. Must use the same endpoint, tenant and mode, and differ from the primary identity |
| `local_import_sql` | Optional deployment-specific LOCAL import SQL containing {table}, requesting filename input; both modes generate LOAD DATA LOCAL INFILE by default |
| `warning_sql` | Optional successful SQL producing nonzero warnings; MySQL defaults to a nonnumeric CAST; Oracle skips the positive warning case if absent |
| `cancel_sql` | Optional long-running SQL; by default, create a table and hold a row lock, then cancel another session's waiting UPDATE |
| `cancel_code` | Optional expected explicit-cancellation server code; defaults to MySQL 1317 / Oracle 1013, also recognizing the exact Oracle 600/70100/-5065 interruption envelope through the complete error chain; other mappings require an explicit override |

warning_sql, cancel_sql and local_import_sql are caller-supplied SQL sent to the server. The tool neither analyzes application-object references nor replays these commands. Use dedicated fixtures only.

To cover both modes and OBProxy in one run, configure each available endpoint separately:

```json
{
  "targets": [
    {"name": "mysql-direct", "dsn_env": "OB_MYSQL_DSN"},
    {"name": "oracle-direct", "dsn_env": "OB_ORACLE_DSN"},
    {"name": "mysql-proxy", "dsn_env": "OBPROXY_MYSQL_DSN"},
    {"name": "oracle-proxy", "dsn_env": "OBPROXY_ORACLE_DSN"}
  ],
  "timeout": "2m",
  "cleanup_timeout": "30s"
}
```

Every listed target is required; missing DSNs fail before any connection. Remove unavailable endpoints rather than treating them as covered. Additional targets can represent TLS, compression, OB20, transports or authentication, using corresponding DSNs. The tool does not provision domains, certificates or platform services.

## Scenarios and generated data

Each scenario uses an independent pool and random uppercase OBT_ object names to avoid Oracle unquoted-identifier case differences. Confirmed creations are registered; tables, procedures, functions and types are dropped in reverse dependency order. Timeout/Ctrl+C still attempts cleanup and reports failures. Forced termination, power loss and remote unavailability cannot guarantee cleanup.

| Scenario | Generated fixtures and assertions |
| --- | --- |
| `connect` | Authentication, initialization, Ping and nonempty OceanBase version |
| `crud` | Create table, insert/update/read/delete, affected counts, mode-specific duplicate-key code and continued use after error |
| `counts-matched`, `counts-changed` | Create integer tables with fixed MatchedRows/ChangedRows. Ordinary Exec, completed Execute summaries and single-item sequential batches check known counts for INSERT, unchanged/changed/unmatched UPDATE and DELETE/empty DELETE; MySQL also checks insert/unchanged/changed upsert branches. Verify no remaining rows and clean up. ChangedRows is MySQL-only; Oracle uses native matched counts. Reseed each entry point; do not compare different products' counts |
| `generated-keys` | MySQL unsigned BIGINT auto-increment table. Exec LastInsertId must equal the stored ID; Execute retains full uint64 decimal IDs. Raise this table's start value and check standard int64 overflow versus exact Execute IDs. UPDATE after every INSERT must not retain generated IDs; verify four rows and clean up. No assumed increment/first ID. Oracle returns are covered by returning |
| `prepare-server`, `prepare-client` | Server/client preparation, repeated reuse, Unicode/quotes/backslashes/NULL and repeated Oracle named parameters |
| `scalars` | Exact NUMBER/DECIMAL, Unicode, RAW/binary, leap day, microseconds and NULL through text/parameterized queries. Compare complete dates/times, accepting equivalent trailing zeros but rejecting extra nonzero nanoseconds, fractions beyond nine digits and trailing data. Explicit server prepare, ParseTime off, input wall time from configured location |
| `mysql-scalars` | Three boundary rows plus all-NULL row: positive/negative >24-hour and zero TIME(6), YEAR, full unsigned/signed BIGINT, integer TINYINT(1), BIT(1/9), empty/NUL VARBINARY and padded BINARY(4). Text/parameterized reads check exact values/NULL/counts, database type/ScanType and binary byte length; explicit server prepare and ParseTime=false |
| `oracle-scalars` | NVARCHAR2, TIMESTAMP(9) WITH TIME ZONE/LOCAL TIME ZONE and interval columns. Public-type binding/reads, nanoseconds/original TZ offset, equivalent LTZ instant, maximum negative intervals, ROWID rebinding/location and typed NULLs. Text/parameterized reads; server prepare, UTF-16 national charset, configured location retained |
| `transactions` | Create table, commit/rollback, savepoint rollback, read-only write rejection and final persisted count |
| `metadata` | Owned-table table/column/key dictionary content: catalog/schema/table identity, column names/order/types/length/precision/NULL and primary-key column; two-column ColumnTypes names/types; reject missing/extra rows |
| `metadata-parameters` | Owned table/one seed row; describe positional UPDATE, Oracle repeated-named UPDATE and parameterless SELECT. Check fixed 11 columns, physical/logical positions, normalized names/unknown direction, mode-specific nullability, dimensions and raw protocol bytes. Unknown types/sizes remain unknown; parameter descriptions need not equal application-column definitions. Reread unchanged seed after each description, verify one row, clean up. Metadata always prepares on the server even under client preparation |
| `execution` | Execute, result reads, early Close, completion summary and Ping on the original session |
| `batch` | Duplicate-key location for five preferred strategies, aggregate/per-input codes, completion counts, stop/continue status and rollback. Report observations retain actual strategy; fallback does not prove native capability |
| `batch-rewrite` | MySQL-only, explicitly enable multi-statements and preserve the driver's safety gate; require actual Rewrite for pass. Verify fallback content/counts before reporting native rewrite unavailable as skip |
| `batch-array` | Oracle-only, actual native array binding, aggregate/per-input duplicate codes, completion counts, per-item status and rollback. Requires OceanBase >=4.2.1; sequential fallback is not success |
| `optimized-execution` | Explicit prepare-execute/cache/type-reuse/checksum settings; parameter type changes, NULL, duplicate keys and reads. Actual server optimization still depends on negotiation |
| `reset-full` | Full Reset with standard sql.Stmt working across repeated pool borrows/returns |
| `multi-results` | MySQL-only, explicitly enabled multi-statements; two result-set orders/values/boundaries and original-session reuse |
| `trace` | Queries with application/no trace and client request diagnostics; no claim of server-side trace visibility |
| `long-data` | Generate about 150 KB binary/Unicode text, stream uploads, compare content, roll back and commit |
| `table-blob` | Owned-table BLOB writes/reads/truncation in the original transaction and content after rollback |
| `routines`, `routines-client` | Create procedures/functions using server/client prepare, INOUT/OUT and function results. Deliberate failures must be MySQL 1644 / Oracle 20001 and publish no outputs; every joined-error branch must match without hiding other failures |
| `cursor`, `cursor-update` | Two seed rows, forward/static positioning; update/refresh/insert/delete, post-rollback values and transaction-end invalidation. Single-row batches verify all content, no extra rows and later read errors |
| `cursor-sensitive` | Oracle-only, reposition/read after another session commits an update; requires sensitive-cursor support |
| `returning` | Oracle-only, INSERT RETURNING INTO numeric/text outputs and count |
| `native-lob` | Oracle-only BLOB/CLOB fixtures, streaming read/write, Unicode offsets, truncation and transaction lifetime |
| `native-nclob` | Independent NCLOB Unicode locator reads with UTF-16; exact target 902 rejection is skipped without hiding BLOB/CLOB results |
| `complex` | Oracle-only OBJECT column storage with a server-prepared query, TABLE OF binding through PL/SQL IN/OUT, exact numbers/RAW, distinct ordered first/last values, server NULL-object representation and complete OBJECT OUT identity; rollback. Nested-table column storage is a separate integration capability |
| `resource-outputs` | Oracle-only procedure/temporary LOBs, REF CURSOR/BLOB/CLOB content and closure; single-row REF CURSOR batches reject extra rows/later read errors |
| `xa-one-phase`, `xa-two-phase`, `xa-rollback` | Owned table/unique XID, branch state, prepare/recover, commit/rollback and persisted visibility |
| `xa-suspend`, `xa-join`, `xa-recovery` | Suspend/resume, end/join and recovery after original-connection closure; touch only this run's XID, never another coordinator's branches |
| `session` | schema/catalog, variables, client info and pool isolation after session changes |
| `warnings` | Nonzero protocol warnings, details or explicit mode rejection, and snapshot clearing |
| `cancel-context` | Owned table/transaction lock, another connection's lock wait until context timeout; unchanged original value and usable replacement connection |
| `cancel-query` | Separate helper cancels a lock-waiting command; explicit code across the complete error chain, reusable original session, one-shot token and no persisted side effects. Expected cancellation mixed with other failures must fail |
| `change-user` | Configured second identity, identity switch, old-statement invalidation and standard-pool restoration of original identity |
| `local-import` | Both modes: refuse import before SQL/upload inside a transaction, verify earlier DML still rolls back, then import in autocommit and compare every value/count. No local data file |

Creation, assertion, privilege and protocol errors fail explicitly; ErrUnsupported never becomes success automatically. Exact MySQL 1398 rejection of XA SUSPEND/JOIN and Oracle 902 rejection of NCLOB are reported as unavailable (skip); other or mixed errors still fail, and cleanup failure overrides skip. Strict require_all also fails skips. The tool does not cover every scalar, metadata filter, platform authentication, HA fault or stress combination. This table is the actual scope; see the [plan](go-driver-development-plan.md) and [matrix](go-driver-compatibility-matrix.md) for other acceptance requirements.

## Reports and exit codes

Stdout prints target, scenario, status and elapsed time. JSON records run ID, toolchain/OS, selected scope, actual mode, each result, actual batch strategy and cleanup. Object states include dropped, cleanup_failed and create_unconfirmed. XA retains this run's XID and branch-handling result. Unknown outcomes/recovery failures retain identifiers for manual checks; application SQL is not retried automatically.

| Code | Meaning |
| --- | --- |
| `0` | All applicable selected scenarios passed, with no missing-configuration skips; not all-platform/version acceptance |
| `1` | Execution/assertion/cleanup failure, or a skip under require_all |
| `2` | CLI, configuration or report-file error |
| `3` | No failure, but missing-fixture skips or no applicable scenario passed |

Oracle-only cases on MySQL, and vice versa, are not_applicable, distinct from missing-configuration skip. Unreachable targets fail, with remaining scenarios explicitly not executed; other configured targets continue. Cleanup after successful interruption does not prove all server-side SQL stopped. Check unknown XA outcomes using this run's reported XID.

## Verification at this development stage

Offline tool regressions cover strict configuration, complete CRUD orchestration, rejection of incorrect data, owned-object cleanup after partial creation, independent cleanup after cancellation, cleanup-error propagation, original-session XA cleanup and cancellation error-code matching. In-process fixtures verify the runner, not real database acceptance. The executable depends on neither these fixtures nor Go testing.


[TestScalarsTemporalAssertions](../cmd/obtest/scalars_test.go) covers 32 offline workflows across both compatibility modes and text/parameterized queries: exact values, equivalent trailing fractional zeros, changed dates/times, extra nanoseconds, fractions beyond nine digits and invalid suffixes. A regression rejects excessive fractional precision hidden by shortening the hour to one digit. Each workflow checks scenario status and cleanup of its own table; Oracle DATE must retain the expected midnight, rather than merely match a date prefix. Local fixtures do not prove real server date or precision behavior.

[TestComplexOutputAssertions/TestComplexCollectionAssertions](../cmd/obtest/complex_test.go) covers 20 offline workflows for OBJECT OUT numbers, RAW, schema and attribute order, and collection first/last values, wrong order, duplicates, missing entries, NULL and object/collection schemas. Each workflow checks transaction rollback and reverse cleanup of the table, collection type and object type; equivalent decimal trailing zeros are accepted. Fixtures replace database dependencies and do not prove real type descriptors or server round trips.

[TestMetadataContentWorkflow/TestMetadataSchemaFilters](../cmd/obtest/metadata_test.go) provides 90 offline workflows. Both modes validate complete table/column/primary-key dictionary identities for an owned ID/V table, column order, INTEGER precision/scale, VARCHAR length and NULL, and two-column result metadata. Incorrect values, missing/extra rows, unknown required properties and read/close errors fail. Checks also cover LIKE escaping and exact case matching for nonempty schemas; every result is closed and the complete scenario drops its table after failure. The old count-only implementation incorrectly accepted 50 invalid inputs. Fixtures do not establish acceptance of real dictionaries or permissions.

[TestCursorBatchAssertions](../cmd/obtest/cursor_test.go) provides 22 offline workflows validating single-row batches from ordinary two-column cursors and single-column REF CURSORs: exact values, missing/duplicate/extra rows, read errors after the first row, close errors, invalid/NULL identifiers, and incorrect column width/content. All verify result closure and table cleanup after scenario failure. The old implementation incorrectly accepted 6 invalid batches, which are now rejected. Shared assertions cover forward/static, refresh, sensitive cursors and routine resource outputs; this is not acceptance of real server fetch behavior.

[TestRoutineFailureAssertions](../cmd/obtest/routines_test.go) provides 68 offline workflows across both modes and server/client scenario selections. Each creates three routines, performs two INOUT/OUT rounds, calls a function and triggers a deliberate exception, checking parameter directions/initial values and reverse cleanup. Expected errors, wrapped errors and joined errors whose branches all match are accepted; joined chains containing other errors, including an outer wrapper, must fail. Syntax, privilege, missing-routine, constraint, wrong-mode and zero-code errors, unexpected success, transport failure, timeout and publishing outputs on failure are rejected. The old implementation incorrectly accepted 24 unrelated server errors. Equivalent integration assertions were tightened and only compiled in this run.

[TestRowsAffectedWorkflows](../cmd/obtest/counts_test.go) provides 51 offline workflows using standard-library in-memory connections and independent byte-level protocol fixtures through the actual public driver. They check CLIENT_FOUND_ROWS, three entry points, unexpected/zero counts, counts exceeding int64, server errors and owned-table cleanup after failure. When Execute count overflow evicts the original session, fixtures also check that initialization of a new connection is followed by cleanup of registered tables only. SQL has no parameters and batches contain one Sequential item to isolate the count contract; this does not establish acceptance of multi-item batches, parameter binding or server SQL semantics.

[TestParameterMetadataWorkflow](../cmd/obtest/parameter_metadata_test.go) provides 53 offline workflows checking raw metadata Query/Kind, complete column and parameter identities, reported or unknown descriptions, raw direction bytes, repeated Oracle names, and failure propagation for misplaced/missing/extra rows, invalid nullable/size/protocol ranges, read/close errors and modified seeds. Open results are closed and owned tables are cleaned up after failure. The fixture substitutes public Metadata results to verify tool orchestration/assertions. Existing [driver protocol tests](../metadata_test.go) cover server prepare, temporary statement closure and server descriptions under PrepareClient; offline evidence is not acceptance of real parameter inference or SQL behavior.

[TestDuplicateKeyWorkflows / TestBatchDuplicateAssertions](../cmd/obtest/duplicate_test.go) provides 56 complete tool workflows and 80 batch-result assertion cases respectively. CRUD/optimized execution checks correct, wrapped or matching joined duplicate-key errors, wrong-mode/syntax/privilege/missing-table/data-too-long/zero-code errors, unexpected success, transport/cancellation errors and owned-table cleanup. Batch checks validate aggregate and per-input error codes, positions, completion counts and stop/continue status; an aggregate error cannot hide a different per-input error. All joined branches are traversed, so duplicate-key errors accompanied by cleanup/transport or other errors fail. Regressions first reproduced the false positives, then passed after correction. Workflows use a substitute SQL driver and batch tests supply results directly; they do not establish acceptance of optimized/native array-binding protocols or real SQL. Corresponding [integration execution-strategy](../tests/integration/execute_strategies_test.go) and [integration batch](../tests/integration/batch_test.go) assertions were strengthened. [Integration helper classification regressions](../tests/integration/env_test.go) run without a database; other database workflows were only compiled.

Duplicate-key errors for owned primary keys follow the pinned OceanBase [OB_ERR_PRIMARY_KEY_DUPLICATE definition](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.def#L695): MySQL mode uses [ER_DUP_ENTRY 1062](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/mysql_errno.h#L79), and Oracle mode uses 1. Neither message text nor SQLSTATE inference is used. Native array-binding diagnostics may omit SQLSTATE, so only the actual error code and input association are required.

Routines, cancellation and duplicate-key checks share strict error-code classification: unwrap each layer and traverse every joined branch; accept only non-nil expected server errors at every leaf. An expected error in one branch must not hide cleanup, timeout or other server errors. `TestCancellationRejectsUnrelatedErrors` covers both default modes and custom codes. The 84 offline cases in `TestExpectedServerErrors` cover 6 duplicate-key/routine/cancellation codes, wrapping/nested joins, typed nil and mixed failures. Database workflows were only compiled.

Codes were checked against pinned OceanBase sources: [unhandled SIGNAL](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.def#L1183) maps to [ER_SIGNAL_EXCEPTION 1644](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/mysql_errno.h#L661). [Oracle error-code conversion](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.cpp#L27832) retains application exception numbers and converts negative values to positive protocol codes, so an owned `RAISE_APPLICATION_ERROR(-20001,...)` should yield 20001. Source and offline fixtures do not replace integration evidence for actual versions/proxies.

[TestMySQLScalarsWorkflow](../cmd/obtest/mysql_scalars_test.go) provides 44 offline workflows: complete boundary values through both read paths, two legal representations of maximum-whole-second/zero TIME and rejection of nonzero out-of-range fractions, integer overflow/precision, TINYINT remaining non-bool, TIME suffixes, BIT width, YEAR zero, empty binary remaining non-NULL, fixed-width zero padding, missing/extra rows, NULL and incorrect metadata. Checks cover original input types, query arguments, Rows closure and owned-table cleanup; fixture metadata and rows do not prove real server behavior.

TIME seeds use maximum whole seconds `838:59:59` and `-838:59:58.999999` to cover range and microseconds respectively. Pinned OceanBase [TIME_MAX_VAL](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/deps/oblib/src/lib/timezone/ob_time_convert.h#L159) and [overflow handling](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/deps/oblib/src/lib/timezone/ob_time_convert.cpp#L5451) show that nonzero fractions after the maximum whole second cause errors or truncation and cannot be normal seeds. Whole-second output may include six zero microsecond digits; nonzero/extra suffixes remain rejected. Original integration scalar seeds were corrected too; those tests were only compiled in this run.

[TestOracleScalarsWorkflow](../cmd/obtest/oracle_scalars_test.go) provides 32 offline workflows: exact values through both read paths, equivalent LTZ offsets, incorrect national text, TZ offset/precision, LTZ instants, intervals, ROWID binding/location and five typed NULL forms. Checks cover scenario status, owned-table cleanup and public input types; fixtures do not establish real server support for these types.

[Complex-type integration tests](../tests/integration/complex_test.go) also use distinct collection seeds and complete OUT-content assertions. DATE accepts only the expected whole-second value or the driver's possible six-zero-microsecond representation; 7 offline positive/negative cases verify this assertion. Database workflows were only compiled, not executed, in this run.

The generated-key scenario executes `ALTER TABLE ... AUTO_INCREMENT=9223372036854775808` on its own table, requiring corresponding DDL privileges and target-version support for the unsigned BIGINT auto-increment range. It does not change global or tenant auto-increment settings. Standard `LastInsertId` returns int64, so out-of-range values are checked for overflow errors and stored range only; the Execute summary supplies exact full-ID comparison. 31 public-driver/in-memory-protocol workflows check assertions, boundaries and cleanup after failure; they do not prove real version-specific allocation behavior.

The current minimum Go version is 1.26. Before the upgrade, full-package builds, unit tests, vet and integration compilation passed on Windows amd64 with Go 1.25.0/1.26.0. See [development progress](go-driver-development-progress.md) for successive evidence covering repeated tool regressions, repository-wide golangci-lint with ordinary/integration tags, Windows/386, `-list`, configuration `-check` and documentation checks. The tool provides 45 scenarios; CI only adds the database-free `-list` check.

No database was connected in this run. All 44 live scenario results remain pending until the user supplies test DSNs; compilation, offline checks and unit tests must not increase the database acceptance count.
