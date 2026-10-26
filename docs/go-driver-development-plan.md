# OceanBase Go Driver Development Plan

> Execution notes: implement tasks in dependency order using the executing-plans workflow. First verify a failing case for each task, then implement, verify, and update progress. This document does not require branch creation, automatic commits, or a multi-agent workflow.

Goal: fully adapt the observable features of obconnector-j into an idiomatic OceanBase Go driver, retaining implementation and verification evidence for every feature.

Architecture: use `database/sql` and its standard pool as the entry point. One public `oceanbase` package owns configuration, standard interfaces, and necessary extensions; share the protocol and session core, handling differences according to the actual Oracle/MySQL compatibility mode.

Technical baseline: Go 1.26; standard-library `database/sql`, `net`, `crypto/tls`, and `log/slog`; prefer pure Go. Evaluate necessary character-set and platform-authentication dependencies in their owning tasks.

Specification: [Architecture design](go-driver-architecture-design.md); feature basis: [Java architecture and feature outline](obconnector-j-architecture-outline.md); current status: [Development progress](go-driver-development-progress.md).

## 1. Usage and Current Baseline

When this plan was written, the repository contained only design documents, ignore rules, and lint configuration, with no `go.mod`, Go source, or tests. The tasks below define implementation commitments; the progress table records actual code and execution results. A checked box means only that the step has sufficient evidence, not that its task or feature has passed acceptance. Record local tests, target-platform execution, and real-server verification separately. Historical RED→GREEN evidence comes from contemporaneous progress records; a successful rerun today cannot manufacture earlier failure evidence. The user requested no database integration runs during this phase: leave related acceptance items unverified and implement/run local and protocol tests first.

This plan uses the module path configured for the repository, `github.com/cocosip/go-oceanbase`, and public package name `oceanbase`. If repository ownership changes, update the module and example imports together in the first code task.

Development order expresses technical dependencies; later capabilities remain within the delivery scope. Accept each task independently for its corresponding modes and servers. Go interfaces, simulated protocols, and real servers provide distinct evidence and cannot substitute for one another.

Manage implementation dependencies separately from acceptance-evidence dependencies: unexecuted D28 Linux/macOS native authentication, real Linux VC, or race checks do not prevent implementing and testing D30/D31 on the available platform. No additional Linux/CI environment is currently available; continue independent local development and verification in this phase. Target-platform and real-service acceptance remain incomplete, and those combinations must not be declared supported. A development blocker must identify the specific missing implementation or input required by the current feature, rather than propagating platform-environment gaps indiscriminately through the dependency graph.

Before starting a task, read the relevant architecture sections, then the task and its direct dependencies' interfaces. If evidence requires changing an architecture contract, update both the architecture and this plan; do not introduce undocumented bypasses in code.

## 2. Global Constraints

- OceanBase is the database; Oracle and MySQL are its compatibility modes. The authentication response determines the mode; configuration supplies only an expected-mode constraint.
- Use `go 1.26.0` in `go.mod` and verify Go 1.26; do not use APIs introduced in Go 1.27 or private standard-library symbols.
- Preserve ordinary `sql.Open` / `sql.OpenDB` and `sql.DB/Conn/Tx/Stmt/Rows` usage; add no custom pool, JDBC class hierarchy, or ORM.
- Inject `Logger *slog.Logger` externally; nil is silent. Default/global loggers and fallback output are prohibited.
- Connector retains a configuration snapshot. Do not replace a socket or actual mode in an established session, or transparently replay a business operation that may already have been sent.
- Scalars follow `driver.Value`; exact numbers never pass through float64. Connection-bound resources must not escape through arbitrary struct scanning or `Raw`.
- Extensions enter the same Exec/Query core through package-private request values; argument validation must neither change session state nor send business data.
- Resource functions accept only non-nil `*sql.Conn` / `*sql.Tx`; XA accepts only `*sql.Conn`. Every handle checks connection, session, and transaction generations.
- Baseline limits: 64 MiB logical messages, 16 MiB automatic LOB materialization, 128-row fetches, 1-second cleanup, and 1024 execution-summary items. Provide configuration and error semantics as specified by the architecture.
- Apply shared checks before sending any sensitive authentication stage. Plaintext passwords require TLS completed and verified by the driver; authentication downgrade is prohibited.
- Documents use only repository-relative paths or upstream URLs pinned to a version; exclude developer-machine paths, credentials, and test-service addresses.
- Preserve other workspace changes; do not create/switch branches without authorization. Commits, pushes, and releases require explicit user authorization.

## 3. File Responsibilities and Interface Contracts

### 3.1 File Ownership

| File/directory | Responsibility | Main tasks |
| --- | --- | --- |
| `go.mod`, `doc.go`, `driver.go`, `config.go`, `dsn.go` | Module, registration, configuration, and strict DSN parsing | D01 |
| `connector.go`, `conn.go`, `session.go`, `mode.go`, `capabilities.go` | Connection establishment, actual mode, session state, resource generations | D03–D06, D09, D15 |
| `internal/wire/` | Packet streams, basic handshake messages, commands, compression, and OB20; independent of database/sql | D02, D03, D23 |
| `internal/auth/` | Independent authentication algorithms and stage state; root package orchestrates handshake and security gates | D03, D28 |
| `transport*.go`, `shared_memory*.go`, `obproxy*.go`, `tools/obproxy-host/main.c` | Root-package platform dialing and standalone native OBProxy host; no additional public/internal packages | D28 |
| `execute.go`, `prepare_execute.go`, `rows.go`, `result.go`, `stmt.go` | Text/binary execution, standard interfaces, mixed-result summaries, required combined protocol, and general strategies | D05, D08, D11, D14, D24 |
| `bind.go`, `bind_oracle.go`, `sql_lexer.go`, `client_prepare.go` | Bind plans, lexing, client preparation, explicit JDBC escapes | D07, D10 |
| `codec*.go`, `types.go`, `internal/charset/` | Basic/extended types in both modes, encoding, and time | D07, D08, D12, D18 |
| `tx.go`, `savepoint.go`, `xa.go` | Local transactions, savepoints, XA state and recovery | D09, D31 |
| `call.go`, `batch.go`, `long_data.go` | OUT/RETURNING, batches, and single-use long parameters | D14, D20–D22 |
| `resource.go`, `lob.go`, `lob_mysql.go`, `cursor.go` | Pinned-session resources, native/emulated LOBs, scrollable and updatable cursors | D15–D20 |
| `metadata.go`, `metadata_sql_mysql.go`, `metadata_sql_oracle.go` | Structural/parameter metadata and shared type dictionary | D13, D18 |
| `internal/routing/`, `trace.go`, `logging.go`, `local_infile.go` | Address selection, tracing, injected logging, and controlled import | D01, D25–D30 |
| `internal/testutil/`, per-package `testdata/`, `tests/integration/` | Fault peers, byte fixtures, and real-server verification | Maintained from D02 onward |
| `architecture_test.go` | External tests of the root package, checking production and test dependency edges | Maintained from D02 onward |
| `examples/`, `.github/workflows/ci.yml` | Compilable examples and toolchain/platform CI | Establish CI in D01; extend with each capability; consolidate in D32 |

Create files only when an implementation needs them; do not generate empty scaffolding first. Do not move the entire driver into `internal` and forward it through another public wrapper layer.

Production dependencies are limited to `oceanbase → internal/{wire,auth,routing,charset}`. The four internal packages import neither the root package nor each other. The root owns Config/Mode, public plugin interfaces, and session execution; wire/auth/routing/charset define their own narrow inputs/results, converted by the root. Public authentication interfaces must not expose internal types. Auth does not directly send/receive packets or sockets: the root orchestrates handshakes, controlled sending, and shared security gates. Do not add common/core/interfaces packages to bridge dependencies.

`internal/testutil` depends only on the standard library and simulates peers using net.Conn/bytes/context. It imports neither the root nor driver-internal packages; production code does not import testutil. D02's `architecture_test.go` uses an external test package and parses `go list -deps -json -test -tags=integration ./...` to check production/test dependencies; compilation and `go list -deps ./...` also reject actual cycles directly. Update architecture §4, this table, and guard assertions whenever adding a package/dependency edge.

The call direction between internal commands and public APIs is also fixed: public functions such as Metadata create package-private requests through standard Session calls. Dictionary resolution/LOB materialization directly use the current physical session's command executor within the driver operation; they must not recursively call Metadata or sql.DB/Conn/Tx/Session. Complete-response boundaries solve packet ordering, but do not release the connection lock held by database/sql. Internal dispatch owns one session operation and reacquires neither that lock nor its own operation lock. D13/D16/D18 share dictionary SQL, descriptions, and caches, not public call stacks; auxiliary connections must not replace the original transaction's visibility.

### 3.2 Interfaces Across Tasks

Implementation must respect the following boundaries. Reuse data types already defined in the architecture rather than duplicating field definitions here.

```go
// D01 / D04: NewConnector only validates/snapshots; Connect performs network operations.
func ParseDSN(dsn string) (Config, error)
func NewConnector(cfg Config) (driver.Connector, error)

// D02: wire sends/receives logical messages, handling fragmentation, sequences, and limits internally.
// These methods belong to internal/wire; PacketIO is a concrete type.
func NewPacketIO(conn net.Conn, maxMessageBytes int64) *PacketIO
func (p *PacketIO) ReadMessage(ctx context.Context) ([]byte, error)
func (p *PacketIO) WriteMessage(ctx context.Context, payload []byte) error
func (p *PacketIO) ResetSequence()

// D07: package-private; plan slots record name, position, and IN/OUT identity.
func buildBindPlan(query string, mode Mode) (bindPlan, error)
func bindArguments(plan bindPlan, args []driver.NamedValue) ([]boundValue, error)

// D11: use the concrete types in architecture §11.4; do not create another Call API.
func Execute(ctx context.Context, session Session, query string, args ...any) (*Execution, error)
func (e *Execution) Rows() *sql.Rows
func (e *Execution) Snapshot() ExecutionSnapshot
func (e *Execution) Close() error

// D09 / D15–D22: pinned-session extensions.
func Savepoint(ctx context.Context, tx *sql.Tx, name string) error
func RollbackTo(ctx context.Context, tx *sql.Tx, name string) error
func ReleaseSavepoint(ctx context.Context, tx *sql.Tx, name string) error
func OpenBlob(ctx context.Context, session Session, query string, args ...any) (*Blob, error)
func OpenClob(ctx context.Context, session Session, query string, args ...any) (*Clob, error)
func OpenTableBlob(ctx context.Context, session Session, target BlobTarget) (*Blob, error)
func OpenCursor(ctx context.Context, session Session, query string, opts CursorOptions, args ...any) (*Cursor, error)
func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error)
func (c *Cursor) Close() error
func ExecBatch(ctx context.Context, exec Execer, query string, rows [][]any, opts BatchOptions) (BatchResult, error)
```

The owning tasks determine `wire`'s []byte lifetime and bindPlan's concrete fields within the package; neither becomes a public extension framework. D02 reads complete logical messages; D05 must not assume it can obtain underlying buffer slices spanning messages.

For extension-value fields/interfaces not yet detailed, the owning task first fixes the contract with compilable external-call tests, then implements it. Downstream tasks use those actual declarations rather than inventing equivalent types. Record fields, defaults, and error behavior in the corresponding API documentation.

Explicit features/configuration not yet implemented must return a clear capability error before use, never be silently accepted and ignored. Remove the corresponding temporary unavailable path when completing each task and add tests demonstrating actual behavior.

## 4. Milestones and Key Dependencies

| Milestone | Tasks | Exit criteria |
| --- | --- | --- |
| G1 Connections and standard queries | D01–D05 | Real connections, Ping, text queries, and errors in both modes; tests for sensitive-authentication gates and logger isolation |
| G2 Execution and session correctness | D06–D12 | Arguments, both preparation strategies, transactions/reset, multiple results, precision/time pass; no replay of sent operations |
| G3 Complete extended data capabilities | D13–D22 | Metadata, OUT, LOBs, complex types, cursors, and batches accepted for each supported combination |
| G4 Protocol and operational capabilities | D23–D30 | Acceptance for OB20/compression, optimizations, FLT, HA/discovery, platform authentication, import, and session helpers |
| G5 Distributed transactions | D31 | XA branches, recovery, and outcome uncertainty verified in both modes |
| G6 Complete delivery | D32 | Evidence for all modules, configuration migration, real environments, toolchains/platforms, performance, and examples |

Numbers give the recommended default order, not mandatory serial execution. Specific dependencies appear under each task heading; for example, D26 static address selection can follow G2 without waiting for every LOB capability. Implement basic logging, cancellation, and security with their owning capabilities rather than postponing them until G4.

## 5. Verification Methods and Environment Contracts

### 5.1 Per-Task Development Cycle

The four steps mean the same thing for every task: add the listed tests/assertions; run the command to confirm the feature is absent or an assertion fails; implement the delivery contract; rerun until passing and update progress. Failures caused by toolchains, permissions, or unavailable servers are environment problems, not evidence of the required functional failure.

Before finishing a task, run relevant package tests and `go test ./...`; broaden checks only for newly affected paths or newly discovered issues. Nontrivial parsers need seed corpora, boundary unit tests, and fuzzing; documentation changes need only structure, link, and content checks.

The required verification set for each Dxx appears in [Development progress §5.3](go-driver-development-progress.md#53-mandatory-validation-sets-by-task). Expand it into capability, mode, version, path, and platform entries when starting a task, then check each entry before acceptance. A missing required environment remains pending integration; do not mark it inapplicable or substitute local success. Test names within tasks specify minimum scenarios and do not replace that set.

### 5.2 Real-Server Tests

D04 established integration-tag database tests. They now reside beside concrete owners, with connect/control/batch integration subpackages and root public smoke/query/pool scenarios. Shared configuration helpers live in `internal/integrationtest`. Test output must not contain DSNs; the external configuration remains:

| Environment variable | Meaning |
| --- | --- |
| `OB_MYSQL_DSN` / `OB_ORACLE_DSN` | Direct test endpoints for both compatibility modes |
| `OBPROXY_MYSQL_DSN` / `OBPROXY_ORACLE_DSN` | Corresponding OBProxy test endpoints |
| `OB_TEST_PROFILE` | `mysql`, `oracle`, `proxy-mysql`, `proxy-oracle`, or `all`; selects actual combinations to verify |
| `OB_TEST_REQUIRE` | When `1`, missing credentials, services, or required test objects for selected combinations must fail; skipping must not produce a green acceptance result |

`go test ./...` does not connect to external databases unless integration is explicitly enabled. Ordinary local exploration may skip missing environments; milestone/release acceptance must set OB_TEST_REQUIRE=1. Tests use a dedicated schema/object prefix and clean up their own objects; destructive DDL/XA tests must not use production objects.

Configure those variables before running integration tests, then execute:

```text
go test -tags=integration . -run TestSmoke -count=1
go test -tags=integration ./... -count=1
```

For every acceptance run, record actual Go/OS/architecture, OceanBase/OBProxy versions, mode, execution/packet-stream paths, commands, and results. If Oracle compatibility mode is unavailable, record the gap; MySQL or ordinary Oracle results cannot substitute.

### 5.3 Toolchain and Fault Verification

Routine CI was simplified on 2026-10-26; [README](../README.md#github-actions) describes the current jobs. References below to dedicated fuzz, native ABI, 386, stability or live integration jobs describe the earlier development setup. Their acceptance requirements and local test entry points remain; removing their CI jobs does not establish a passing result.

- D01 creates `.github/workflows/ci.yml`, installs Go `1.26.x`, and runs `go version`, `go list -deps ./...`, `go test ./...`, and `go vet ./...` on Linux/Windows amd64. Changing only the GOTOOLCHAIN string does not prove another version was tested.
- Verify basic capabilities on Linux/Windows amd64; run `CGO_ENABLED=0 go test ./...` on supported systems. On 386, focus on protocol lengths, indexes, and integer conversion. Record only compile evidence for targets that cannot run. The toolchain requires cgo for `-race`; this driver is pure Go and does not import `C`, so CI omits race and continuously verifies zero-cgo builds using `CGO_ENABLED=0`. Run race detection manually in a separate cgo environment, outside routine checks.
- Use the existing `.golangci.yml` with a toolchain-compatible golangci-lint v2; pin its CI version and run `golangci-lint run`.
- D02 adds architecture dependency guards, protocol fuzzing, and 386 boundaries. D04 adds §5.2 integration jobs, with OB_TEST_REQUIRE=1 for acceptance. D06 adds `CGO_ENABLED=0` concurrent-resource checks, without race (see above). Ordinary PRs lacking external credentials may run only local jobs, but these do not count as integration acceptance. Later tasks extend jobs/scenarios with their capabilities; D32 consolidates results and closes gaps.
- D02 fault peers support interruption at a selected read/write, short writes, truncation, delayed responses, and call counts. Prove no duplicate execution using the actual number of business commands received.
- Interface assertions and simulated packets cannot prove real SQL, dictionary, transaction, locking, LOB, or XA semantics; integration tests are required.

### 5.4 Key Failure Conditions and Test Ownership

| Input or failure condition | Expected user-visible behavior | Tests that must fix this behavior |
| --- | --- | --- |
| Disconnect after a query is partly sent | Preserve the unknown-outcome error; receive the business command only once | D06 TestNoReplayAfterSend; D11 TestExecutionPartialError |
| Close before all ordinary procedure results are read | Bounded finalization; publish output handles only on success; no leaks on failure | D11 TestExecutionClose; D20 TestCursorOutPublish |
| No-op UPDATE or generated ID containing a large integer | Do not report a missing row incorrectly or conflate zero/unknown/overflow | D11 TestExecutionOrder; D17 TestTableBlobTx; D19 TestCursorUpdateIdentity |
| Authentication switches to a password-sending algorithm | Verify TLS before the sensitive-stage write; switching does not relax requirements | D03 TestAuthSwitch; D28 TestAuthPluginLifecycle |
| Reuse an old statement/LOB/Cursor after reset/transaction end | Rebuild statements by generation; explicitly invalidate old resources | D09 TestResetGeneration; D15 TestResourceGeneration; D20 TestOutputLease |

## 6. Development Tasks

The following checkboxes are acceptance steps within tasks; **maintain global status only in the progress table**. An unchecked box means this plan has no execution evidence for that step, not that another task should be created.

### D01 Module, Public Configuration, and Logger Injection

Dependencies: none. Coverage: M01, M02, M15, M16; architecture §5–§7, §18.

Files: add `go.mod`, `doc.go`, `driver.go`, `config.go`, `dsn.go`, `connector.go`, `logging.go`, `config_test.go`, `dsn_test.go`, `logging_test.go`, `.github/workflows/ci.yml`, and `docs/go-driver-options-mapping.md`.

Deliver: register `oceanbase`; implement ParseDSN/NewConnector, Mode, configuration enums and specified defaults, and configuration snapshots. Common Config fields follow the architecture; DialContext uses `func(context.Context, string, string) (net.Conn, error)`. This task does not claim connection support: Connect is completed in D04; earlier calls return an explicit unavailable error rather than a fabricated session.

- [x] Test `TestConfigDefaults`, `TestDSNStrict`, `TestConnectorSnapshot`, `TestLoggerIsolation`: architecture defaults; reject unknown/duplicate sensitive parameters; later map/TLS changes cannot alter snapshots; nil logger is silent; two Connectors' output remains isolated; errors contain neither passwords nor full DSNs.
- [x] Run `go test . -run 'Test(Config|DSN|ConnectorSnapshot|Logger)' -count=1` and confirm new assertions fail.
- [x] Implement configuration/registration; NewConnector rejects InsecureSkipVerify; never call a user log handler under the internal network lock. Establish an option inventory pinned to a Java version, recording each owning Dxx, Go equivalent or Java-only purpose, and migration status; unimplemented fields must not be labeled supported.
- [ ] Pass the same command; establish §5.3 test/vet/list/lint CI for both Go versions and Linux/Windows, recording actual toolchains and execution links. D01 acceptance must not be described as “the driver is usable.”

### D02 Standard Packet Streams and Fault Peers

Dependencies: D01. Coverage: M03, M04; architecture §9, §12.2.

Files: add `internal/wire/packet.go`, `packet_test.go`, `packet_fuzz_test.go`, `testdata/`, `internal/testutil/peer.go`, `peer_test.go`, `architecture_test.go`; extend CI.

Deliver: §3.2 PacketIO with protocol fragmentation/reassembly, sequence reset, and maximum logical-message limits; controllable peer reads/writes and command counts, without requiring real servers for unit tests.

- [x] Test `TestPacketRoundTrip`, `TestPacketLimit`, `TestPacketShortIO`: empty messages, exact fragment boundaries/terminators, sequence wraparound, invalid sequences, oversized/truncated input rejected before allocation; short writes lose no data.
- [x] Run `go test ./internal/wire ./internal/testutil -count=1` and confirm relevant assertions fail.
- [x] Implement packet streams and fault injection; identify source/protocol version for byte fixtures and retain no authentication secrets. Implement the §3.1 dependency guard, prove rejection with one reverse-import fixture, and integrate it into existing CI.
- [x] Pass the same command; run `go test ./internal/wire -run '^$' -fuzz FuzzPacket -fuzztime 30s` without panic/uncontrolled allocation; execute 386 boundary cases.

### D03 Handshake, Basic Authentication, TLS, and Mode Detection

Dependencies: D02. Coverage: M03, M04; architecture §8–§9, §17.2.

Files: add `internal/wire/handshake.go`, `internal/auth/auth.go`, `native.go`, `sha2.go`, `clear.go`, and corresponding tests; add `authentication.go`, `errors.go`, and update configuration snapshots; session-mode pinning is completed in D04.

Deliver: handshake/authentication-response parsing, native/SHA/clear basic paths and switching; shared state-machine credential gates; separate actual mode/version/capabilities. ModeAuto locks only after the first successfully initialized session; failed attempts do not lock the Connector.

- [x] Test `TestAuthSecurityGate`, `TestAuthSwitch`, `TestTLSRequired`, `TestActualMode`: zero plaintext credential bytes sent during clear/full-auth without TLS; no downgrade after certificate/hostname failure; custom Dial cannot forge verified security state; close mode-mismatched connections.
- [x] Run `go test ./internal/auth ./internal/wire . -run 'Test(Auth|TLS|ActualMode)' -count=1` and confirm failure.
- [x] Implement handshake, non-TLS encryption with a pinned public key, and authentication-stage checks. The root adapts public authentication factories to auth algorithm inputs/results; factories create per-session state, expose neither internal types nor sockets, and introduce no auth→root/wire dependency.
- [x] `TestHandshakeIdentityEncoding` and `TestHandshakeIdentityEncodingRejected` verify strict Session.Charset encoding for initial handshake identities/database selection in both modes with static/dynamic credentials. Reject invalid encoding before plugin creation/authentication-response sending; retain logical identities as Go strings.
- [x] Pass the same command; create the version/capability evidence table `docs/go-driver-compatibility-matrix.md`, recording only confirmed source conditions and marking others pending evidence; do not guess server-version thresholds.

### D04 Connection Establishment, Initialization, Ping, and Integration Entry Points

Dependencies: D03. Coverage: M01, M04, M05; architecture §5, §8–§9.

Files: add `conn.go`, `session.go`, `connector_test.go`, `tests/integration/env_test.go`, `fixtures_test.go`, `smoke_test.go`; complete `connector.go`.

Deliver: Connector.Connect, basic driver.Conn methods, and Pinger; a total connection budget covering Dial/TLS/credentials/initialization, closing connections on failure; §5.2 test environment configuration and temporary-object cleanup.

- [x] Test `TestConnectBudget`, `TestConnectorModePinning`, `TestPingFailure`: all Dial/TLS/credential/initialization stages share one total budget; concurrent initialization never mixes modes; failed initialization does not pin mode; Ping failures cannot report success. D26 adds multi-address iteration, reusing this total budget rather than restarting the timer.
- [x] `TestDialContextNilConnection` and `TestDialContextErrorClosesConnection` verify nil/typed-nil custom-dial rejection, error chains/retry counts, and close ownership when both a valid connection and error are returned; do not invoke typed-nil methods, enter credential callbacks, or admit these connections into the pool.
- [x] Run `go test . -run 'Test(Connect|ConnectorModePinning|Ping)' -count=1` and confirm failure.
- [x] Implement baseline connection/initialization; Exec/Prepare/BeginTx explicitly fail while unavailable rather than treating empty implementations as success.
- [ ] Pass the same command; integrate jobs into existing CI and execute `TestSmoke` for four profiles with OB_TEST_REQUIRE=1 for acceptance, recording actual authentication/mode/version. Missing environments remain pending integration and cannot pass G1.

### D05 COM_QUERY, Text Results, and Standard Counts

Dependencies: D04. Coverage: M06, M08; architecture §10.1, §11.

Files: add `execute.go`, `rows.go`, `result.go`, `error.go`, `codec.go`, `codec_mysql.go`, `codec_oracle.go`, and tests; add `tests/integration/query_test.go`.

Deliver: ExecerContext/QueryerContext, driver.Rows/RowsNextResultSet, basic column information, EOF/OK/ERR/session tracking; overflow checks on standard counts; errors.As-compatible server errors; correct subsequent response handling after the first row read. LastInsertId returns ErrUnsupported without a nonzero OK insert ID, separately from known RowsAffected=0. Check each field independently for int64 overflow; Execute summaries retain complete unsigned IDs without inferring primary keys.

- [x] Test `TestTextRows`, `TestResultCounts`, `TestRowsDrain`: distinguish NULL/empty; []byte ownership; EOF/OK termination; result transitions; zero/overflow counts; SQLState errors; synchronized connections after early Close.
- [x] Run `go test . -run 'Test(TextRows|ResultCounts|RowsDrain)' -count=1` and confirm failure.
- [x] Implement standard text queries and complete response boundaries in both modes; explicitly fail unknown types instead of treating all as BLOB.
- [ ] Pass the same command; run `TestQueryCRUD`, `TestQueryResults` in both modes; provide standard OpenDB/Query examples. G1 also requires passing D03 security and D01 logger tests.

### D06 Cancellation, Error Boundaries, and Connection Eviction

Dependencies: D05. Coverage: M05, M13; architecture §12.1, §12.2, §12.5.

Files: modify `conn.go`, `session.go`, `execute.go`, `rows.go`; add `cancel_test.go`, `retry_test.go`, `validator_test.go`.

Deliver: Validator, coordinated concurrent closing, stopping/waiting for context.AfterFunc; ErrBadConn is allowed for pre-send invalidation, while post-send failures preserve the original error/uncertainty; shared cleanup budgets.

- [x] Test `TestNoReplayAfterSend`, `TestCancelCompletionRace`, `TestCloseBudget`: one business command after partial writes/lost responses; old cancellation callbacks cannot close subsequent operations; bounded Close; invalid connections cannot return to the pool.
- [x] Run `go test . -run 'Test(NoReplay|CancelCompletionRace|CloseBudget)' -count=1` and confirm failure.
- [x] Implement cancellation/invalidation across the full Query Rows lifetime; do not return retryable ErrBadConn merely to discard a connection.
- [ ] Pass the same command; run `go test . -run 'Test(NoReplay|Cancel|Close)' -count=1` on supported platforms and integrate with existing CI; reuse equivalent assertions in later resource tasks. Race detection requires a manual cgo run and is excluded from CI.

### D07 Lexical Binding and Argument Conversion

Dependencies: D05. Coverage: M07, M09; architecture §10.2–§10.3, §11.2.

Files: add `sql_lexer.go`, `bind.go`, `bind_oracle.go`, `bind_test.go`, `bind_fuzz_test.go`; modify `codec*.go`.

Deliver: §3.2 bind plans; NamedValueChecker/NumInput; standard values, Valuer, uint64, and necessary extended inputs. Separate private requests from SQL arguments; validation has no side effects.

- [x] Test `TestBindPlan`, `TestNamedValueValidation`: quoting, comments, q-quotes, :=, anonymous-block semicolons; repeated names; reject missing/extra/mixed arguments and MySQL named arguments; zero network writes on Valuer errors.
- [x] Run `go test . -run 'Test(Bind|NamedValue)' -count=1` and confirm failure.
- [x] Implement the lexical state machine and single argument conversion; reject unsupported inputs without fmt-based interpolation.
- [x] Pass the same command and bind fuzzing; both preparation modes reuse one plan rather than separate argument-count rules.

### D08 Server Preparation and Binary Results

Dependencies: D06, D07. Coverage: M06–M09; architecture §7, §10, §11.

Files: add `stmt.go`, `stmt_test.go`, `binary_rows_test.go`; modify `execute.go`, `codec*.go`; add `tests/integration/prepare_test.go`.

Deliver: ConnPrepareContext, StmtExecContext/StmtQueryContext, binary arguments/results, statement IDs, type headers, and NULL bitmaps; establish bounded, correct server-prepare caching first.

- [x] Test `TestServerPrepare`, `TestBinaryRows`, `TestStmtOwnership`: NULL bitmap boundaries, uint64/large numbers, changed types on repeated execution, idempotent close, wrong-session ID rejection; failed execution responses never trigger client retries.
- [x] Run `go test . -run 'Test(ServerPrepare|BinaryRows|StmtOwnership)' -count=1` and confirm failure.
- [x] Implement server-statement creation/execution/close; legacy standard methods delegate to context paths without introducing different semantics.
- [ ] Pass the same command; compare text/binary results with `TestPrepareRoundTrip` in both modes; DB can safely recreate Statements on different physical sessions.

### D09 Local Transactions, Savepoints, and Reset Generations

Dependencies: D08. Coverage: M05, M13; architecture §12.3–§12.4.

Files: add `tx.go`, `savepoint.go`, `tx_test.go`, `reset_test.go`; modify `session.go`, `stmt.go`; add `tests/integration/tx_test.go`.

Deliver: ConnBeginTx/driver.Tx, §3.2 savepoint functions, Managed/Full reset; discard connections when Full reset is unreliable; invalidate statement generations after reset and prepare again on demand.

- [x] Test `TestTxOptions`, `TestCommitUnknown`, `TestResetGeneration`: reject unsupported isolation; lost COMMIT responses cannot report rollback; safe ErrBadConn after reset failure; adjacent calls on a pinned Conn do not reset.
- [x] Run `go test . -run 'Test(Tx|CommitUnknown|Reset)' -count=1` and confirm failure.
- [x] Implement transaction state/recovery, quoted savepoint identifiers, and statement-generation checks; never swap physical connections to finish a transaction.
- [ ] Pass the same command; run `TestLocalTx`, `TestSavepoints`, `TestStmtAfterReset` in both modes; verify state/resource cleanup during pool reuse.

### D10 Client Preparation and Explicit Escape Rewriting

Dependencies: D07, D08. Coverage: M06, M07; architecture §10.1–§10.2.

Files: add `client_prepare.go`, `escape.go`, `client_prepare_test.go`, `escape_test.go`; modify `execute.go`.

Deliver: explicit client-prepare strategy; public `RewriteJDBCEscapes(query string, mode Mode) (string, error)`; reject rewriting with insufficient ModeAuto context rather than guessing a mode.

- [x] Test `TestClientPrepareEncoding`, `TestJDBCEscapes`: quotes/backslashes/multibyte/binary values, modes/session escape settings, JDBC date/function/call syntax; ordinary SQL is unchanged by default; placeholders in comments are not replaced.
- [ ] Run `go test . -run 'Test(ClientPrepare|JDBC)' -count=1` and confirm failure; historical failure is not recorded in current progress and cannot be inferred from present passing results.
- [x] Reuse D07 lexing/bind plans and actual session encoding; allow configured fallback only when unsupported preparation is established before sending.
- [ ] Pass the same command; compare client/server values and errors in both modes with `TestPrepareEquivalence`; string similarity cannot prove execution equivalence.

SQL/client budget supplement: TestSQLTextEncodedBudget, TestClientSQLTextEncodedBudget, TestSQLBudgetStrictEncoding, TestSQLExecutionEncodedBudget, and TestOutputSQLTextEncodedBudget cover actual encoded sizes, cumulative fragments, literal escaping, public text/server/client preparation in both modes/three packet streams, and Oracle OUT: 58 local scenarios. Parsing prechecks bound source code-point counts; full-message limits include transcoded content, command/protocol overhead, and spaces. Reject before sending when over budget; the original session remains Ping-capable. GBK/latin1 contraction false rejections were reproduced and fixed; real SQL equivalence/server acceptance remain separate.

JDBC CONVERT boundary supplement: MySQL boolean/floating emulation preserves compound-operand precedence; both modes preserve newlines after trailing line comments. Blank/comment-only operands return ErrBind before sending. Regressions cover OR/XOR/comparison/bitwise operations, NULL, nested conversions, and argument counts; real SQL equivalence remains subject to the integration item above.

### D11 Execute, Multiple-Result Summaries, and Count Policies

Dependencies: D08, D09. Coverage: M08; architecture §11.4.

Files: add `execution.go`, `execution_test.go`; modify `rows.go`, `result.go`, `capabilities.go`; add `tests/integration/multi_result_test.go`.

Deliver: §3.2 Execute/Execution, architecture ResultInfo/ExecutionSnapshot, MatchedRows/ChangedRows, AllowMultiStatements, and MaxResultItems; ordinary Rows contain no artificial row sets.

- [x] Test `TestExecutionOrder`, `TestExecutionPartialError`, `TestExecutionClose`: UPDATE→SELECT→UPDATE order, RowSetIndex=0, zero versus unknown, full uint64 IDs, summaries retained on direct Query errors, limit failures, final Close/automatic-close state.
- [ ] Run `go test . -run 'TestExecution' -count=1` and confirm failure; historical failure is not recorded in current progress and cannot be inferred from present passing results.
- [x] Implement parser publication into a private carrier, thread-safe snapshots, and shared finalization; never invent internal procedure counts absent from the server response.
- [ ] Pass the same command; run `TestMultiResult` in both modes; compare MySQL no-op UPDATE/upsert counts and reject ChangedRows in Oracle. Snapshot only observes and never performs network operations.

### D12 Complete Types, Character Sets, and Time Semantics

Dependencies: D08, D10. Coverage: M09; architecture §11.2.

Files: add `types.go`, `number.go`, `temporal.go`, `internal/charset/`, and corresponding tests; extend `codec*.go` and `tests/integration/types_test.go`.

Deliver: lossless Scanner/Valuer for Number, NString, RowID/Raw, TimestampTZ/LTZ, IntervalYM/DS; concrete types and sql.Null[T] retain binding identity; character sets/collation, NCHAR, and actual session timezone. Session.ParseTime retains text by default; explicit timezone-free date/timestamp parsing rejects invalid/DST-ambiguous values; ordinary Scan sources remain standard values.

- [x] Test `TestTypeBoundaries`, `TestTemporalLossless`, `TestCharsetRoundTrip`: maximum/minimum/overflow numbers, negative/>24-hour TIME, zero dates, nanoseconds/regions/DST, month intervals, invalid encodings, NULL/empty strings/empty bytes; NUMBER never becomes float64.
- [x] Run `go test . ./internal/charset -count=1` and confirm new assertions fail.
- [x] Implement type mappings and text conventions; if character sets require a dependency, add only the corresponding encoding library, recording version/purpose; do not build a generic encoding framework.
- [ ] Pass the same command; verify `TestAllScalarTypes` across modes/text/binary/client paths; record support differences in the compatibility matrix.

Oracle DATE metadata supplement: protocol type 12 reports DATE in Oracle and DATETIME in MySQL; ParseTime changes only Scan sources, never type names or the time component. TestDateColumnTypes/TestDateParameterMetadata provide local regressions. TestAllScalarTypes adds DATE/TIMESTAMP column-name checks and an Oracle DATE fixture containing time; integration only compiles, and real verification remains unchecked.

Binary text-parameter budget supplement: ordinary strings, NString, and complex-type text fields enforce content budgets using actual transcoded bytes, preserving source-code-point lower bounds, strict encoding, and cumulative length-prefix/command limits. TestParameterEncodedBudget, TestComplexTextEncodedBudget, and TestPreparedParameterEncodedBudget provide 63 local scenarios covering contraction/expansion, exact boundaries, zero/negative budgets, applicable combinations in both modes, and public preparation across three packet streams; rejection precedes sending and the original session remains Ping-capable. SQL text/client-interpolation budgets are separate under D10; LongData retains both source-byte and transcoded-byte limits. This does not complete real-database acceptance.

Number budget supplement: TestNumberNormalizedBudget, TestComplexNumberNormalizedBudget, and TestNumberSQLNormalizedBudget provide 46 local scenarios covering normalization contraction, fixed-point expansion, precision, zero/negative budgets, cumulative complex-field prefixes, and public server/client preparation in both modes/three packet streams; extreme positive/negative exponents are also rejected before expansion. Construct results only after checking final fixed-point bytes and platform MaxInt, avoiding an oversized mantissa copy first. FuzzScalars adds contraction seeds and variable 0–510-byte budgets, checking exact-value preservation. Real precision/server round trips remain pending integration.

LTZ preparation equivalence supplement: TestLTZPrepareEquivalence and TestLTZSQLPrepareEquivalence provide 56 scenarios covering cross-region session conversion in client/server preparation, nanoseconds, same-region wall-time preservation, source DST gaps/overlaps and target overlap rejection, session year boundaries, public preparation across three packet streams, and reuse of the original session after local rejection. Client and binary paths share session wall-time validation; offset SQL literals cannot bypass binary-path ambiguity/range restrictions. FuzzScalars adds LTZ cross-region, source-gap, and year-boundary seeds and verifies matching acceptance/rejection with a fixed UTC session and sufficient budget. Fixed-offset sessions convert using the offset actually set at initialization even when their names match the source region; additional cases cover date carry, unresolvable source regions, DST ambiguity, and out-of-range years. Real timezone conversion/server type semantics remain pending integration.

NString national-charset supplement: [TestNStringPrepareCharsets](../internal/valuecodec/national_test.go) and [TestNStringSQLPrepareCharsets](../internal/valuecodec/public_national_charset_paths_test.go) provide 62 offline scenarios covering explicit national character sets for both preparation strategies, connection-charset fallback, default utf8mb4, empty values, invalid UTF-8, unrepresentable input, and separate SQL latin1/ASCII versus national UTF16. Public database/sql execution across three packet streams verifies statement/original-session reuse before and after rejection without invalid execution commands. With at most 512 source bytes and a separate sufficient 8192-byte budget, FuzzScalars compares acceptance/rejection for eight national configurations including fallback, without treating binary content and UNISTR expansion budgets as equal. Real NCHAR round trips remain pending integration.

### D13 Database, Result, and Parameter Metadata

Dependencies: D09, D12. Coverage: M11; architecture §16.1.

Files: add `metadata.go`, `metadata_sql_mysql.go`, `metadata_sql_oracle.go`, `metadata_test.go`; complete five RowsColumnType interfaces in `rows.go`; add `tests/integration/metadata_test.go`.

Deliver: ColumnTypes, parameter plans/server parameter metadata, and queries for catalogs/schemas/tables/views/synonyms/columns/primary and foreign keys/indexes/privileges/procedures/functions/types. Public entry: `Metadata(ctx context.Context, session Session, filter MetadataFilter) (*sql.Rows, error)`; filter.Kind is a closed enum and each result category has fixed column names/types.

- [x] Test `TestMetadataFilters`, `TestColumnTypes`, `TestMetadataInternalBoundary`: parameterized filters, pinned modes, ok=false for unknown length/precision, quoted identifiers, schema case/synonyms, and no fabricated unavailable data; public/internal entry points share query plans, and internal calls never reenter standard-library sessions.
- [x] Run `go test . -run 'Test(Metadata|ColumnTypes)' -count=1` and confirm failure.
- [x] See architecture §16.1.1 for usage, MetadataFilter, and 17 column schemas. SQL for both modes and a same-physical-session executor are implemented without public Metadata reentry. Response/gate ownership is tested; D18 type descriptions establish the description cache, sharing D13 query plans without caching arbitrary dictionary Rows.
- [ ] Pass the same command; compare `TestMetadataCatalog` with real dictionary contents of created objects, recording explicit insufficient-privilege/version errors.

Locally complete: full-package test/vet/386 on both toolchains, ordinary/integration lint, repeated targeted checks, and FuzzMetadataFilters pass. TestMetadataCatalog only compiles; the final real comparison remains unchecked. See progress D13.

BIGINT UNSIGNED ScanType must be uint64 so scan targets created through metadata reflection accept the full unsigned range; signed BIGINT remains int64 and ordinary row sources remain lossless int64/string. TestUnsignedBigIntScanType covers three execution paths/three packet streams, boundaries, and NULL; TestAllScalarTypes real boundary comparisons only compile.

MySQL BINARY/VARBINARY type names are distinguished by character set 63; BINARY_FLAG on an ordinary text column must not change it to a binary type. TestBinaryStringColumnTypes/TestBinaryStringParameterMetadata cover both modes, three packet streams, and three result-execution paths, verifying raw bytes, empty/NULL, ScanType, lengths, and session reuse; Oracle VARCHAR2/CHAR remain unchanged. TestAllScalarTypes fixed BINARY(4)/VARBINARY(100) comparisons only compile.

### D14 Scalar OUT, INOUT, and RETURNING

Dependencies: D11, D12. Coverage: M07, M08; architecture §13.1, §13.3.

Files: add `call.go`, `call_execution.go`, `call_mysql.go`, `prepare_execute.go`, `out_test.go`, `out_paths_test.go`; modify `bind*.go`, `execute.go`, and necessary wire command definitions; add `tests/integration/call_test.go`.

Deliver: sql.Out, Out with explicit database type/length/precision, RETURNING; stage conversions before publishing ordinary built-in outputs, and explicitly report possible partial side effects of custom Scanners. This task delivers the minimal COM_STMT_PREPARE_EXECUTE encoding/response and version/capability gates needed by Oracle RETURNING; D24 reuses them for general strategies. Do not postpone required protocol foundations as an optimization or make D14 depend backward on D24.

- [x] Test `TestOutValidation`, `TestOutFinalization`, `TestOutReturningProtocol`: reject nil/unwritable destinations and unsupported types before sending; INOUT initial values; NULL cannot become a non-null zero; overflow cannot partially update built-in targets; Scanner failures do not hide earlier side effects; required RETURNING combined commands, output ordering, and zero business sends when capabilities are insufficient.
- [x] Run `go test . -run 'TestOut' -count=1` and confirm failure.
- [x] Implement actual OUT responses in both modes and the combined base path required by RETURNING; variable intermediates and cleanup stay on the pinned session; outputs take effect only after the final response. Without source and target-server evidence, ordinary separate prepare/execute is not a RETURNING substitute.
- [x] Pass the same command locally; protocol peers cover multiple scalar outputs, functions, anonymous blocks, and errors; D20 implements connection-handle outputs.
- [ ] Verify these behaviors using `TestScalarCall`, `TestReturning` against real servers/proxies in both modes; existing integration source only compiles and database tests have not run.

Local implementation/verification are complete; `TestScalarCall`, `TestReturning` integration source only compiles, and current constraints defer real-database integration. Checking local steps does not complete real mode/version acceptance.

### D15 Pinned-Session Resources and Lifecycles

Dependencies: D09, D11. Coverage: M08, M10, M13; architecture §10.3, §13.2, §14.1.

Files: add `resource.go`, `resource_test.go`; reuse Session validation from `execution.go`; modify `conn.go`, `bind.go`, `execute.go`, `stmt.go`, `rows.go`, `tx.go`, `reset.go`, `call_execution.go`.

Deliver: concrete internal resource records, generation checks, parent-statement leases, and private-request dispatch; public Session retains only the architecture's Exec/Query call surface.

- [x] Test `TestResourceSession`, `TestResourceGeneration`, `TestResourceLease`: reject DB/wrappers/typed nil before sending; no reuse across Conn/Tx/reset; release the server statement only with the final lease; Close does not end the caller's transaction.
- [x] Run `go test . -run 'TestResource' -count=1` and confirm failure.
- [x] Implement shared identity/cleanup rules without retaining driverConn from Raw callbacks or adding public ResourceManager/global handle registries.
- [x] Pass the same command; unpublished resources do not leak on faults, transaction end, or reset; later extensions reuse these assertions.

This task supplies internal lifecycle foundations; D16/D19/D20 add concrete LOB/cursor records and server-release commands. This alone does not establish advanced handle availability or real-database acceptance.

### D16 Native BLOB/CLOB/NCLOB

Dependencies: D12, D13, D15. Coverage: M10; architecture §14.1–§14.3.

Files: add root `lob*.go` files (public entry points, native dispatch, character operations, mutation, bounded row sets), `internal/wire/lob.go`, and tests; modify Rows, codecs, resources, combined execution, and handshake; add `tests/integration/lob_test.go`.

Deliver: OpenBlob/OpenClob; fix the operation surface below before implementing native backends. Reader/Writer adapters bind explicit contexts, never retain the open operation's context as a permanent lifetime.

```go
func (b *Blob) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error)
func (b *Blob) WriteAtContext(ctx context.Context, p []byte, off int64) (int, error)
func (b *Blob) Size(ctx context.Context) (int64, error)
func (b *Blob) Truncate(ctx context.Context, size int64) error
func (b *Blob) Close() error
func (c *Clob) ReadChars(ctx context.Context, off, count int64) (string, error)
func (c *Clob) WriteChars(ctx context.Context, off int64, value string) (int64, error)
func (c *Clob) CharLength(ctx context.Context) (int64, error)
func (c *Clob) TruncateChars(ctx context.Context, count int64) error
func (c *Clob) Close() error
```

Blob short reads/writes follow io.ReaderAt/WriterAt semantics, with zero-based off. Clob off/count use zero-based Unicode code points, not UTF-8 bytes; explicitly convert or reject differing backend character units without silently splitting characters. WriteChars returns code points written.

- [x] Test `TestLOBLocator`, `TestLOBInterleave`, `TestLOBNoReentry`, `TestClobOffsets`: V1/V2, NULL/empty/temporary/persistent, inline/out-of-line, >16 MiB, chunked short reads, character versus byte offsets; no LOB commands within unread responses. Use actual database/sql QueryContext/Rows.Next with protocol peers for original-session materialization, bounded timeouts proving no public-session/operation-lock reentry, rather than merely checking helper invocation.
- [x] Run `go test . -run 'Test(LOB|Clob)' -count=1` and confirm failure.
- [x] Publish locators only after their query fully finishes; name/index Clob character operations separately in rune/character units rather than reusing Blob byte indexes; distinguish temporary LOB release from local close.
- [x] Pass the same command; original-session materialization covers QueryContext/Rows.NextResultSet, scalar OUT, and mixed results without auxiliary connections. Both-version test/vet/386, lint, and locator fuzzing pass locally.
- [ ] Run `TestNativeLOB` for uncommitted reads in the original transaction, cancellation/release/reset; source/manual CI entry points are added and only compiled; current constraints defer database integration.

UTF16 overwrites follow the architecture's permitted rejection boundary: changing existing surrogate-pair width returns ErrUnsupported before writing; equal-width overwrites and appends work. Actual NCLOB server character units/collation remain pending target verification.

### D17 MySQL Table BLOB Locator Emulation

Dependencies: D13, D16. Coverage: M10; architecture §14.4.

Files: add `lob_target.go`, `lob_mysql.go`, and tests implementing BlobTarget/OpenTableBlob; add `tests/integration/table_blob_test.go`. Target resolution/both backends remain in the root with no new dependency edges.

Deliver: explicit table/column/full-primary-key identity, OCTET_LENGTH/SUBSTRING chunked reads, WriteAt/Truncate after locking within Tx; corresponding Oracle targets resolve native locators.

- [x] Test `TestBlobTarget`, `TestTableBlobBounds`, `TestTableBlobTx`: composite primary keys, identifiers, single Valuer conversion, isolation from Key mutation; missing/NULL/empty rows; off+len overflow; append/unchanged/empty writes; reject mutation through Conn before sending.
- [x] Run `go test . -run 'Test(BlobTarget|TableBlob)' -count=1` and confirm failure.
- [x] Implement locked existence checks, parameterized chunk SQL, and generations per architecture §14.4; ChangedRows=0 does not mean missing rows; do not automatically create sparse gaps.
- [x] Pass the same command; fault peers add cross-chunk behavior, cancellation/disconnect prefixes, no replay, read-only/unknown engines, capacities/warnings, Oracle native transaction row locks, and maximum-offset no-ops.
- [ ] Accept real SQL/lock evidence from `TestTableBlob` for sizes above the materialization limit, concurrent locks/rollback, Truncate, etc.; source/manual CI entry points are provided and only compiled in this phase as requested.

See progress D17 for local implementation/verification evidence; real-database behavior does not count as local success.

### D18 OBJECT, ARRAY, and Type Descriptions

Dependencies: D12, D13, D16. Coverage: M09, M10, M11; architecture §11.2, §14, §16.1.

Files: add `complex.go`, `complex_codec.go`, `type_descriptor.go`, ordinary/fault/fuzz tests for complex values, and `internal/wire/complex.go`; modify codecs/dictionary/preparation/OUT/Rows; add `tests/integration/complex_test.go`.

Deliver: Object/Array Scanner/Valuer, actual complex-type binding protocol, and self-describing versioned JSON text scan representations. Reuse type descriptions by mode/schema/name/protocol version only within one business command on the same physical session, clearing on each command/reset to avoid reuse across privileges/schemas. Zero denotes the server's current version.

- [x] Test `TestComplexLossless`, `TestDescriptorRefresh`, `TestDescriptorNoReentry`: nested NULL/empty collections, decimal text, base64, time/field order, synonyms/type versions, maximum nesting; refresh after DDL mismatch without replaying sent operations. Cover description-cache misses through actual database/sql QueryContext/Rows.Next and protocol peers, bounded timeouts, same physical session, and dictionary lookup after the full response; assert no public-session/operation-lock reentry.
- [x] Run `go test . -run 'Test(Complex|Descriptor)' -count=1` and confirm failure.
- [x] Implement explicit type names/field plans rather than reflection over arbitrary structs/slices; description queries use only the internal executor at complete-response boundaries, never Metadata/Session/sql.Conn public entry points.
- [x] Pass the same command locally; add regressions for complex OUT column counts, shared materialization budgets, draining subsequent complex row sets, invalid collection-element headers, Close final-OUT timeouts, and buffered description refresh.
- [ ] `TestComplexTypes` provides four-target integration source for objects/collections, NULL elements, precision/RAW/DATE, nested LOBs, original transactions, and complex OUT; only compiled in this phase. Real-server type compatibility remains pending integration; byte peers cannot substitute for acceptance.

Local support boundaries appear in architecture §11.2.2: only server-prepared binary complex results; explicitly reject text complex results, client interpolation, non-NULL TIMESTAMP/LOB field inputs, and collections without confirmed element headers. Real environments must supply corresponding protocol/compatibility evidence; local implementation completion does not establish full acceptance.

### D19 Cursors, Scrolling, Refresh, and Updatable Results

Dependencies: D13, D15, D16. Coverage: M08; architecture §15.

Files: add root `cursor.go`, `cursor_protocol.go`, `cursor_test.go`, `cursor_update.go`, `cursor_update_test.go`; add `tests/integration/cursor_test.go`; reuse preparation within the statement's original connection gate, with no new subpackage/reverse dependency.

Deliver: OpenCursor/Fetch/Close, CursorOptions/CursorPosition; explicit next/absolute/relative positions. Update/Delete/Insert/Refresh use closed operations, column changes, and reliable row identity, with no arbitrary-operation command map.

- [x] Test `TestCursorFetch`, `TestCursorUpdateIdentity`: close previous batches first; zero/negative/out-of-range positions; bounded static caches; caches cannot impersonate sensitive cursors; reject updates for joins/aggregates/missing row identity; no-op updates cannot falsely report concurrent deletion.
- [x] Run `go test . -run 'TestCursor' -count=1` and confirm failure.
- [x] Implement supported offset fetch/positioning and parameterized DML; ChangedRows supports updatable results only with reliable locked existence checks; otherwise reject before sending.
- [x] Pass the same command locally; protocol peers cover support conditions in both modes, LOB fetch, update identity, errors, and transaction-end invalidation.
- [ ] Run `TestCursorModes`, `TestCursorUpdates` against real servers/proxies in both modes for cursors/concurrent changes; integration source only compiles.

See architecture §15 for current local positioning, version, primary-key/transaction, and refresh boundaries. Integration scenarios are provided as compiled-only source; real acceptance remains unchecked.

### D20 REF CURSOR and Streaming LOB Outputs

Dependencies: D14, D16, D19. Coverage: M08, M10; architecture §13.2.

Files: add `cursor_out.go`, `cursor_out_test.go`, `cursor_child_test.go`, `tests/integration/cursor_out_test.go`; modify output finalization, Execution, cursor protocol, resource leases, binary-row decoding, and CI.

Deliver: CursorOut and explicit Out to `**Blob`/`**Clob`; only Execute accepts connection-handle outputs; ordinary REF CURSOR columns retain standard child driver.Rows lifetimes.

- [x] Test `TestCursorOutPublish`, `TestOutputLease`: initially nil destinations, multiple outputs, successful early-Close publication, no publication after read failure; Cursor remains usable after Execution.Close; final handle releases the parent statement; later Fetch does not inherit old context.
- [x] Run `go test . -run 'Test(CursorOut|OutputLease)' -count=1` and confirm failure.
- [x] Reuse D11 finalization/D15 leases; atomically publish built-in handles only after successful output conversion and final response.
- [x] Pass the same command locally; protocol peers verify mixed ordinary results/counts/multiple cursors/LOBs, close/failure, and parent-statement lease release.
- [ ] Run `TestProcedureResources` against real servers/proxies in both modes for procedure resources and close/failure behavior; integration source only compiles.

See architecture §13.2 for current mode/version, child-Rows buffering/depth, and publication boundaries. Local implementation/protocol tests are complete and integration source only compiles; real acceptance is not passed.

### D21 Long Parameters and Chunked Uploads

Dependencies: D08, D12, D15. Coverage: M07, M10; architecture §10.1, §13.4, §14.2.

Files: add `long_data.go`, `long_data_test.go`; modify stmt/bind, wire parameter headers, OUT/cursors, and shared Rows cancellation cleanup; add `tests/integration/long_data_test.go`.

Deliver: explicit long-parameter inputs with Reader ownership/close rules fixed in the type; choose LONG_DATA/PIECE_DATA by mode capabilities, enforce a total bound for unknown lengths, and upload without first reading all content. Public *LongData Length/MaxBytes/Text, borrowed Readers, and single-consumption rules appear in architecture §13.5. Reuse the same gate; upload failures evict the connection and cancellation between upload/execute also cleans up. Ordinary PrepareClient explicitly rejects these inputs; required native OUT paths remain.

- [x] Test `TestLongDataChunks`, `TestLongDataFailure`: empty/unknown lengths, chunk boundaries, Reader errors/cancellation, partial writes, exceeded limits; no rewind/second execution after errors.
- [x] `TestLongDataNilReader` rejects all nil-capable concrete Reader types; `TestLongDataNilReaderSession` verifies rejection before preparation in both modes/three packet streams without consuming the object, reuse after corrected input, and acceptance of non-nil function/map/slice/channel empty Readers.
- [x] Run `go test . -run 'TestLongData' -count=1` and confirm failure.
- [x] Implement single-use long-parameter encoding and failure cleanup; upload support does not replace MySQL table-based random access.
- [x] Pass the same command; four-target `TestLongParameters` source covers large-content hashes and commit/rollback in both modes, but only compiles; real semantics remain pending integration. Logging object contents is unnecessary.

### D22 Batches, Bulk, Array Binding, and Rewrite Strategies

Dependencies: D11, D14, D18, D21. Coverage: M07; architecture §13.4.

Files: add `batch.go`, `batch_protocol.go`, `batch_array.go`, batch tests, `internal/wire/pipeline.go` / `stmt_array.go` and tests, `tests/integration/batch_test.go`; modify binding, execution entry points, and shared Rows finalization.

Deliver: ExecBatch, BatchOptions/BatchResult/BatchError; per-item states, known/unknown counts, stop-on-error by default and explicit continuation; capability selection for sequential execution, multi-send, safe rewriting, bulk, and array binding.

- [x] Test `TestBatchResults`, `TestBatchAtomicity`: intermediate failures/subsequent item states, unknown counts, OUT, chunk limits; do not turn partially successful autocommit tasks into all-or-nothing single-statement failures; disconnects never resend sent items.
- [x] Run `go test . -run 'TestBatch' -count=1` and confirm failure.
- [x] Sequential/restricted multi-send paths are implemented; native Conn/Tx takes one standard-library lock and DB pins a session for the entire batch. Fall back to sequential execution without equivalence evidence; never change an existing Tx or implicitly commit; cleanup errors retain confirmed successes and cancellation retains earlier SQL errors.
- [x] Pass the same command; `TestBatchStrategies`, `TestBatchTxBoundary`, and wire `TestPipelineWritesAhead` compare content/order/errors/transactions and actual writes ahead. BatchRewrite adds restricted MySQL multi-statements, explicit permission, and per-chunk server-option gates. Oracle native array binding adds fixed >=4.2.1, writable-transaction/explicit-continue gates, column-oriented parameters, and saved-exception parsing. Explicit BatchMultiValues adds simple MySQL placeholder tuples and aggregate chunk results. Bulk/ineligible requests fall back sequentially; see architecture §13.4. Four-target `TestBatchExecution` / `TestBatchRewriteExecution` / `TestBatchArrayBindingExecution` source only compiles; real-server support/equivalence remain unverified.

### D23 Compression, OB20, New Metadata, and Checksums

Dependencies: D06, D08. Coverage: M03, M04, M15; architecture §9.

Files: add `internal/wire/compress.go`, `ob20.go`, `checksum.go`, and tests; add root `protocol.go`, session-tracking decoding/fuzzing; modify capability/packet-stream selection, column interpretation, and batch-strategy gates.

Deliver: standard/compressed/OB20 streams sharing command codecs, request IDs, sequences, extension fields, and checksums; auto/off/required use actual negotiation.

- [x] Test `TestCompressedPackets`, `TestOB20Frames`: cross-packet boundaries, checksum failures, skippable unknown fields, invalid sequences, decompression expansion bounds; required but unsupported fails connection establishment, never switches protocol after business sends.
- [x] Run `go test ./internal/wire -run 'Test(Compressed|OB20)' -count=1` and confirm failure.
- [x] Implement stream variants/new column metadata with one response state machine; check sizes before decompression/allocation.
- [x] Pass the same command and `FuzzFrames` locally; tests cover both modes/three streams, per-message budgets, byte evidence, ID wraparound, cancellation, and frame-batch fallback; four-target `TestPacketModes` source only compiles.
- [ ] Pin actual versions and execute four-target `TestPacketModes` for registered positive/rejection cases; current user instructions defer integration, so local tests cannot check real acceptance.

D23 is implemented locally; acceptance remains incomplete. Five independent options/protocol boundaries appear in architecture §9.1 (all five options are tristate); proxy checksum and OB20 CRC are mapped separately. See development progress §5.6 D23 for evidence.

### D24 PrepareExecute, Caching, and Execution Optimization

Dependencies: D08, D11, D14, D21, D23. Coverage: M06, M07; architecture §3, §10.

Files: extend D14 `prepare_execute.go`; modify `stmt.go`, `execute.go`, `rows.go`, and batch/cursor callers; add `execution_options.go`, `stmt_cache.go`, `stmt_types.go`, tests, and encoding benchmarks.

Deliver: reuse D14's required combined base protocol for general Java-equivalent prepare/execute strategies, parameter-type header reuse, checksum/cache, and capability-selected optimizations; bounded per-physical-session LRU caches. Completed D14 is an explicit prerequisite for OUT regressions.

- [x] Test `TestPrepareExecuteGate`, `TestStatementCache`: conservative choices for threshold/unknown versions, type changes/cache eviction/Full reset, no double execution after combined-path errors; results equivalent to ordinary server execution.
- [x] Run `go test . -run 'Test(PrepareExecute|StatementCache)' -count=1` and confirm failure.
- [x] Register optimization conditions from pinned source in the compatibility matrix and integrate with existing execution plans; fallback only before business sends.
- [x] Local commands and parameter-type/SQL CRC golden-packet tests pass; encoding benchmarks are complete but do not establish server-throughput gains; four-target `TestExecuteStrategies` source only compiles.

- [ ] Pin actual versions for both modes/proxies and run `TestExecuteStrategies` for values, OUT, errors, and resource closure; current user instructions defer integration.

D24 is implemented locally; real acceptance remains incomplete. See architecture §10.1.1 for options/cache/handle boundaries, compatibility matrix §15 for gates, and development progress §5.6 D24 for execution evidence.

### D25 slog Diagnostics, Profiling, and Full Link Trace

Dependencies: D11, D23. Coverage: M15; architecture §18.

Files: extend `logging.go`; add `trace.go`, `trace_test.go`, `diagnostic_test.go`, `tests/integration/trace_test.go`.

Deliver: structured slow-request reporting, bounded SQL/packet diagnostic callbacks, OB20 FLT; request-scoped `WithTraceInfo(ctx context.Context, info TraceInfo) context.Context`; optional OTel bridge with no mandatory exporter.

- [x] Test `TestDiagnosticRedaction`, `TestTraceIsolation`: nil logger, no network-lock deadlocks on handler reentry, no credentials/LOBs/parameters in logs; no trace leakage on connection reuse; disabling slog does not disable protocol FLT.
- [x] Run `go test . -run 'Test(Diagnostic|Trace)' -count=1` and confirm failure.
- [x] Define TraceInfo module/action/client fields and length limits; assign separate request boundaries to query/fetch/LOB/batch/connection establishment.
- [x] Pass the same command locally; client protocol is implemented, with local evidence for nil logger, inter-command isolation, and redaction.
- [ ] Run `TestFullLinkTrace` against observable real OBProxy/server information; integration source only compiles. Without server-trace evidence, record only client implementation completion.

D25 contracts appear in architecture §18.3.1: metadata/fingerprint-only diagnostics; bounded synchronous delivery; clear app information per command; server-controlled sampling and one-time deferred span reporting; no mandatory exporter. Real-server visibility does not count as local completion.

### D26 Static Multi-Address Routing, Load Distribution, and Connection Failover

Dependencies: D06, D09. Coverage: M12; architecture §17.1.

Files: root routing.go/failover.go, configuration/DSN/connector/reset/tx; internal/routing/address.go, selector.go, health.go, and tests; integration source and CI.

Deliver: static candidates, random/round-robin/affinity/group selection, shared bounded health/blacklists; Connector-pinned roles; retries only within a new connection's total budget.

- [x] Test `TestAddressSelection`, `TestFailoverBudget`, `TestRoleIsolation`: backoff/expiry/concurrency, tenant isolation, bounded authentication/mode rejections; transactions/sent queries never switch sockets.
- [x] Run `go test ./internal/routing . -run 'Test(Address|Failover|Role)' -count=1` and confirm failure.
- [x] Implement selection/connection retries; read/write roles use two standard sql.DB instances; BeginTx never switches connections.
- [x] Both local toolchains, actual 386/test/vet/lint, repeated targeted regressions, and address fuzzing pass; progress updated through D26.
- [ ] Race/Linux/real endpoints remain pending acceptance. TestConnectionFailover source/CI are added and only compiled this phase; each profile requires at least two real endpoints for the same tenant/mode, then verifies new-connection failover/recovery and no redial for active transactions. Protocol peers first verify no replay after lost business responses.

See architecture §17.1.1 for API/defaults, no retries after TLS alerts, and single initialization budgets; existing sockets cannot be switched.

### D27 TNS, OCP, and Topology Discovery

Dependencies: D26. Coverage: M02, M12; architecture §17.1.

Files: root `discovery.go`, configuration/DSN/Connector lifecycle; add `internal/routing/tns.go`, `ocp.go`, `topology.go`, tests; update compatibility matrix, integration entry points, and CI.

Deliver: extended address descriptions/TNS/OCP and discovery adaptations corresponding to replication/aurora; record mappings according to actual service capabilities rather than treating external product names as OceanBase support promises. See architecture §17.1.2 for D27 public API, source-strategy mapping, actual OCP envelopes, and external TopologySource boundaries.

- [x] Test `TestTNSParser`, `TestOCPRefresh`, `TestTopologyChanges`: nested/invalid descriptions, timeouts/invalid responses, cache expiry, consistent logical service/roles after refresh; Connector close ends refresh without canceling active business connections.
- [x] Run `go test ./internal/routing -run 'Test(TNS|OCP|Topology)' -count=1` and confirm failure.
- [x] Use standard-library parsing/HTTP and lifecycle contexts for bounded refresh; no background goroutine without configuration or global source registration.
- [ ] Pass the same command; verify roles/tenants with `TestDiscovery` against available services; individually mark adaptations lacking environments pending integration.

### D28 Complete Authentication Plugins and Platform Transports

Dependencies: D03, D26. Coverage: M03, M16; architecture §9, §17.2.

Files: extend old/Ed25519/GSSAPI/PAM in `internal/auth/`; add platform adapters/build tags and tests; update Config instance-factory documentation.

Deliver: adapters or equivalent capability mappings for Java's registered authentication and TCP/Unix sockets, Named Pipe/Shared Memory/OBProxy VC; state target platforms, negotiation conditions, and dependencies for each, without claiming all platforms complete through one global switch.

- [x] Local `TestAuthPluginLifecycle`, `TestPlatformTransport*` algorithm/protocol and Windows IPC tests are established: independent per-session state, weak algorithms rejected by default, TLS gates for initial/switch/ChangeUser stages; no plaintext exemption for custom transports; cleanup after cancellation/platform errors.
- [x] See progress D28 for Windows local algorithm/transport failure→success records; `go test ./internal/auth . -run 'Test(AuthPlugin|PlatformTransport)' -count=1` passes. Linux/macOS system libraries and target-transport execution remain unverified under the next item.
- [x] Implement adaptations where protocols are established; isolate authentication/transport dependencies with platform files so base packages do not force native-library loading. Preserve §3.1 one-way dependencies; platform authentication algorithms do not refer back to root configuration or wire.
- [ ] Pass the same command; list each algorithm/transport separately for target-platform/server combinations per progress §5.4, recording positive authentication/I/O, rejection, switching, and cleanup evidence. Accept only after every required item is satisfied; cross-compilation alone cannot complete platform acceptance.

D28 proceeds in stages: explicit old-password enablement, Ed25519/PAM, password character sets, plugin io.Closer lifecycles/raw-token sessions, Windows SSPI, Unix GSSAPI, and Unix socket/Windows Named Pipe/Shared Memory have local implementations; regressions cover algorithms/initial/switch/security/cleanup, native ABI, and local IPC/SSPI peer I/O. See architecture §17.2 for Unix GSSAPI target scope/library dependencies/unexecuted boundaries. Shared Memory cancellation for uncorrelated allocations/soft budgets and single-buffer batch fallback appear in §9.0.5; the standalone OBProxy VC host appears in §9.0.6, with real platforms unverified. D30 public reauthentication is added; real tickets, domain Kerberos, and service combinations remain to verify. Overall completion stays unchecked; see progress/compatibility matrix for evidence.

TestAuthPluginGSSNativeSessions uses the current Windows identity, system NTLM, local TCP, and verified TLS for complete database/sql initialization in both modes, two consecutive ChangeUser operations, baseline reconstruction/Ping after each, final authentication rejection, original-connection close, and pool isolation. It reuses the native SSPI peer; ordinary tests explicitly skip without identity credentials. Positive evidence requires checking that native-target cases did not skip; sandbox package-level success cannot substitute. It covers only standard packet streams and does not prove domain authentication, real-database authorization, or leak-free native resources.

Named Pipe address validation covers Config/DSN rejection of invalid UTF-8 host/pipe names, preservation of valid Unicode, and local native pipe I/O via TestPlatformTransportPipeUTF8 and TestPlatformTransportNamedPipeUTF8, preventing endpoint aliasing during Windows UTF-16 conversion.

TestPlatformTransportUnixMixedSessions / TestPlatformTransportNamedPipeMixedSessions reuse mixed-session fixtures on real system IPC endpoints to check both modes/three packet streams, original-connection reuse, preparation/Rows/transactions/error recovery, and QUIT/close. See architecture §9.0.1 for Windows AF_UNIX EOF boundaries/original-connection close assertions. Default tests/existing CI run these automatically; each Unix target needs actual execution evidence, and Windows AF_UNIX success cannot substitute for Linux/macOS.

The D28 VC sub-batch follows architecture §9.0.6: root obproxy.go owns the DialContext factory, pinned endpoint, and initialization singleton; obproxy_host.go uses standard os/exec/pipes to start standalone C host tools/obproxy-host/main.c, with build tags restricting production platforms. The host binds official init/close and isolates native signals/locale/allocator from Go; Go reuses Unix net.Conn. TestOBProxy* verifies configuration rejection, concurrent single initialization/conflicts/cancellation/failure caching, both modes/three streams, and TLS gates. Standalone C shared-library/host fixtures verify JSON/void**/return codes, temporary handles, bad handshakes/missing libraries/missing symbols, and parent EOF, including stuck init. Ordinary builds need no C compiler; obproxytest builds fixtures with local gcc. Real Linux libraries/versions/signals/lifecycles/services still require execution acceptance; synthetic Windows ABI or cross-compilation cannot complete D28.

### D29 Controlled LOCAL INFILE Import

Dependencies: D06, D21. Coverage: M07 and LOCAL INFILE; architecture §17.3.

Files: add `local_infile.go`, `local_infile_test.go`, `tests/integration/local_infile_test.go`.

Deliver: explicit Reader/request-resolution callbacks, request-scoped permission, size/time limits; server filenames do not directly map to local files; explicit Reader Close ownership.

- [x] Test `TestLocalInfilePolicy`, `TestLocalInfileFailure`: no file reads for unauthorized requests; Reader failures/cancellation/limits terminate the response correctly or evict the connection; no implicit retransmission.
- [x] Run `go test . -run 'TestLocalInfile' -count=1` and confirm failure.
- [x] Send authorized content through the existing execution state machine; no global file allowlist; separate configuration from import SQL.
- [ ] Pass the same command; `TestLocalImport` verifies actual imported row counts/content and failure-transaction behavior; record support conditions in the matrix.

D29 local implementation: `Import` + single-use `LocalInfile`, choosing an exact-name Reader or authorized Open callback. Callers close Readers; the driver closes Open results. Inputs attach only to the current text command, reusing long-parameter bounded reading and existing Rows state machines. Wire adds continuation writes to avoid restarting diagnostics/tracing at sequence wraparound; no new packages/dependencies. No database connection is made currently; the final item's local command passes and four-target integration source/CI are added but not executed, so real acceptance remains unchecked. See architecture §17.3.1 for contracts.

### D30 Session Helpers, Warnings, ChangeUser, and Explicit Cancellation

Dependencies: D09, D11, D15, D26, D28. Coverage: M05; architecture §12.5, §16.3.

Files: add `session_ops.go`, `session_ops_test.go`, `cancel_query.go`, `tests/integration/session_ops_test.go`.

Deliver: schema/catalog, client information/variables, structured warnings, and user switching on pinned *sql.Conn; explicit CancelQuery uses an already obtained connection identity/generation and an auxiliary channel from a configured Connector.

- [x] Test `TestChangeUserIsolation`, `TestChangeUserConversation`, `TestWarnings`, `TestCancelQueryIdentity`: invalidate old statements/LOBs/Cursors; switched-user physical connections never rejoin the original pool; multi-round reauthentication, explicit credential isolation, close on failure; nil logger retains warnings; local session/command generations prevent old tokens affecting subsequent operations. See architecture §16.3.4 for server-ID reuse protocol boundaries.
- [x] Run `go test . -run 'Test(ChangeUser|Warnings|CancelQuery)' -count=1`; missing public APIs fail first, then sub-items pass.
- [x] Expose closed functions instead of an arbitrary private-command interface; ChangeUser repeats authentication gates/rebuilds baseline; successful KILL does not mean the original query never executed.
- [ ] Pass the same command; `TestSessionOperations` verifies identity, warnings, auxiliary-channel TLS, and pool isolation; insufficient privileges return explicit errors.

Initial D30 implementation: session_ops.go supplies schema/catalog/client information/variables on pinned sql.Conn; setters reject active transactions and evict the physical session on close to prevent state leaks. Cache cleanup, actual-mode escaping/encoding, and closed requests reuse the existing core. Warnings and ResultInfo.Warnings provide logger-independent OK/EOF count snapshots; MySQL detail/local clearing, ChangeUser, and explicit CancelQuery were added in stages. Four-target TestSessionOperations, TestChangeUserSessions, TestWarningDetails, TestCancelQuerySessions source only compiles; actual cancellation/identity/privileges/proxies remain pending integration; auxiliary TLS has local peer tests. D28 Shared Memory/VC evidence is incomplete, so overall D30 remains in progress; sub-items on supported transports cannot substitute for dependency completion.

D30 ChangeUser sub-item: change_user.go and protocol/TLS/framing/lifecycle tests implement explicit credentials/database, same-connection reauthentication, same-mode constraints, shared initialization under the held lock, server/client statement identity generations, and pinned-resource invalidation. Old statement.Close never sends potentially reused IDs; post-send failures close connections and successful switched sessions leave the original pool on close. Connector is unchanged and old credential providers are not called. Four-target TestChangeUserSessions source/CI are added without database runs; warnings detail/clearing appear in §16.3.3 and cancellation in §16.3.4. See architecture §16.3.2.

D30 warning sub-item: warnings.go supplies Warning, ReadWarnings, ClearWarnings. MySQL reads the current diagnostic area on demand; Oracle retains protocol counts and rejects detail without fabricated SQLSTATE. Details have row/cumulative-byte limits and never return partial lists on read failure. Local clearing suppresses details until the next ordinary SQL; pool reset/identity changes do not leak old details. TestWarningDetails cases cover clearing, modes, transactions, reset, reenabling after prepared/failed commands, character sets, packet streams, cancellation, malformed/oversized input. Four-target TestWarningDetails integration source requires SQL producing nonzero warnings and only compiles. See architecture §16.3.3.

D30 cancellation sub-item: cancel_query.go supplies opaque CancelTarget, advance QueryTarget acquisition, and CancelQuery. A target allows one attempt and binds the first ordinary SQL/first pipeline chunk. One auxiliary connection to the original actual endpoint reuses explicit Connector policy and validates user/tenant, mode, Network, and verified TLS. Completion/reset/identity change/close expire targets; coordinate in-flight KILL before cleanup/next commands. Losing confirmation after writing evicts the original session without business replay. Architecture §16.3.4 documents the non-atomic server-ID-only verification window. Local tests cover both modes, TLS, privileges/malformed responses/timeouts, completion races, single-use targets, and lifecycle. Four-target TestCancelQuerySessions and CANCEL_SQL/CANCEL_CODE manual CI inputs only compile; no database connections. D28 dependencies remain incomplete and D30 stays in progress.

### D31 XA Branches, State, and Recovery

Dependencies: D09, D14, D18, D30. Coverage: M14; architecture §16.2.

Files: add `xa.go`, `xa_test.go`, `tests/integration/xa_test.go`, `examples/xa/`.

Deliver: XID{FormatID int32, GlobalID []byte, BranchID []byte}; explicit XAStart/XAEnd/XAPrepare/XACommit/XARollback/XARecover/XAForget on pinned *sql.Conn; dedicated flags with length/state validation. Implement commands/XID encoding separately for both modes; callers own coordinator logs.

```go
func XAStart(ctx context.Context, conn *sql.Conn, xid XID, flags XAFlags) error
func XAEnd(ctx context.Context, conn *sql.Conn, xid XID, flags XAFlags) error
func XAPrepare(ctx context.Context, conn *sql.Conn, xid XID) (XAVote, error)
func XACommit(ctx context.Context, conn *sql.Conn, xid XID, onePhase bool) error
func XARollback(ctx context.Context, conn *sql.Conn, xid XID) error
func XARecover(ctx context.Context, conn *sql.Conn) ([]XID, error)
func XAForget(ctx context.Context, conn *sql.Conn, xid XID) error
```

XAVote distinguishes prepared/read-only; return errors separately. Paths without read-only feedback must not fabricate that vote. XAFlags accepts only stage-supported XA standard values and evidenced Oracle extensions (architecture §16.2.1). Copy input/output XID byte slices by value so caller mutation cannot alter registered branch identity.

- [x] Test `TestXAState`, `TestXARecover`, `TestXAUnknownCommit`: no mixing with sql.Tx, invalid flags/lengths, join/resume/suspend, 1PC/2PC, prepared branches survive local-object closure without rollback; no replay for unknown outcomes.
- [x] Run `go test . -run 'TestXA' -count=1` and confirm failure.
- [x] Fix flags/return codes and Recover XID lists through external-call tests before implementing native XA/DBMS_XA; connections with unknown branch state cannot return to pools.
- [ ] Pass the same command; run `TestXATransactions`, `TestXARecoveryAfterDisconnect` with recovery from new connections in both modes; state actual Forget/timeout support without fabricated success.

D31 local implementation: xa.go / xa_protocol.go added; native OUT reuses internal calls while retaining send-stage evidence. Local protocol tests in both modes, four-target integration source, and examples are added; see architecture §16.2.1 for contracts. Forget/RM timeouts are explicitly unsupported. D30 platform dependencies remain incomplete; preserve overall dependency/real-server gaps in progress rather than substituting local tests for acceptance.

### D32 Per-Option Configuration Migration, Full Acceptance, and Delivery

Dependencies: D01–D31. Coverage: M01–M16, LOCAL INFILE, Java ecosystem mappings; architecture §19–§21.

Files: complete `docs/go-driver-options-mapping.md`, `docs/go-driver-compatibility-matrix.md`, `README.md`, `examples/`, `.github/workflows/ci.yml`, `tests/integration/`, and benchmarks.

Deliver: Go configuration/standard-library alternatives/platform adaptations/Java-only API purposes for each Java option; full verification lists for both modes, Go 1.26/platform CI, runnable examples, benchmarks, and resource-stability evidence. Maintain configuration tables as features arrive from D01; CI starts in D01 and this task extends/checks the full matrix rather than introducing it. Check all required D01–D31 entries against progress §5; do not reduce targets to obtain a green final result.

- [ ] Compare pinned Java DefaultOptions, registration entry points, and tests item by item: every option has a category, owning task, actual Go field/alternative, default differences, and tests; renaming alone is not feature completion.
- [ ] Execute §5 toolchain/platform checks, full integration, fault cases, and targeted fuzzing; explicitly require environments so SKIP cannot impersonate a complete pass.
- [ ] Compile/run standard query/transaction, preparation, procedure, multiple-result, batch, LOB/complex-type, cursor, and XA examples; document restrictions/evidence for inapplicable OB20/HA/authentication combinations.
- [ ] Collect correctness-checked CRUD/prepare/cache/batch/LOB/fetch benchmarks and long-running resource curves; mark G6 complete only after confirming all M01–M16/options are covered. Release/commit/push only on a separate user request.

Initial D32 additions: Config.Interactive, ConnectionAttributes, and shared handshake/ChangeUser encoding/boundary validation; see architecture §6.1. The migration inventory reflects this sub-item; retain D28 platform gaps, remaining options, examples, benchmarks, and full acceptance, without marking D32 complete early.

D32 collation sub-item adds Session.Collation/DSN collation. MySQL uses one SET NAMES/COLLATE to retain the encoding baseline; Oracle explicitly rejects it. Initialization recovery/ChangeUser share the original path; see architecture §6.2. Local positive/negative cases in both modes and TestSessionCollation integration source are added; integration only compiles.

D32 proxy-login sub-item adds Config.ProxyUser/Credentials.ProxyUser/DSN proxyUser without parsing bracketed usernames. Strict encoding, mode/capability rejection, ChangeUser clearing/pool isolation, CancelQuery dual-identity comparison, and external offline examples appear in architecture §6.3. TestProxyUserSessions is in manual CI; real identities/privileges remain pending integration. This does not close platform dependencies or remaining D32 scope.

D32 delivery entry points add README, [Usage guide](go-driver-usage.md), and `examples/usage`, covering standard queries/transactions, preparation, procedure OUT/INOUT, multiple results, batches, table BLOBs in both modes, Oracle locators/OBJECT/ARRAY, and forward cursors; XA retains its separate example. Examples depend only on the public root package/standard library and require explicit modes/cleanable test schemas. They only compile currently; network steps have not run. Example execution, full benchmarks, and overall delivery remain unchecked.

D32 initial-database-creation sub-item adds Config.CreateDatabaseIfMissing / DSN createDatabaseIfMissing, disabled by default. Only MySQL new physical connections defer handshake database selection, creating/selecting after authentication before InitSQL. Oracle explicitly rejects it; ChangeUser/CancelQuery clear this option in derived configuration. Failures neither retry nor delete databases possibly created; see architecture §6.4. Dedicated TestCreateDatabaseSessions fixtures only compile without network steps; overall D32 remains in progress.

D32 network-read-budget sub-item adds Config.ReadTimeout / DSN readTimeout, default zero/disabled. Both modes reuse independent underlying net.Conn read limits, compatible with TLS, three packet streams, Rows, and batch pipelines. Earlier context deadlines prevail; post-write timeouts never replay. See architecture §12.5.1. Four-target TestReadTimeoutSessions entry points only compile; real slow SQL/proxy buffering/post-disconnect execution states remain pending integration; overall D32 remains in progress.

D32 TCP configuration checks seven Java options using existing DialContext and standard net.Dialer/net.TCPConn, retaining one public Config/default behavior without duplicate TCP options/same-name DSN switches. Compilable examples and actual local TCP-peer protocol/cancellation/authentication-gate/explicit-close tests are added; default differences/boundaries appear in architecture §9.0.2. Remote networking/database deployment is unverified; overall D32 remains in progress.

D32 TLS configuration checks 12 Java options using native TLSConfig/crypto/tls/crypto/x509. Applications manage certificate files/Java store unlocking/conversion; no additional driver-configuration layer. Mutual-authentication/file examples and local tests cover both modes/TLS1.2/1.3, callbacks/rejection/cancellation/snapshots; see architecture §17.2.1 for defaults/boundaries. Real-database certificate deployment/identity mapping are unverified; overall D32 remains in progress.

D32 local-performance sub-item adds 16 baseline `BenchmarkDriver` scenarios for CRUD, preparation, caching, batching, LOB, and fetch in both modes, plus MySQL batch rewriting/Oracle native array binding for 18 total; ordinary unit tests share result/protocol assertions. See [Local benchmarks](go-driver-benchmarks.md) for scope/timing/reproduction commands/short samples on both versions. Peer overhead is included; real-database performance/long-term resource curves remain unverified, and D32 stays unchecked.

D32 offline-resource sub-item adds `TestDriverResourceStability`, reusing 18 scenarios for eight fixed rounds with configurable iterations per scenario. After each complete cleanup it checks original-session leases/transactions/responses/resources/cache bounds, pool/physical closure, then records process heap/goroutines after GC. Ordinary tests use small workloads; manual `stability` CI independently supports extended Go1.26/Linux and Windows runs. This serial net.Pipe evidence cannot replace real handles, multi-connection/error combinations, or long-term database acceptance; see local benchmarks §6.

D32 same-session-resource sub-item adds `TestDriverSessionStability`: one persistent physical connection per combination of both modes/three packet streams, mixing cached preparation/type reuse, early Rows close, alternating standard commits/rollbacks, and handle release/Ping after recoverable SQL errors. Check original sessions/pools directly each round without hiding residual state through reacquisition/reset. Default eight rounds per combination; manual `stability` extends runs and samples resources; see local benchmarks §7. Full concurrency/fault/advanced-capability and long-term production stability remain pending acceptance.

D32 shared-pool concurrent-resource sub-item adds `TestDriverConcurrentSessionStability`: three pinned physical sessions in one pool per mode/stream combination, running the mixed workload above with distinct IDs/values. A protocol barrier on each sampling segment's first round delays the first response until all three execution requests arrive. Check original sessions each round and real pool counts per segment; finally reacquire all original connections simultaneously, Ping, and close pool/physical sessions. Default eight rounds per connection; manual `stability` also runs 1024 rounds each. See local benchmarks §8 for scope/reproduction. This adds no completed-feature counts and cannot replace full concurrency/fault/advanced-resource, real-platform, or database acceptance.

D32 configuration-entry sub-item checks initial values of 160 canonical fields in pinned Java, ten historical aliases, DBNAME/port/driver-selection markers, and built-in-plugin-only parameters, adding field-source/initialization interactions. TestDSNJava* covers Go configuration-stage rejection/redaction, standard sql.Open, units/zero values, and literal markers. Preserve existing Go APIs without a Java-parameter translation layer. See architecture §6.5/configuration mapping for default differences; per-option runtime behavior, platform dependencies, and full acceptance remain open, without checking overall completion.

D32 credential-source/RSA-file configuration uses existing Credentials callbacks, Auth.RSAKey, and standard-library parsing, adding application examples, input rejection, rotation within one Connector, both-mode initial/switch/ChangeUser decryption, and snapshots; no new public API/configuration layer. See architecture §17.2.2 for boundaries. Real plugins/public-key distribution/source services remain unverified; overall D28/D32 status is unchanged.

D32 autocommit/read-only configuration checks four options using standard sql.TxOptions.ReadOnly, pinned sql.Conn, and Routing.Role; default differences, Oracle isolation, and manual-transaction boundaries appear in architecture §12.3.1. Tests add both-mode error propagation, read/write restoration after read-only use, initialization rejection, pool-reuse rollback ordering, no role fallback on failures, and external standard-API examples. No production configuration/packages/dependencies are added; real privileges, snapshot isolation, and HA remain pending integration, and D32 is not prematurely complete.

D32 result-column origins use existing Execute.Snapshot, adding ResultColumn/ResultInfo.Columns while retaining standard Rows labels. Local tests cover both modes, GBK, binary/combined execution, multiple results, error prefixes, independent concurrent snapshots, and cumulative budgets. Three Java metadata options map per architecture §16.1.2 without new configuration/packages/dependencies. TestMetadataCatalog actual-origin checks only compile; real versions/proxies remain pending integration and D32 counts are unchanged.

D32 SOCKS5 uses existing DialContext and x/net/proxy ContextDialer. Application examples/local tests cover both modes/three streams/remote DNS/proxy authentication/cancellation/routing/TLS. No production API/configuration is added; only examples/tests import x/net v0.58.0, with module selection also raising x/sys/x/text. See architecture §9.0.3 for contracts. Real proxy deployment/performance are unverified; overall D28/D32 status remains unchanged.

D32 connectProxy adaptation uses Session.SkipVersionProbe / skipVersionProbe to skip only the existing optional version probe. Required initialization, mode, and unknown-version gates remain; default probe failures never downgrade automatically. Tests cover both modes/three streams/reuse/ChangeUser/cancellation auxiliary connections/capability rejection. Four-target TestVersionProbeSessions source only compiles; architecture §9.0.4 records semantic differences; overall D28/D32 status is unchanged.

D32 standard-pool sub-item maps eight Java pool options to native sql.DB setters/context/Stats and external slog, adding one-time multi-connection readiness examples and actual driver-lifecycle verification in both modes. minPoolSize has no equivalent minimum guarantee; staticGlobal's server-global-variable cache does not become a configuration switch. See architecture §5.1 for defaults/shared-Connector boundaries. No production APIs/packages/dependencies/pool are added; real capacities/idle timeouts/monitoring/long-term loads remain pending acceptance; D28/D32 and D30/D31 dependency states remain unchanged.

D06/D32 adds TestPoolCancelIsolation: in both modes/three packet streams, cancel streaming Rows while another physical connection in the same pool executes a prepared transaction. Verify only the target receives the error/physical close, the original transaction can commit, and its original session can Ping, with no redial/pool leak/test-cleanup-assisted closure. This local combination does not replace all concurrency/fault, real-database, or successful race acceptance.

D32 initialization/execution-option evidence follows pinned Java: current usePipelineAuth post-authentication initialization calls getResult for each command, so Go retains individual acknowledgments/close-on-failure without claiming pipeline optimization. Per-call contexts replace enableQueryTimeouts; encloseParamInParentheses corresponds to current space boundaries; Endpoints/Weight/Strategy express serverAffinityOrder replacement lists, which cannot map only to Ordered. Add TestInitializationErrorBoundary, TestClientParameterBoundaries, TestStatementContextDeadline, and same-name Java DSN rejection tests, without production configuration/API/dependency changes. Array-encoding fuzzing joins 30-second CI and the original FuzzStmt selector is fixed to avoid multiple-target ambiguity. Real SQL/HA/platform/remote-CI acceptance remains open.

D32 per-option evidence classifies all 160 canonical options as Go configuration, standard library, explicit API, fixed semantics, safe rejection, application responsibility, platform adaptation, or inapplicable, linking concrete local tests. Example compilation, old-parameter rejection, and query plans do not count as full behavioral acceptance. TestDSNJavaCanonicalOptions independently fixes 158 rejected query keys/two same-name Go entry points. TestMySQLScalarCompatibility adds nine TINYINT/YEAR/BIT execution/packet-stream paths, retaining Go integer/byte contracts. TestMetadataCatalogAndRemarks checks that empty Catalog adds no implicit schema restriction, nonempty values produce exact-filter requests, table/column remarks retain content, repeated queries work, and same-session boundaries hold across both modes/two charsets/three streams. Fixed dictionary row sets do not prove real SQL evaluation. TestMetadataCatalog DDL remarks/Catalog-filter source only compiles; platforms, remote CI, successful race, and real targets remain open. Neither complete per-option verification nor overall D32 completion is checked.

## 7. First Development Batch

The next code-development session starts with D01, then D02, D03, D04, D05; each task can be verified independently. D01 delivers testable configuration rather than empty files for every interface; D02 delivers real fault-injectable packet streams; D04 adds actual connections; D05 enables standard text execution in both modes.

Finishing this batch means only G1 is complete. Progress retains all G2–G6 features; “connected/queryable” must not be described as a completed full-featured driver.

## 8. Feature Coverage and Ongoing Maintenance

D32 provides an [Independent database acceptance tool](go-driver-test-tool.md) as the central execution entry point. Configuration manages targets/fixtures; the program prepares/cleans data automatically and records scenario reports. It supplements existing protocol regressions/integration source; only offline verification runs in this phase and actual database reports remain pending acceptance evidence.

D11/D32 count-acceptance entry supplement: standalone counts-matched/counts-changed fix count policies and use self-created data to verify standard Exec, Execute summaries, and single-item Sequential batches for INSERT, unchanged/changed/unmatched UPDATE, DELETE/empty DELETE. MySQL also checks three upsert branches; Oracle does not emulate ChangedRows. TestRowsAffectedWorkflows uses the public driver/in-memory protocol peers for capability bits, known counts/overflow/errors/cleanup, without database connections. It does not replace real no-op/upsert acceptance or existing multi-item batch/binding cases.

D13/D32 parameter-description acceptance supplement: standalone metadata-parameters uses self-created tables to check StatementParameters for positional parameters, repeated Oracle named parameters, and parameterless Query. Verify original seed values after each description, confirm final row counts, and clean up. See architecture §16.1.1 for fixed output schema/identity/unknown direction/description contracts; server parameter descriptions are not business-column definitions. TestParameterMetadataWorkflow substitutes public Metadata results to verify tool orchestration/assertions/cleanup; existing driver-protocol tests cover underlying prepare/close. Real server parameter inference remains pending integration.

D05/D11/D32 generated-key acceptance supplement: standalone generated-keys creates an unsigned BIGINT autoincrement table only in MySQL compatibility mode. Verify ordinary LastInsertId, full uint64 Execute summaries, >int64 errors, no stale ID after subsequent UPDATE, and final four rows/cleanup, without assuming contiguous IDs. Standard overflow results cannot recover the full raw ID, so do not fabricate exact comparisons; Execute covers that part. TestGeneratedKeyWorkflows checks 31 public-driver/in-memory-peer workflows, including cleanup through reconnection after invalid data evicts a connection. Real autoincrement ranges/allocation/SQL behavior remain pending integration; Oracle returned columns retain the returning scenario.

D05/D22/D24/D32 error-semantics correction: standalone CRUD, optimized execution, ordinary batches, and native array binding accept only mode-specific duplicate-key codes (MySQL 1062 / Oracle 1) for self-created primary keys. Batches check aggregate errors, failed-input errors, completed counts, and stop/continue states. TestDuplicateKeyWorkflows/TestBatchDuplicateAssertions verify tool assertions/failure cleanup; related integration execution-strategy/batch assertions are corrected too. Real SQL/native strategies remain pending existing-target integration.

Tool metadata compares dictionary contents of self-created tables, not merely row counts: catalog/schema/table identity, ID/V names/positions/types, INTEGER precision/scale, VARCHAR length, NULL, primary keys, and both ColumnTypes, preserving exact-schema filtering/LIKE escaping. Wrong/missing/extra content and read/close errors must fail and clean objects. Ninety offline workflows prove assertions/orchestration only, not server-dictionary acceptance.

Single-row tool cursor batches must verify complete counts/final errors: after matching the first row, check for extra rows and Rows.Err while propagating close errors. Forward/static/refresh/sensitive cursors and procedure REF CURSOR outputs reuse this assertion. Twenty-two offline workflows cover both column shapes, incorrect content/counts/read/close/failure cleanup; real fetch semantics remain pending integration.

Tool routines/routines-client failure atomicity must confirm that the created routine actually raised its explicit exception: require structured MySQL1644/Oracle20001 codes and unchanged OUT sentinels. Syntax/privilege/transport errors cannot count as success. Sixty-eight offline workflows verify complete routine orchestration, both modes/two scenario selections, parameter directions/initial values, wrapped errors, and reverse cleanup. Integration assertions are tightened too but only compile; see tool documentation for code sources. Check every branch of joined errors: routines, cancellation, and duplicate keys share strict code matching. An expected exception accompanied by cleanup/timeout/other server errors does not pass. Integration reuses the rule; 84 additional offline error-classification cases do not connect to databases.

Tool mysql-scalars centralizes MySQL-specific scalar acceptance: automatically prepare three boundary-value groups/all-NULL rows; text/parameterized reads verify TIME/YEAR, full integer ranges, BIT, empty/zero-containing binary values, and fixed-length padding, along with column types/ScanType/binary byte lengths. Distinguish zero/empty/NULL, reject missing/extra rows, and close results/clean tables after failure. Offline regressions prove orchestration only; real types/SQL behavior remain pending integration.

Tool oracle-scalars centralizes Oracle-specific scalars: automatic table creation, populated/NULL rows, text/parameterized reads, and ROWID rebinding for lookup. Verify public NString, TimestampTZ/LTZ, IntervalYM/DS binding, Unicode, original TZ offsets/nanoseconds, LTZ equivalent instants, and typed NULL; use server prepare/UTF-16 national charset while preserving configured location. Oracle compatibility mode only; offline regression does not replace server acceptance.

Tool scalars date/time assertions compare complete wall time/nanoseconds; string prefixes must not hide Oracle DATE time changes or extra nonzero fractions. Equivalent trailing zeros are accepted; fractions beyond nine digits must not pass after Go parsing truncates them, and single-digit hours cannot bypass fractional limits. TestScalarsTemporalAssertions supplies 32 offline workflows across both modes/two query paths and verifies self-created table cleanup after failure; real round trips remain pending acceptance.

Tool complex OBJECT OUT checks field values, attribute order, and schema, not just type names/field counts. Distinct NUMBER/RAW values at collection ends are checked position by position, avoiding duplicate seeds that hide reordering. TestComplexOutputAssertions/TestComplexCollectionAssertions provide 20 offline workflows including transaction rollback/reverse dependent-object cleanup; real type descriptions/database round trips remain pending acceptance.

Existing integration complex-type cases adopt the same content/position checks. OBJECT DATE checks complete whole seconds, accepting the driver's six zero microseconds. Seven offline assertion regressions do not connect to databases; original database workflows only compile.

| Java module/capability | Implementation tasks | Required additional verification |
| --- | --- | --- |
| M01 Entry points/unified API | D01, D04, D05 | D32 external-call examples/toolchains |
| M02 URL/Options/address descriptions | D01, D26, D27 | D32 per-option mapping |
| M03 Transport/TLS/all authentication | D02–D04, D23, D28 | Credential gates at every stage, real transports on each platform |
| M04 Modes/capabilities | D03, D04, D23, D24 | Atomic pool-mode pinning, version/capability combinations |
| M05 Sessions/local transactions | D06, D09, D30 | Cancellation/unknown outcomes/identity and generations |
| M06 Ordinary/both prepared execution strategies | D05, D08, D10, D24 | Path equivalence, cache/reset, no replay |
| M07 Parameters/batches/long data | D07, D14, D21, D22 | Partial success/errors/OUT and transaction equivalence |
| M08 Results/counts/generated keys/cursors | D05, D11, D14, D19, D20 | Mixed ordering, error prefixes, output lifetimes |
| M09 Types/character sets/time | D07, D08, D12, D18 | All-type multi-path round trips, NULL/precision |
| M10 LOBs/complex types | D15–D18, D20, D21 | Original-transaction visibility/locks/oversized objects/invalidation |
| M11 All metadata | D13, D18 | Actual dictionaries, privileges, parameter metadata |
| M12 HA/discovery/topology | D26, D27 | New-connection failover, consistent roles/logical services |
| M13 Pooling/reuse | D06, D09, D15, D30 | Standard-pool load, reset, handle cleanup |
| M14 XA | D31 | Both-mode 1PC/2PC/flags/recovery |
| M15 Logging/diagnostics/OB20/FLT | D01, D23–D25 | Silent nil, redaction, trace isolation |
| M16 Build/platform/performance | D01, D28, D32 | Go1.26 and each supported platform |
| LOCAL INFILE | D29 | Controlled Readers, failures, and transactions |
| JDBC scroll/updatable | D19 | Separate acceptance for static/sensitive/update positioning |
| Java OSGi/JMX/serialization/legacy class names | D01, D25, D32 | Go module/DB.Stats/explicit value-contract mappings; no Java runtime |

Progress documentation retains current task status, verification combinations, and next steps; code/tests and ordinary documentation retain change rationale, without separate walkthrough reports. When adding/splitting tasks, update this table, dependencies, and the progress denominator together; never raise completion rates by deleting unfinished items.
