# OceanBase Go Driver Architecture Design

Status: architecture and interface contracts. Refer to development progress for the local implementation and acceptance status of each item. This document targets complete feature adaptation; basic CRUD is not the final scope.

## 1. Goals, Confirmed Requirements, and Design Baseline

Confirmed requirements:

1. Target OceanBase with unified support for Oracle and MySQL compatibility modes.
2. Account for the Java driver's complete functionality in the Go architecture, explaining the implementation approach and verification conditions for every capability.
3. Follow Go conventions, keep ordinary application APIs simple, and avoid reproducing the JDBC class hierarchy.
4. The minimum Go version is **1.26**; this design does not depend on interfaces introduced in Go 1.27.
5. Use the caller-provided `*slog.Logger`; do not use the global default logger.
6. Keep design documents in this repository's `docs` directory.

Reference baseline:

- [Java Source Structure and Feature Outline](obconnector-j-architecture-outline.md).
- See the [Development Plan](go-driver-development-plan.md) for implementation tasks. Maintain task status and verification evidence in [Development Progress](go-driver-development-progress.md).
- Java source: [oceanbase/obconnector-j](https://github.com/oceanbase/obconnector-j), version `2.4.14`, commit `6adc311ba5db8b2f19f98f3e6e4734667fdfdda1`.
- Standard library source reference: Go `1.27.2`. The `database/sql` and `database/sql/driver` interface contracts have been reviewed; the selected interfaces already exist in Go 1.26. Compilation and testing must use a genuine Go 1.26 toolchain.
- Historical Go API lists have also been checked: `RowsNextResultSet` dates from Go 1.8, `sql.Out` / `NamedValueChecker` from Go 1.9, and `slog` / `context.AfterFunc` from Go 1.21. None of these core mechanisms requires Go 1.26 or 1.27.
- The API names, package layout, default policies, and resource limits below are proposals in this design, not published compatibility commitments.

“Complete functionality” means adapting observable capabilities: ordinary execution, both preparation strategies, named binding, procedures/functions, OUT/INOUT, RETURNING, batching, cursors and updatable results, large objects, complex types, metadata, local and XA transactions, address discovery, failover, secure authentication, OB20, tracing, and diagnostics. Java OSGi, JMX, legacy JDBC class names, and Java object serialization should map to corresponding Go mechanisms rather than become APIs that Go users must learn.

Capabilities depend on the OceanBase/OBProxy version and negotiation results. Including a capability in the design does not mean every version and mode can execute it. Unsupported cases must return a clear error or use an implementation that preserves semantics.

## 2. Overall Design

Use **the standard `database/sql` entry point + one public `oceanbase` package + a shared session/execution core + explicit mode handling**.

| Decision | Design |
| --- | --- |
| Ordinary calls | `sql.DB`, `sql.Conn`, `sql.Tx`, `sql.Stmt`, `sql.Rows` |
| Connection pool | Managed by `database/sql`; the driver does not implement another pool |
| Physical connection | One underlying socket represents one physical session; do not switch sockets during execution |
| Compatibility mode | Identify it from the authentication response; configured mode is only an expected-mode constraint |
| Extensions | Use standard interfaces wherever they can express the capability; otherwise expose small functions or concrete types |
| Extended types | Use `NamedValueChecker` for input; use `Scanner` and standard values for ordinary reads |
| Go 1.26 compatibility | No dependency on `RowsColumnScanner` or `sql.ConvertAssign`; no standard library internal symbols or `go:linkname` |
| Logging | Each Connector holds the caller's logger; nil disables logging |
| Retries | New connections may try another candidate address; never automatically replay a business operation that has been sent |
| Implementation | Prefer pure Go and the standard library; use narrowly scoped dependencies where character sets or platform authentication require them |

Do not implement the overall design as a single entry point that merely forwards to two existing database drivers. The Java source shows that Oracle compatibility mode involves OceanBase-specific commands, types, LOBs, and cursor extensions, requiring a shared protocol implementation controlled by this driver.[J01][J02]

```mermaid
flowchart TD
    APP[Application code] --> SQL[database/sql]
    APP --> EXT[Batch / LOB / Cursor / Metadata / XA extensions]
    EXT --> SQL
    SQL --> ADAPTER[Driver / Connector / Conn / Stmt / Rows / Tx]
    ADAPTER --> CORE[Session and execution core]
    CORE --> MODE[Oracle / MySQL compatibility mode handling]
    CORE --> WIRE[Commands, responses, standard packet stream / compression / OB20]
    CORE --> ROUTE[Address selection and discovery during connection establishment]
    WIRE --> NET[net.Conn / TLS / authentication]
    NET --> OB[OceanBase / OBProxy]
    CORE -. Structured diagnostics .-> LOG[Caller-provided slog.Logger]
```

The extension layer reuses the same core, connections, and error rules; it does not introduce separate connection management.

## 3. Constraints Beyond the Java Skeleton

| Source fact | Required Go architecture response |
| --- | --- |
| Authentication OK status bits identify Oracle compatibility mode | `Config.Mode` does not force another interpreter; validate the actual mode after connecting.[J01] |
| The same type codes have two interpretations | Decoding context must include the actual mode; type code alone is insufficient.[J03] |
| Java receives ordinary, callable OUT, cursor, and multiple-result responses | Response parsing is a complete state machine; do not return the connection after reading only the first result.[J04] |
| Java COM_RESET_CONNECTION clears the prepare cache | Go session reset must also invalidate statement IDs and prepare statements again.[J05] |
| Some Java LOB paths open another connection to avoid active streaming results | Go does not copy implicit auxiliary connections; large objects use explicit session ownership and cursor batch boundaries.[J06] |
| Java considers some SELECT statements replayable | Go does not infer safety from the first SQL word; SELECT statements with functions, side effects, or locking semantics cannot safely be replayed on that basis.[J07] |
| Java PrepareExecute and offset fetch have additional version requirements | Gate features using mode, options, version, and capabilities; one `isOracle` boolean cannot represent every capability.[J08] |
| Go `ErrBadConn` triggers standard library retries | When a business operation might have executed, return its original error and discard the connection through Validator.[G01][G02] |
| Go Rows and Scanner primarily exchange standard values | Complex values require explicit lossless representations; connection-bound handles use separate extension entry points.[G03][G04] |

## 4. Package and File Organization

Start with one public package and organize responsibilities by Go file. Only protocol boundaries and independently reusable `internal` subsystems belong in `internal`.

```text
go-oceanbase/
├── go.mod                         go 1.26
├── driver.go / connector.go        Registration, configuration snapshots, connection entry points
├── config.go / dsn.go              Go configuration and connection strings
├── conn.go / session.go            Physical sessions, lifecycle, operation state
├── mode.go / capabilities.go       Actual mode, versions, effective capabilities
├── stmt.go / execute.go            Ordinary execution and both preparation strategies
├── bind.go / bind_oracle.go         Parameter plans, named binding, type handling
├── rows.go / result.go             Standard Rows, multiple results, counts, generated keys
├── codec.go / codec_mysql.go       Shared value conversions and MySQL compatibility mode
├── codec_oracle.go / types.go      Oracle compatibility mode and public extension values
├── tx.go / savepoint.go / xa.go    Local transactions, savepoints, XA
├── batch.go / call.go              Batching, procedures, output parameters
├── lob.go / cursor.go              Connection-bound extension resources
├── metadata.go / metadata_sql_*.go Database structure and type dictionaries
├── error.go / logging.go           Errors, injected logging, diagnostics
├── trace.go / local_infile.go      Request tracing, local data import
├── internal/
│   ├── wire/                      Packet boundaries, buffers, command/response foundations, OB20
│   ├── auth/                      Authentication algorithms and their phase state
│   ├── routing/                   Addresses, TNS/OCP, load balancing, blacklist
│   └── charset/                   Character encoding and collation mapping
├── examples/                      Standard and extension usage
└── docs/                          Architecture, capability matrix, protocol evidence, API documentation
```

These filenames describe responsibilities; they do not require creating every empty file at once. `wire` does not depend on `database/sql` or public business types; `routing` does not execute user SQL. Mode handling stays in the same Go package, avoiding a cross-package interface layer for two built-in modes.

Allowed production dependencies are fixed as `oceanbase → internal/{wire,auth,routing,charset}`. These four internal packages must not import the root package and initially must not import one another. They may depend on the standard library and necessary algorithm/platform libraries confirmed by their tasks. The root package owns composition, handshake ordering, authentication security gates, session execution, mode, and policy. Internal packages accept small inputs of their own types and return their own results; they do not receive an entire Config/Conn or introduce common/core/interfaces packages for shared types.

| Type/behavior | Ownership and conversion boundary |
| --- | --- |
| Config, Mode, public credential callbacks, authentication factory/plugin interfaces | Root package; public signatures use only public or standard library types and never expose internal types |
| Algorithm inputs, challenge/response, algorithm phase state | auth-owned types; the root package adapts public plugins and built-in algorithms and retains controlled sending and TLS gates; auth never operates wire/sockets |
| Packet headers, basic handshake fields, command/response structures | wire-owned types; the root package converts fields into actual Mode, Capabilities, and errors |
| Addresses, discovery results, load selection state | routing-owned types; the root package supplies necessary configuration values and validates mode/role policies without passing an entire Config |
| Encoders and collation data | charset-owned types; root-package codecs own database type and value semantics |

The test helper `internal/testutil` depends only on the standard library and simulates peers using net.Conn, bytes, and context. It must not import the root package or the internal packages above, and production code must not import it. Package tests may depend on testutil in one direction; external API and integration tests may import the root package. From D01, CI uses `go list -deps ./...` to detect actual cycles. D02 adds the external root-package test `architecture_test.go`, parsing `go list -deps -json -test -tags=integration ./...` to validate production and test import edges and reject reverse/lateral dependencies. New internal packages or dependency edges must first update this section and the test rules; temporarily successful compilation is insufficient.

Use concrete functions and small `switch` statements for mode dispatch, such as `initSession`, `encodeParameter`, `decodeColumn`, and `metadataSQL`. With only two implementations and fixed call sites, do not introduce DialectFactory, ProtocolFactory, ServiceContainer, or similar structures upfront.

## 5. Entry Points for Ordinary Users

Retain the connection string entry point:

```go
db, err := sql.Open("oceanbase", "oceanbase://app:secret@127.0.0.1:2881/demo?mode=auto")
```

For logging, TLS, or external credentials, prefer programmatic configuration:

```go
// oceanbase is imported using the project's final module path; the symbols below are design APIs.
logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

connector, err := oceanbase.NewConnector(oceanbase.Config{
    Addr:     "127.0.0.1:2881",
    User:     "app@tenant#cluster",
    Password: password,
    Database: "demo",
    Mode:     oceanbase.ModeAuto,
    Logger:   logger,
})
if err != nil {
    return err
}
db := sql.OpenDB(connector)
defer db.Close()

db.SetMaxOpenConns(20)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(30 * time.Minute)
if err := db.PingContext(ctx); err != nil {
    return err
}
```

This is a usage fragment, not an already runnable package. `NewConnector` validates configuration and creates a snapshot without connecting to the network; the actual connection is established in `Connect`. Pass usernames in the form required by OceanBase/OBProxy without independently splitting or rewriting tenant/cluster semantics.

Keep the main entry points as:

```go
func ParseDSN(dsn string) (Config, error)
func NewConnector(cfg Config) (driver.Connector, error)
```

Do not add another DB, Connection, Transaction, or Pool wrapper. Use `sql.OpenDB` directly to avoid maintaining constructors and connection pool configuration already supplied by the Go standard library.

### 5.1 D32 Standard Connection Pool and Lifecycle

`sql.Open` / `sql.OpenDB` return a concurrency-safe `*sql.DB` with lazy connection establishment; applications should reuse it long term and close it on shutdown. `sql.Conn.Close` returns a lease; `sql.DB.Close` closes the pool. Creating/closing a DB per request must not emulate a Java Connection. Mode binding belongs to the Connector. Create separate Connectors and DBs for independent modes, identities, configurations, or lifecycles, without shared mutable configuration. Multiple DBs created from one Connector share its mode lock, routing/discovery, and closed state. Closing any DB closes that Connector, preventing the others from creating further physical sessions; do not reuse a Connector for independent pools.

| Capability | Native Go contract and default differences |
| --- | --- |
| Maximum connections | `SetMaxOpenConns(n)` limits physical connections; `n<=0` is unlimited, and the Go default is 0; Java maxPoolSize defaults to 8. At the limit, borrowers wait under the operation context; no extra connection is created and no SQL is replayed |
| Idle connections | `SetMaxIdleConns(n)` is an upper bound, defaulting to 2 in Go; `n<=0` retains no idle sessions, and positive values are capped by MaxOpenConns. It is not the guaranteed minimum of Java minPoolSize and performs no proactive warmup |
| One-time readiness | Applications may hold N sql.Conn leases simultaneously, Ping each, then return them together; see the [example](../pool_example_test.go). This checks multiple physical sessions; repeated DB.Ping calls may reuse one session. The example helper checks that N does not exceed the current connection maximum and must run with a timeout during startup while pool configuration is stable. Failure/cancellation releases acquired leases; healthy connections may remain pooled, and completed connection establishment is not rolled back |
| Idle time and lifetime | `SetConnMaxIdleTime` / `SetConnMaxLifetime` use durations; 0 disables either by default. Java maxIdleTime defaults to 600 seconds with a 60-second minimum. The standard library triggers expiry cleanup; it is not an exact timer. Held Conn/Tx/Rows are not interrupted when lifetime expires; connections are discarded/replaced on return or next checkout |
| Validation | `PingContext` explicitly contacts the server; Validator checks only local session validity. A positive Routing.ValidationInterval sends PING in ResetSession when a reused session has been idle long enough since its last completed operation; 0 disables this. Java poolValidMinDelay defaults to 1000 milliseconds and validates on every checkout at 0, so zero-value semantics differ. No Java background validation pool is copied |
| Session baseline and cache | Every new physical session receives required initialization. Managed reset restores only tracked transaction/autocommit state; it does not clear all session variables changed by arbitrary business SQL. Use ResetFull to discard sessions when full isolation is required. Java staticGlobal defaults to false; true assumes constant server global variables and caches initialization information, rather than enabling process-static configuration. Go does not implement this cross-session global-variable cache or obtain initialization values from other pools |
| Identification and monitoring | Identify pools through application variables or external logger.With("pool", name). db.Stats exposes OpenConnections/InUse/Idle, cumulative WaitCount/WaitDuration, and three closure counters. Applications integrate monitoring; the driver does not automatically register JMX, exporters, or background logging tasks. Logger=nil remains silent |

Local TestPool* tests use the actual driver and database/sql. Protocol peers check initialization in both modes, same-session PING/QUIT, zero sends on cancellation, and physical connection counts. They cover warmup/failure/cancellation/server rejection, limit waits, reuse, idle/lifetime cleanup, and independent Auto-mode pools. Legacy Java pool DSN names are explicitly rejected; no public package, production configuration, or driver pool was added. Real server idle timeouts, capacity, sustained load, and monitoring deployment still require integration validation. The example does not maintain a minimum connection count and does not complete all of D32.

## 6. Configuration Model and Defaults

Keep common `Config` fields at the top level: `Addr`, `User`, `Password`, `Database`, `Mode`, `Logger`, `TLSConfig`, and `ConnectTimeout`. Group advanced options by actual responsibility rather than copying every Java boolean switch.

| Group | Capabilities and proposed defaults |
| --- | --- |
| Connection | `Addr` defaults to `127.0.0.1:2881`; static User or a Credentials callback is required; the total connection establishment budget defaults to 10 seconds, constrained by any earlier calling-context deadline |
| Connection declarations | `Interactive` defaults to false; `ConnectionAttributes` defaults to nil and sends application metadata only when explicitly supplied; see §6.1 |
| Initial database | `CreateDatabaseIfMissing` defaults to false; only new MySQL-mode physical connections may explicitly create and select a nonempty Database; see §6.4 |
| Read budget | `ReadTimeout` defaults to 0, disabling the separate read timeout; a positive value limits each underlying network Read, while any earlier context deadline still applies; see §12.5.1 |
| Mode | `ModeAuto` is the zero value; `ModeMySQL` / `ModeOracle` constrain actual mode validation |
| Transport | `TLSConfig *tls.Config`, `DialContext`; TCP is the default. nil does not proactively enable TLS; non-nil requires successfully verified TLS and forbids downgrade. See §17.2 for plaintext credential restrictions |
| Credentials/authentication | External Credentials callback and per-Connector authentication plugin factories; use static User/Password/ProxyUser without a callback. See §6.3 for proxy login |
| Protocol | `Config.Protocol`: Compression, OB20, Checksum, ProxyChecksum, NewMetadata; the zero value is FeatureAuto, with FeatureOff/FeatureRequired also available; connection negotiation determines the outcome |
| Execution | Automatically select server-side preparation; explicitly support client-side preparation, combined execution, caching, and batch policies; `AllowMultiStatements` defaults to false; `RowsAffectedMode` defaults to `MatchedRows`, see §11.4 |
| Session | Charset, NCHAR charset, Location, session initialization settings, Managed/Full reset policies |
| Resources | Maximum logical message size, LOB materialization limit, FetchSize, `MaxResultItems`, close/cleanup timeout, cache capacity |
| HA | Config.Routing: static addresses, fixed roles, within-group policy, per-attempt/total connection budgets, bounded blacklist; optional ValidationInterval checks liveness before reuse; see §17.1.1 |
| Diagnostics | Slow-request threshold, bounded packet tracing callback, full-link tracing; Logger nilness controls logging |

Proposed initial resource defaults: 64 MiB maximum logical message, 16 MiB automatic materialization per LOB, cursor fetch of 128 rows, `MaxResultItems=1024` result summaries per Execute, and a 1-second cleanup timeout. Limits are configurable. Exceeding a limit returns a clear error rather than truncating and claiming success; large objects should direct callers to streaming entry points. Physical packet size and logical message limits are separate concepts.

Existing authentication configuration interfaces are `Credentials func(context.Context) (Credentials, error)` and `AuthOptions{RSAKey, Plugins}`. Plugins maps protocol plugin names to per-authentication factories returning plugins implementing Security/Respond. RSAKey is a caller-pinned public key copied into the Connector; automatic retrieval is forbidden. Callbacks, plugin factories, and callbacks/private-key objects inside external TLS objects must meet concurrency and context contracts. See the [Compatibility Matrix](go-driver-compatibility-matrix.md) for interface security boundaries.

The initial handshake username and database name selected by CLIENT_CONNECT_WITH_DB are strictly encoded from Go UTF-8 strings into `Session.Charset`, consistently with the handshake collation declaration and ChangeUser. Invalid UTF-8 or unrepresentable characters return a redacted ConfigError and close the connection before creating authentication plugins/sending authentication responses. Without the database-selection bit, Database is neither encoded nor sent in the handshake. Plugins still receive original Go strings in Credentials, as does the authenticated session identity; password encoding is separately controlled by `Auth.PasswordCharset`. Do not depend on the platform-default encoding of Java username getBytes().

Configuration rules:

- DSNs use `net/url` escaping rules. Validate parameter names strictly; reject unknown options and duplicated sensitive options.
- Never silently ignore behavioral parameters such as `mode`; error messages must not contain the complete DSN or password.
- Pass loggers, function callbacks, and TLS objects through Go configuration, without global registries or instance lookup by string name.
- The Connector copies slices/maps and clones TLS configuration; subsequent changes to public cfg do not change an existing Connector. External callbacks and loggers must meet their own concurrency contracts.
- Do not provide a `map[string]any` that forwards arbitrary Java options. Every public option must correspond to an existing feature and explicit semantics.

### 6.1 D32 Implemented Connection Attributes and Interactive Flag

`Config.Interactive` corresponds to Java `interactiveClient`; both default to false. The DSN uses `interactive=true/false`. Enabling it sends CLIENT_INTERACTIVE without requiring the greeting to advertise it first. The actual idle timeout follows server interactive_timeout settings without changing Go contexts or query timeouts.

`Config.ConnectionAttributes map[string]string` is programmatic only and defaults to nil. Keys and values are Go UTF-8 strings, snapshotted and strictly encoded using `Session.Charset` through the same path for handshakes and ChangeUser. Empty values are allowed; empty keys, NUL, invalid UTF-8, unrepresentable characters, and encoded key collisions are rejected. Keys starting with an underscore and `ob_capability_flag` are protocol-reserved and cannot override capability bits or proxy identity. Proxy login uses the separate identity fields in §6.3.

The entire attribute area, including driver declarations, key/value pairs, and all length prefixes, is bounded by `Limits.MaxMessageBytes`. Before encoding, character counts establish minimum required space; after encoding, each item consumes the remaining budget, avoiding false rejection from GBK contraction and unbounded aggregation. The full handshake still obeys the existing message limit. Configuration errors return field names only. If the target lacks CLIENT_CONNECT_ATTRS, explicit nonempty attributes return ErrUnsupported before credentials are requested rather than being silently discarded.

Go does not automatically collect Java's dynamic PID, thread, OS, host, or similar attributes. Java `sendConnectionAttributes` and its default-attribute exclusion list therefore map to caller control of this map, not copied global switches. A nil/empty map sends no application attributes; driver protocol declarations and explicit proxy identity still come from their respective configuration. Attributes are excluded from driver logs and diagnostic callbacks. Transport protection follows `TLSConfig`; with TLS enabled, attributes appear only in the handshake response after successful verification.

Local coverage includes both modes, UTF-8/GBK, extended GB18030 encoding, length-prefix thresholds, configuration snapshots, reserved keys, missing capabilities, TLS, and ChangeUser retransmission over ordinary/compressed/OB20 streams. This does not verify visibility in server attribute tables or the actual effect of interactive_timeout.

### 6.2 D32 Implemented Connection Collation

`SessionOptions.Collation string` / DSN `collation` corresponds to Java `connectionCollation`. Go's zero value is empty and Java defaults to null; neither selects an additional custom `collation`. Nonempty values must first pass original ASCII-identifier validation (maximum 128 bytes), then are lowercased. Whitespace, quotes, separators, NUL, non-ASCII, and invalid UTF-8 are rejected; string concatenation must not carry arbitrary SQL.

MySQL compatibility mode initializes with `SET NAMES <Charset> COLLATE <Collation>`, setting client/connection/results character sets and connection collation in one command. Collation never rewrites the caller's Charset; unspecified Charset still defaults to utf8mb4. The target server atomically validates the name and charset compatibility. Failure preserves the server error and closes the connection without default fallback. Evidence is the mode condition in pinned Java `AbstractConnectProtocol.setCollationVariables` and `NAMES charset_name_or_default opt_collation` in the [OceanBase v4.3.5_CE MySQL grammar](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/sql/parser/sql_parser_mysql_mode.y).

After Session.Variables/InitSQL, the same setting restores the baseline. ChangeUser initialization and new Full reset replacement connections continue applying it; the Managed reset boundary is unchanged. Driver encoding always follows Charset; result decoding follows each column's actual collation ID without guessing encoding from its name.

Java ignores this option in Oracle compatibility mode; Go rejects explicit configuration. With ModeOracle set, NewConnector returns `ConfigError{Field:"Session.Collation"}`; with ModeAuto identified as Oracle after authentication, it returns ErrUnsupported before initialization SQL. This field does not replace Oracle NLS settings.

Local coverage includes configuration/DSN, UTF-8/GBK/utf8mb3 aliases, initialization restoration, ChangeUser, Full reset replacement, ordinary/compressed/OB20 streams, server errors, and Oracle rejection. `ExampleSessionOptions_collation` runs offline. Four-target TestSessionCollation integration source is wired into manual CI: MySQL checks session state, comparisons, prepared Unicode, and baseline reconstruction; Oracle checks configuration rejection. This phase compiles it only and makes no real-effect or version-compatibility commitment.

### 6.3 D32 Implemented Proxy User Login

`Config.ProxyUser string` / DSN `proxyUser` specifies an Oracle-mode proxy target; `User`/`Password` always identify the authenticating account and its password. The empty zero value disables proxy login, corresponding to Java `useProxyUser=false`. A nonempty value directly names the target; the driver does not parse Java's `padmin[admin]@tenant` syntax, fold case, or strip quotes. It generates no proxy authorization SQL; the server confirms target identity and authorization.

`Credentials` adds a `ProxyUser` field. Each credential callback completely replaces User/Password/`ProxyUser`, including clearing a static target with empty `ProxyUser`. Static fields must still pass NewConnector validation; callbacks cannot bypass invalid configuration such as a nonempty static target in ModeMySQL. Construct this public struct with named fields. Existing unkeyed two-field literals must become `Credentials{User: user, Password: password}`; adding the field is not source-compatible with that usage. String snapshots, callback concurrency/context contracts, password rules, and TLS gates remain unchanged.

The target is strictly encoded using Session.Charset into dedicated `__ob_client_proxy_user_name`. NUL, invalid UTF-8, and unrepresentable characters are rejected. It shares the attribute-area budget with application attributes, protocol declarations, and all length prefixes and remains subject to the complete authentication-message limit. Application maps cannot override this key. NewConnector validates static configuration; dynamic credentials are validated before authentication plugin creation/response transmission. With TLS enabled, the target is sent only after successful verification.

A nonempty target without CLIENT_CONNECT_ATTRS returns ErrUnsupported. A static target without a callback is rejected before requesting credentials. With a callback, obtain full credentials first, allowing it to clear the target; reject only if it remains nonempty. Explicit ModeMySQL is rejected during configuration/ChangeUser preflight. If ModeAuto identifies MySQL, return ErrUnsupported and close before initialization SQL. Preserve server rejection errors (including 1235) without retrying ordinary login. Attribute support and authentication OK do not independently prove the proxy target; actual identity still requires target integration validation.

Every ChangeUser call explicitly supplies its proxy target; empty clears it without inheriting the previous target or calling the original credential callback. Success records the authenticating user and target and reinitializes the session. Default Oracle schema still derives from the authenticating username before @, matching pinned Java sendChangeUser; the target does not implicitly replace explicit Database. Existing rules invalidating old handles/CancelTarget, discarding failures, and preventing return to the original pool after switching remain. New pool connections still use the original Connector target.

Besides existing endpoint, authenticating-user, mode, TLS, and connection-ID checks, CancelQuery compares the targets recorded after successful authentication on both connections. If auxiliary credentials return another target or clear it, refuse to send KILL. Rejection changes neither the original query identity nor automatically replays business operations. Usernames/targets are excluded from logging and diagnostics.

Pinned evidence: target separation in `Credential.java`, dedicated attributes in `SendHandshakeResponsePacket.java` (L88, L399), ChangeUser retransmission (L280), and proxy/actual-user assertions in `ConnectionOracleTest.testProxyUser`. `TestProxyUser*` covers configuration, DSN, dynamic replacement/clearing, capability/mode/encoding/budget, TLS, ChangeUser over all three streams, pool isolation, and cancellation identity. External ExampleNewConnector_proxyUser runs offline. TestProxyUserSessions provides Oracle direct/proxy identity, switching/clearing, and new-pool restoration cases; MySQL checks configuration rejection. This phase compiles it only.

### 6.4 D32 Implemented Initial Database Creation

`Config.CreateDatabaseIfMissing` / DSN `createDatabaseIfMissing=true/false` corresponds to Java `createDatabaseIfNotExist`; both default to false. Explicit enabling and nonempty Database are required. Strict Session.Charset encoding rejects NUL, invalid UTF-8, and unrepresentable characters without echoing names in configuration errors. NewConnector rejects explicit ModeOracle. If ModeAuto authenticates as Oracle, return ErrUnsupported and close before initialization SQL, rather than silently ignoring creation as Java does.

When enabled, the initial handshake neither declares CLIENT_CONNECT_WITH_DB nor sends a database name and does not require server support for that bit. After authentication, mode checks, and protocol negotiation, establish charset/autocommit/version/timezone baseline on the same physical connection, send `CREATE DATABASE IF NOT EXISTS` and COM_INIT_DB, then run Session.Variables/InitSQL. Quote identifiers with backticks and double embedded backticks; do not concatenate names as SQL values or unescaped fragments. COM_INIT_DB encodes the original name using connection charset. When disabled, ordinary handshake database selection applies.

Creation and selection share ConnectTimeout/routing attempt budgets and preserve server errors. Failure closes the connection without another address or ordinary-login fallback. DDL may already have taken effect; initialization failure, cancellation, or disconnect never automatically deletes the database. IF NOT EXISTS does not guarantee accounts lacking CREATE privilege can connect to existing databases; server authorization applies. The driver adds no default charset/collation and uses server CREATE defaults.

New physical pool connections and Full reset replacements execute IF NOT EXISTS again; ordinary reuse does not. ChangeUser clears this option in derived configuration and reauthenticates using its database parameter without creating databases. CancelQuery auxiliary connections also clear it and cancel immediately after normal database selection, avoiding creation side effects. These local changes do not alter the original Connector configuration; no packages or global state are added.

Pinned Java evidence: CLIENT_CONNECT_WITH_DB conditions in AbstractConnectProtocol (L337–L342) and additionalData create/USE ordering (L1378–L1386). Local TestCreateDatabase* covers handshake, three framing modes, UTF-8/GBK, quoting, initialization order, mode rejection, server failure/total budget, and ChangeUser/cancellation isolation. TestCreateDatabaseSessions provides MySQL direct/proxy creation, existing-database, InitSQL, and physical-replacement cases requiring dedicated CREATE/DROP DATABASE fixtures. Oracle checks configuration rejection only; this phase performs no live connections.

### 6.5 D32 Java Configuration Migration Boundary

The [Options Mapping](go-driver-options-mapping.md) lists initial values for all 160 canonical fields in pinned Java, separately listing 10 historical aliases, DBNAME/port, driver-selection markers, and plugin parameters. Aliases do not increase canonical counts. Defaults follow actual Options initializers, DefaultOptions.parse, postOptionProcess, and UrlParser branches; descriptions alone cannot establish final behavior.

Go keeps one Config and a strict URL entry point. Historical aliases, Properties, Java stores/class names, and unknown options are not admitted into DSNs. Existing Go configuration, standard library facilities, or application closures carry capabilities; see the mapping for entry points. To override parsed values, ParseDSN, modify Config, then call NewConnector, which revalidates and copies it. There are no competing parameter-merge precedence systems. Query names are case-sensitive; duplicates/empty values are rejected. Booleans accept only true/false, without automatic conversion of Java bare milliseconds or quoted booleans.

Do not copy connectTimeout directly: Java normally defaults to 30000 milliseconds, is affected by DriverManager login timeout, and uses explicit `0` for unlimited time. Go defaults to 10 seconds and uses durations for positive values. Both `0` and `0s` accepted by time.ParseDuration retain the 10-second default rather than disabling total connection budgets. Zero ReadTimeout instead disables the separate read timeout; both obey earlier context deadlines. JDBC disableMariaDbDriver substring rules do not apply to Go password/database contents, but that query key remains rejected.

Local TestDSNJava* covers alias/implicit-entry rejection, ParseDSN/sql.Open configuration errors, redaction, units/zero values, and literal selection markers. No production API, package, or global configuration was added. Pinned evidence supports all 160 defaults, without replacing acceptance of every runtime behavior, platform, or server.

## 7. Standard Library Interface Implementation Table

| Internal type | Required interfaces | Key rules |
| --- | --- | --- |
| driver | `driver.Driver`, `driver.DriverContext` | Register as `oceanbase`; Open/OpenConnector share one parser |
| connector | `driver.Connector` | Concurrency-safe, creates equivalent sessions; never retain Connect context for later queries |
| conn | `driver.Conn`, `ConnPrepareContext`, `ConnBeginTx` | Main paths support context; legacy methods delegate to the same implementation |
| conn | `ExecerContext`, `QueryerContext` | Unified fast execution path; never return `ErrSkip` after sending a command |
| conn | `Pinger`, `SessionResetter`, `Validator` | Real liveness checks, checks before reuse, disposal before return; `IsValid` performs no network I/O |
| conn / stmt | `NamedValueChecker` | Standard values use standard library conversion; explicitly validate extensions instead of always returning nil |
| stmt | `driver.Stmt`, `StmtExecContext`, `StmtQueryContext` | Statements bind to physical sessions; NumInput follows binding plans |
| tx | `driver.Tx` | Legacy Commit/Rollback use bounded network I/O; never assume CommitContext exists |
| rows | `driver.Rows`, `RowsNextResultSet` | Streaming reads, complete multiple-result cleanup, explicit buffer ownership |
| rows | Five `RowsColumnType*` interfaces | Support `Rows.ColumnTypes()`; unknown attributes return `ok=false` |
| result | `driver.Result` | Check int64 overflow; LastInsertId errors without reliable generated IDs |

Go 1.26 provides neither `RowsColumnScanner` nor `sql.ConvertAssign`; ordinary `Rows.Next` does not return custom structs. Future Go 1.27 scanning improvements are internal optimizations only and must not change public value semantics.[G03][G05]

## 8. Mode, Version, Capabilities, and Session State

```go
type Mode uint8

const (
    ModeAuto Mode = iota
    ModeMySQL
    ModeOracle
)
```

Store the following separately internally:

- `expectedMode`: from Config; may be Auto.
- `actualMode`: actual post-authentication compatibility mode; MySQL or Oracle only.
- `serverVersion` / `obVersion`: protocol version string and OceanBase version.
- `negotiatedCapabilities`: capabilities confirmed by handshake and connection attributes.
- `effectiveOptions`: behavior computed from configuration, version, and capabilities.
- `sessionState`: transaction state, schema, character sets, timezone, active results, resource generation.

On mode mismatch, close the new connection and return expected/actual modes in the error; never reinterpret the same response. Missing required server capabilities likewise fail before use.

Not every capability follows one handshake bit. PrepareExecute, offset fetch, and similar features require source-based version gates and real-server verification. Unparseable versions use confirmed conservative paths, not an assumed latest version.

A pool keeps one mode. ModeAuto atomically locks the Connector to its first successfully initialized connection's mode; reject later candidates returning another mode. Address sources must represent the same logical OceanBase service, never mixing tenants/database services. Checks do not log passwords.

## 9. Connection Establishment, Transport, and Protocol Layer

Connection sequence: address selection → Dial → initial handshake → TLS → authentication → mode identification → OB capability negotiation → packet-stream selection → session initialization → baseline snapshot.

- Connection context and ConnectTimeout cover address selection, external credentials, TLS, authentication, and initialization as one budget, never renewed per candidate. See §9.0.1 and §9.0.5 for soft-budget exceptions involving synchronous platform calls and unassociated Shared Memory allocation.
- Support TCP/Unix sockets. Windows Named Pipe, Shared Memory, and OBProxy VC integrate through supplied Go platform adapters and `DialContext`. Build tags isolate platform-specific native authentication/transport dependencies.
- Standard packets, compression, and OB20 share command codecs; OB20 manages request IDs, sequence numbers, extra information, and checksums.
- Handle fragmentation, reassembly, EOF/OK terminator differences, extra OK packets, session tracking, OUT responses, and multiple results.
- Validate packet lengths, column counts, nesting depth, and decompressed sizes before allocation; check int conversions for overflow on 32-bit platforms.
- Safely length-skippable unknown fields preserve synchronization; unknown structures with uncertain boundaries invalidate the connection.
- Never implicitly treat every unknown type as BLOB. Java has this path; Go instead returns UnsupportedType to prevent incorrect type interpretation reaching applications.[J03]

### 9.0.1 D28 Implemented TCP, Unix, and Named Pipe

`Config.Network` defaults to `tcp`. `unix` uses `net.Dialer.DialContext` with literal socket `Addr`, without environment/home expansion. Windows `pipe` uses `github.com/Microsoft/go-winio v0.6.2` with explicit UNC Named Pipe Addr. Established pipes use overlapped I/O/deadlines; dialing checks context and polls busy pipes. Built-in pipe dialing returns ErrUnsupported outside Windows, but custom DialContext may handle that network/address. Build tags isolate implementation without extra native libraries. `golang.org/x/sys/windows` identifies temporarily missing pipes for existing retries; permission errors return immediately.

Named Pipe UNC addresses must be valid UTF-8. Config/DSN reject invalid host/pipe bytes before connecting so Windows UTF-16 replacement cannot select another endpoint. Valid Chinese text, supplementary-plane characters, and literal U+FFFD are preserved; a valid replacement character is not an error. Unix sockets keep literal Go/OS address semantics.

Native IPC mixed-session tests use system Unix/Windows Named Pipe listeners and built-in driver dialing. Both modes cross standard/compressed/OB20 streams, repeatedly executing cached preparation, early Rows close, commit/rollback, and recoverable SQL errors on each connection. Each round checks original physical sessions/pools directly, without checkout reset hiding residual state. Exit checks QUIT; SetDeadline on the closed original Unix connection must return net.ErrClosed. Named Pipe and non-Windows Unix also check peer EOF. Windows AF_UNIX has shown successful Close without peer EOF, reproducible with the standard library alone; that combination checks actual original-connection closure. Tests do not alter production dialing/closing. Both transports have run on Windows; Linux/macOS Unix sockets still need CI execution, and test peers do not replace real services.

IPC is one explicit fixed endpoint and rejects Routing.Endpoints/Discovery combinations. Attempt counts, per-attempt/total budgets, initialization, idle validation, and subsequent commands retain existing flows. IPC failure never falls back to TCP. Non-nil TLSConfig requires explicit certificate ServerName for IPC rather than a file/pipe name; nil TLS grants no plaintext exemption. DialContext receives actual Network/Addr and retains context/failure-resource ownership contracts.

DSNs use `network=unix|pipe&socket=escaped-address`; these must appear together, and tcp/default networks forbid socket. URL authority is no longer the IPC dial address; the database path remains the database name. Programmatic Config sets Network/Addr directly without inventing TCP hosts. Parsed configuration remains valid when passed to NewConnector again.

Unix targets depend on Go/OS AF_UNIX support, including Go/OS handling of Linux abstract addresses. Local Windows file sockets have run tests. Named Pipe has run local Windows peers; remote UNC still requires deployment/permissions acceptance. go-winio uses synchronous CreateFile: blocking remote SMB opens cannot be interrupted by context, so ConnectTimeout is not a strict wall-clock bound. After return, the driver still checks cancellation and closes acquired connections. Both transports have local handshake, baseline, Ping, QUIT/closure, and plaintext-gate tests; this does not prove real OceanBase/OBProxy exposes these endpoints. Separate Shared Memory and OBProxy VC host implementations appear in §9.0.5/§9.0.6; real platforms remain unaccepted.

### 9.0.2 D32 Native Go TCP Configuration

Seven Java TCP options use existing `Config.DialContext` and Go's standard library, without TCPOptions, another dialer interface, or global factories. Callbacks receive current candidate network/address and remaining connection context, returning raw net.Conn; the driver performs unchanged TLS, mode negotiation, and initialization. Wrapping connections grants no plaintext-authentication exemption. Callers manage callbacks and captured Dialer/addresses; Connector cannot copy function internals, so keep configuration immutable and allow concurrent dialing.

Nil/typed-nil connections without errors are rejected before TLS/reads/credential requests without switching candidates. Errors retain original error/retry rules and close only genuinely non-nil connections, never invoking typed-nil methods. This also applies to explicit-cancellation auxiliary dialing. TestDialContextNilConnection/TestDialContextErrorClosesConnection cover both modes/nil forms, propagation/retry, and exactly-once closure of connections returned with errors.

| Java option | Go equivalent and default differences |
| --- | --- |
| tcpNoDelay | `(*net.TCPConn).SetNoDelay(bool)`; built-in Go 1.26 TCP defaults to true, matching Java |
| tcpAbortiveClose | Explicit `SetLinger(0)` enables it; `SetLinger(-1)` keeps normal background sending/closing. Default does not force RST, matching Java false |
| localSocketAddress | `net.Dialer.LocalAddr = &net.TCPAddr{IP: localIP}`; port 0 is system-assigned. TCP source interface binding, not a Unix socket address |
| tcpKeepAlive | `net.Dialer.KeepAliveConfig`; disable with KeepAlive=-1 and KeepAliveConfig.Enable=false. Go attempts default enabling with 15-second idle/interval and count 9; Java defaults true without specifying identical probes here |
| tcpRcvBuf / tcpSndBuf | `SetReadBuffer` / `SetWriteBuffer`; built-in behavior retains OS defaults/autotuning without hardcoded sizes; OS may adjust actual values |
| socketFactory | Inject `Config.DialContext`; no Java class-name parsing, reflection, or process-global factories |

The complete [compilable example](../tcp_example_test.go) uses LocalAddr/KeepAliveConfig, TCP_NODELAY, buffers, and linger, checking setter errors and closing acquired connections on failure. Its 64 KiB buffers and 30-second/10-second/3-probe keep-alive are application examples, not built-in defaults. net.Dialer keep-alive is best effort; applications requiring confirmation must call SetKeepAliveConfig on acquired TCPConn and check errors, as the example does. Before-SYN options use standard `net.Dialer.ControlContext`; options/support are platform-dependent.

`SetLinger(0)` may discard pending data and cause peer-visible resets. It neither proves COM_QUIT delivery nor rolls back executed/committed SQL and cannot prevent replay or recover unknown outcomes. The example disables it by default; a separate case checks local transport termination without requiring identical cross-platform RST codes. Keep-alive does not replace ReadTimeout, contexts, pool idle/lifetime settings, or business-state confirmation.

Policies are programmatic only; all seven Java DSN names stay explicitly rejected. TCP-specific examples return ErrUnsupported for unix/pipe without fallback; IPC remains separate. TestTCPDialContext* uses native local TCP peers to check source addresses, selected endpoints, single factory calls, handshake/initialization/Ping/QUIT, plaintext rejection, cancellation, and abortive close. ExampleConfig_tcpOptions is offline. Real remote interfaces, OS buffering/probes, and databases remain unverified.

Pinned Java evidence: DefaultOptions tcpNoDelay/tcpAbortiveClose/localSocketAddress (L100–L116), tcpKeepAlive/tcpRcvBuf/tcpSndBuf/socketFactory (L225–L249), and AbstractConnectProtocol.createSocket (L249–L285). Go defaults follow Go 1.26 net.Dialer/newTCPConn. Matching Go APIs do not establish real-network acceptance.

### 9.0.3 D32 Go SOCKS5 Proxy Support

SOCKS5 uses existing `Config.DialContext` with official `golang.org/x/net/proxy.SOCKS5`. The [compilable example](../socks_example_test.go) supplies an explicit proxy and context-aware `net.Dialer`, passing `proxy.ContextDialer.DialContext` directly to the driver. No new proxy struct/dial interface/global registry, ALL_PROXY/NO_PROXY reading, or direct bypass after proxy failure. Construct the callback before Connector, reuse it for concurrent connections, and keep proxy configuration/source objects immutable.

Java socksProxyHost defaults null and socksProxyPort 1080. Go still defaults direct; callers explicitly use `net.JoinHostPort(host, "1080")`, including IPv6. Both Java DSN names are rejected. Current routing candidates pass unchanged to callbacks; SOCKS resolves hostnames remotely without driver-side IP resolution or pinning to initial Config.Addr. Retries retain total budgets/roles/blacklists without replay or automatic direct fallback.

Proxy credentials use separate `proxy.Auth`, unrelated to OceanBase Credentials/ProxyUser. Nil requests only no-authentication; non-nil permits server-selected no-authentication or username/password and does not mandate password authentication. SOCKS5 password negotiation is plaintext; subsequent database TLS does not protect it. Deployment protects that link without relaxing database TLS gates. Database TLS verifies selected database endpoints or explicit ServerName, never the proxy hostname.

ConnectTimeout/AttemptTimeout and contexts cover proxy TCP/SOCKS and later database handshake/initialization; dialers close tunnels on cancellation/failure. Socket deadlines can precede context timers, returning context.DeadlineExceeded or net.Error.Timeout; custom dialers need not normalize all errors. Successful connections do not inherit establishment-context lifetimes; later commands use separate context/ReadTimeout. Only TCP targets are supported, without IPC proxy exemptions. Java `connectProxy` is a separate initialization policy, not SOCKS/HTTP CONNECT; its explicit counterpart in §9.0.4 is not automatically enabled.

Go 1.26 examples/local tests use x/net v0.61.0, with x/sys v0.49.0 and x/text v0.43.0 module dependencies; upgrades require full-package/cross-platform regression. Production packages do not import x/net, preserving APIs/dependency directions. TestSOCKS* covers both modes/three streams, remote DNS, authentication, zero database credentials before proxy success, routing switches, cancellation/closure, positive/negative TLS, and target identity. Real proxy deployment, DNS/permissions, throughput, and database integration remain unaccepted.

### 9.0.4 D32 Explicit Version-Probe Suppression

`Session.SkipVersionProbe` (DSN `skipVersionProbe`) defaults false; explicitly enable only when the application knows targets lack initialization probes such as `ob_version()`. Initial connections, ChangeUser, and cancellation auxiliaries then send no version query and retain unknown internal OceanBase versions. Authentication still confirms mode; charset, autocommit, timezone, NCharset, Variables, InitSQL initialization/error handling remain. Never infer this from TCP/SOCKS/OBProxy addresses or treat handshake versions, application InitSQL, or metadata responses as confirmed OceanBase versions.

Unknown versions disable PrepareExecute, offset fetch, and version-gated SQL checksums. Oracle native OUT/LOB, sensitive cursors, streaming long parameters, and XA return ErrUnsupported through existing gates; ordinary CRUD/preparation/transactions remain unchanged. Explicit MetadataServerInfo still queries its version regardless of initialization suppression. Default probe failure preserves errors and closes connections without automatic suppression fallback; baseline/InitSQL failures likewise prevent publishing connections.

Java connectProxy defaults false, skipping some session-variable reads, ob_version, and auto_increment_increment probes. Go has no matching implicit variable/increment reads and maps only its existing optional version probe, without an ambiguous connectProxy field or Java DSN name. This does not guarantee a proxy accepts other baseline SQL; deployment still needs validation. Evaluate advanced-capability restrictions before use.

Java `usePipelineAuth` defaults null, resolved to false by UrlParser. It selects post-authentication initialization without changing plugins/TLS. Pinned [postConnectionQueries](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L978) also temporarily shortens reads to 500 ms and may fall back to additionalData. Yet pinned [sendPipelineAdditionalData](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1103) immediately calls getResult per SET; names/descriptions cannot establish parallel sending. Go sends/fully consumes initialization statements sequentially after final authentication OK, without a same-named switch, temporary 500 ms timeout, or retries of potentially side-effecting failed InitSQL. Existing ConnectTimeout/context and separate ReadTimeout apply. TestInitializationErrorBoundary covers authentication/charset/autocommit/timezone/variables/InitSQL rejection, chains, closure, and pool exclusion. TestVersionProbeSessions/Initialization cover both modes/three streams and custom-baseline restoration. No initialization pipelining/performance benefit is claimed.

### 9.0.5 D28 Windows Shared Memory

`Config{Network: "sharedmemory", Addr: "MYSQL"}` selects Windows native named-event/file-mapping transport; the DSN is `network=sharedmemory&socket=MYSQL`. Addr is the server object basename, not a disk file; explicit `Local\`/`Global\` prefixes are allowed. Permit only one namespace prefix and a nonempty UTF-8 basename without NUL, backslashes, or edge whitespace. Total length is at most 220 UTF-16 units, reserving room for connection IDs/event suffixes. No environment expansion. Try Global only when no prefix is supplied and local CONNECT_REQUEST is absent, never on permission errors. TCP remains default; Java sharedMemory is not a DSN alias.

Implementation stays in root-package Windows files using existing x/sys/windows, without new public interfaces, packages, or dependencies. Built-in non-Windows dialing explicitly returns ErrUnsupported; custom DialContext may handle the same network/address. Fixed IPC endpoints, Routing/Discovery exclusion, explicit TLS ServerName, and authentication gates match §9.0.1. Local shared memory does not replace verified TLS.

Protocol evidence is pinned Java [SharedMemorySocket](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/io/socket/SharedMemorySocket.java): CONNECT_MUTEX serializes CONNECT_REQUEST/CONNECT_ANSWER and the four-byte connection-ID mapping; then five ID-specific events and a 16004-byte DATA area open. IDs use signed 32-bit decimal object names. Go holds/releases allocation mutexes on a fixed OS thread, creates no substitute server mappings, and widens no permissions. Mutex access is SYNCHRONIZE only, CONNECT_DATA uses FILE_MAP_READ, and DATA uses FILE_MAP_WRITE (read/write view), matching Java minimum ACLs. After acquiring an ID, obtain CONNECTION_CLOSED first so later open failure/cancellation can notify the server and release resources.

**Allocation cancellation has a soft budget.** Waiting for CONNECT_MUTEX and the stage before sending can cancel promptly. After CONNECT_REQUEST is sent, answers lack correlation IDs, so retain the lock until an answer arrives, then close that allocated session and return the original context error; otherwise another connection could claim the old answer. ConnectTimeout, AttemptTimeout, and calling contexts cannot impose strict wall-clock bounds here. A permanently silent server leaves this dial waiting and holding one OS thread; later bounded callers may time out waiting for the mutex. The driver neither abandons background workers nor resends incomplete requests or sends database credentials for canceled sessions. Other clients sharing the endpoint must obey the same named mutex protocol; safe concurrent allocation cannot be guaranteed with legacy clients bypassing it.

DATA begins with a four-byte little-endian length and supports at most 16000 payload bytes, avoiding Java's out-of-bounds path treating 16004 as payload capacity. Read copies the full block to a Go buffer before ACK and supports partial caller reads; zero/oversized native blocks are rejected. Write splits at the limit and counts bytes after notifying the peer, retaining the notified prefix on timeout. The last block does not wait for another ACK, so Close may discard an unconsumed tail; QUIT delivery is not guaranteed and cannot prove SQL rollback/nonexecution. One data area cannot support bidirectional pipelining: sharedmemory BatchAuto/BatchMultiSend falls back to BatchSequential before sending and reports the actual strategy, preserving functionality through sequential execution.

Established Read/Write supports moving deadlines earlier/later or clearing them, retaining os.ErrDeadlineExceeded/net.Error.Timeout. Close wakes waiters, waits for native wait/memory access to exit, then closes handles/unmaps, avoiding handle closure during Win32 waits. Each I/O direction is serialized. No per-I/O goroutines or event polling. Local tests cover both modes/three streams, initialization/Ping/QUIT, TLS target verification/rejection, blocks/partial reads, invalid lengths, dynamic deadlines/Close, pre/post-allocation cancellation/delayed answers, concurrent allocation/negative IDs, partial-open failures, and batch fallback. These do not accept real servers, object ACLs, or mixed-client interoperability. Separate OBProxy VC adaptation is in §9.0.6; D28 platform/live-service evidence remains separately tracked.

### 9.0.6 D28 Embedded OBProxy VC

Pinned OBProxy commit `81eeae92526f5efbee516e863c7234532dfa6175` [C entry points](https://github.com/oceanbase/obproxy/blob/81eeae92526f5efbee516e863c7234532dfa6175/src/obproxy/driver/c/ob_proxy_vc_c.cpp), [process initialization](https://github.com/oceanbase/obproxy/blob/81eeae92526f5efbee516e863c7234532dfa6175/src/obproxy/ob_proxy_init.cpp), [client](https://github.com/oceanbase/obproxy/blob/81eeae92526f5efbee516e863c7234532dfa6175/src/obproxy/proxy/client/ob_driver_client.cpp), and [layout](https://github.com/oceanbase/obproxy/blob/81eeae92526f5efbee516e863c7234532dfa6175/src/obproxy/utils/ob_layout.cpp) show init starts a single OBProxy then creates an unconnected client handle; data uses `.obproxy/obproxy.domain` in the working directory. Native Close immediately frees handles, timeout setters do not wake polling I/O, and send collapses error codes. Data I/O therefore uses standard Go Unix net.Conn.

[ObProxyMain startup and signals](https://github.com/oceanbase/obproxy/blob/81eeae92526f5efbee516e863c7234532dfa6175/src/obproxy/ob_proxy_main.cpp) show client mode also installs SIGINT/SIGTERM and other handlers without Go runtime-required SA_ONSTACK, changes locale/allocators, registers exit callbacks, and starts background threads. Loading it directly into Go would violate runtime/application signal contracts. A project-supplied separate C host loads the library and isolates process side effects; Go retains one root package and DialContext.

```mermaid
flowchart LR
    DB[database/sql] --> Dial[Go DialContext]
    Dial -->|First startup and readiness handshake| Host[C host process]
    Host -->|Official init / close| Native[Native OBProxy library]
    Dial -->|Unix net.Conn / TLS / shared protocol| Socket[.obproxy/obproxy.domain]
    Native --> Socket
```

`NewOBProxyDialer(hostPath, libraryPath, proxyConfig)` returns Config.DialContext. hostPath is the absolute path of a trusted executable built from [host source](../tools/obproxy-host/main.c); libraryPath is an absolute trusted native shared-library path. No PATH search, automatic downloads, or shell execution. Build on Linux from the repository root:

```sh
cc -std=c11 -O2 -Wall -Wextra -Werror -o obproxy-go-host tools/obproxy-host/main.c -ldl -pthread
```

Deployment supplies native libraries/dependencies separately; the command above builds only the host. Go production targets are Linux amd64/arm64 with the `obproxy` build tag; other targets explicitly return ErrUnsupported. Ordinary builds need neither a C compiler nor the library; build the host separately per platform. Windows obproxytest runs only a custom C ABI fixture and does not claim native Windows OBProxy support.

proxyConfig contains native `-o` content: valid UTF-8, no NUL, maximum 4095 bytes to avoid the official fixed argv buffer overflow. Construction only validates and captures the working directory. First dialing starts the host and sends original configuration over stdin as a uint32 little-endian length frame; the host encodes it as the sole JSON proxy_config field (empty configuration becomes `{}`). Native output is separate from control: before loading, the host redirects stdout/stderr to the null device; native configuration still owns file logs. The host calls official `ob_proxy_vc_c_init` / `ob_proxy_vc_c_close`, immediately releases the temporary unconnected handle, then returns an eight-byte readiness message `OBV1` + int32 little-endian status. Missing library/symbols, nil handles, or nonzero status fail. Go closes pipes, terminates/reaps failed hosts; the reader is independent of cmd.Wait to prevent exit/readiness races.

After startup the host retains stdin as a control pipe; parent exit causes EOF and a dedicated monitor thread terminates all native threads, even when init is stuck. DB.Close closes business connections only, not the shared host; successfully initialized libraries are never unloaded. Host/library/configuration/working directory form one process-wide identity: identical concurrent callers share one init, differing identities are rejected. Failures are cached without restart/replay. Later host exit surfaces through Unix errors and cannot establish whether SQL executed.

Config uses `Network=unix`, `Addr=.obproxy/obproxy.domain`. The callback rejects other networks/paths, routing/discovery mixing, and TCP fallback. Native startup deletes/recreates the socket, so the working directory must be exclusive; absolute socket paths are at most 107 bytes and chdir is forbidden during use. Different configurations require separate parent processes/directories. Cancellation before initial startup creates no host; waiting calls may return on context while shared init continues, so cancellation does not imply native initialization stopped. Subsequent connections, dynamic deadlines, and Close use standard Unix connections.

Every physical session reuses standard TLS, authentication, mode identification, and initialization. IPC grants no plaintext exemption; TLS requires explicit ServerName. Java cs_id is native-client logging only; Go retains protocol connection IDs and caller-supplied slog, with nil silent. Native logging remains in the separate process and never uses slog.Default. Offline fixtures cover ABI, configuration escaping/limits, readiness/failure/EOF cleanup, both modes, and three streams. Real Linux native-library startup, signals, exit, and server interoperability still require platform acceptance.

### 9.1 Implemented Packet Streams and Negotiation (D23)

All six `ProtocolOptions` policies and DSN `compression`, `ob20`, `checksum`, `proxyChecksum`, `newMetadata`, `fullLinkTrace` accept only `auto|off|required`. Invalid/conflicting configuration fails before Dial. Selection happens only during establishment; execution never changes protocols or replays business operations.

| Capability | Selection and behavior |
| --- | --- |
| Compression | Intersection of greeting CLIENT_COMPRESS and client policy; after final authentication OK, use zlib with a seven-byte outer header; send uncompressed frames when compression saves nothing |
| OB20 | CONNECT_ATTRS declares proxy bits 8/15; final authentication OK session-tracking `ob_capability_flag` bit 8 confirms it. Preferred over optional compression; CompressionRequired does not request OB20 |
| Checksum | OB20 CRC16 headers and zero-initialized, uncomplemented CRC32C payloads. auto sends checksums and validates received nonzero checksums; off sends none but still validates nonzero received checksums; required demands OB20 and compares actual values even against zero checksums |
| ProxyChecksum | Java `useObChecksum` proxy capability bits 3/6, independent of OB20 CRC. auto requests, required demands both confirmed bits, off omits declaration and rejects nonzero echoes. This does not prove proxy-to-backend checksums were verified |
| NewMetadata | Request client attribute bit 1 (value 2), confirmed by `__ob_client_capability_flag`; column layout stays unchanged. Oracle NCHAR/NVARCHAR2 lengths count characters, RAW/ROWID lengths use precision, LOB length is unknown. Do not copy JDBC Java-class/display-width rules |
| FullLinkTrace | D25; OB20 + FLT bit 14, supporting new-extra bit 15 and ShowTrace bit 18; independent of logging, see §18.3.1 |

Unconfirmed auto capabilities retain standard behavior. Missing required capabilities, enabled off capabilities, and invalid echoes fail establishment. CompressionRequired conflicts with OB20Required/ChecksumRequired/FullLinkTraceRequired; OB20Off conflicts with ChecksumRequired/FullLinkTraceRequired. New attributes retain implemented locator-v2 bits without requesting unimplemented extra-OK, session sync, or routing. See §18.3.1 for D25 FLT negotiation.

Size layers are independent: outer/decompressed lengths use 24-bit fields, at most `0xffffff` bytes; OB20 outer bodies add 28 bytes overhead. One outer frame may contain multiple logical messages, each independently checked against `Limits.MaxMessageBytes`; never count an aggregate frame as one message. Decompression reads at most declared length plus one and rejects mismatches, corrupt streams, and trailing compressed data. Inner headers crossing frames, 16 MiB continuation packets, and empty terminators reuse the logical-message state machine.

OB20 resets both sequence layers per command; 24-bit request IDs increment and wrap. Receivers validate connection/request IDs, sequences, flags, and new-extra TLV lengths. Unknown bounded extras are safely skipped; old extras are skipped by total length without claiming their fields were processed. Corruption, partial writes, and cancellation during I/O invalidate connections; errors preserve actual network byte counts. Unconsumed frame data prevents resetting into another command. Outer last flags do not replace complete inner response boundaries.

Current compressed/OB20 multi-send batches fall back to sequential before sending; `BatchResult.Strategy` reports Sequential. Enable future pipelining only after separate packet-stream identities are maintained for each in-flight command and equivalence is verified.

Source evidence: [handshake and both checksum attributes](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L361), [final OK and stream switching](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823), [OB20 layout](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/flt/OceanBaseProtocolV20.java), [new metadata interpretation](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/ColumnDefinition.java#L443). Local protocol verification remains distinct from real compatibility acceptance; see Compatibility Matrix §14.

## 10. Execution Core and Binding

### 10.1 Path Selection

- Prefer COM_QUERY for ordinary parameterless execution.
- Default parameterized execution to server prepare/execute, avoiding ordinary-call text interpolation.
- Config.PrepareMode defaults PrepareServer; explicit PrepareClient uses mode-aware lexical plans/text encoding. MySQL text uses charset-qualified hex literals; Oracle uses quoting/type literals. Use current NO_BACKSLASH_ESCAPES without querying sql_mode during execution. No automatic fallback currently.
- Oracle cursor, LOB, and extension-protocol statements may select server execution even without IN parameters.
- Retain original SQL, binding plan, and statement generation on the connection; bounded per-session LRU server caches release evicted server resources.
- PrepareExecute, checksum, parameter-type-header reuse, long/piece data, multi-send, bulk, and array binding are implementation strategies in the same core.

SQL text, server preparation, native OUT, and client interpolation are ultimately limited by session-encoded bytes; command bytes for COM_QUERY/COM_STMT_PREPARE also consume budget. Before lexing, retain bounded checks based on original SQL code points, avoiding false rejection of UTF-8 text shrinking under GBK/latin1. Placeholder-name shrinking cannot bypass original parsing limits. Interpolation determines boundaries/escaping in UTF-8, then encodes SQL fragments and complete literals segment by segment, charging actual bytes and surrounding parameter spaces without reencoding transport bytes. Oracle quotes, doubled quotes, NUL CHR(0), and parentheses consume encoded budget; MySQL charset prefixes/hex expansion also count. Invalid encoding/limits fail before business sends without truncation/replay; synchronized sessions remain usable. Independent original UTF-8 StatementCacheSQLLimit and dual LongData content limits remain.

Implement the COM_STMT_PREPARE_EXECUTE basic encoding, output decoding, and capability gates needed by Oracle RETURNING in D14 rather than waiting for D24 optimization. D24 explicitly depends on D14 and reuses that path for general execution selection, caching, and performance policy; D14 does not depend on D24. When protocol is a semantic requirement, do not fall back to ordinary execution without proven support.[J17]

Fallback occurs only before business execution. Only confirmed unsupported server-prepare errors may permit configured client-prepare fallback; lost execution responses never trigger another execution path.

Multi-statement text requires explicit `AllowMultiStatements`; semicolons inside procedures/Oracle blocks are not multiple top-level statements. Parameterized multi-statements unsupported by server preparation must fail capability checks before sending or use explicitly selected client preparation; never secretly enable interpolation. Use Execute (§11.4) for full ordered mixed results/per-item counts.

### 10.1.1 Implemented Optional Execution Strategies (D24)

Enable explicitly through `Config.Execution ExecutionOptions`; zero values preserve separate prepare/execute, no cache, type headers each time, and no SQL checksum. Ordinary applications still use `database/sql`, without another pool/Statement API.

```go
cfg.Execution = oceanbase.ExecutionOptions{
    PrepareExecute:         true,
    StatementCacheSize:     64,
    StatementCacheSQLLimit: 1024,
    ReuseParameterTypes:    true,
    StatementChecksum:      true,
}
```

Five DSN options: `prepareExecute`, `statementCacheSize`, `statementCacheSQLLimit`, `reuseParameterTypes`, `statementChecksum`. Booleans accept only `true|false`; negative cache limits fail before dialing. Capacity 0 disables caching; SQL length limit 0 normalizes to 1024 original Go-string UTF-8 bytes.

| Strategy | Implemented selection and boundaries |
| --- | --- |
| General PrepareExecute | Oracle mode, explicitly enabled, actual ob_version >= 2.2.76. Ordinary parameterized calls combine with ID 0, then reuse returned IDs; explicit sql.Stmt retains standard Prepare/Close ownership |
| Missing capability | MySQL, unknown, or old actual versions retain standard prepare/execute before sending. Greeting versions are not actual OceanBase versions; explicit client preparation remains client-side |
| OUT and long parameters | D14-required combined OUT protocol is independent of general optimization. Long parameters acquire IDs before upload; ordinary execution retains standard execute while native OUT/cursors use required extensions. No reselection/replay after upload |
| SQL checksum | Zero-initialized, uncomplemented CRC32C of rewritten SQL encoded in actual transport charset. Standard execute uses it only for Oracle actual version >= 4.1.2 in the former iteration field; combined commands use a dedicated trailing field and iteration 1. Independent of OB20/proxy checksums; standard batch/cursor execute shares the gate |
| Type-header reuse | Save vectors only after complete successful responses for the same statement. Types include unsigned bits/complex descriptions; resend on any change or execution failure. Retain types/descriptions only, never previous data; encode NULL bitmaps/values every time |
| Automatic statement cache | Bounded per-session LRU; recycle only fully successful automatic SELECT/INSERT/UPDATE/DELETE/REPLACE without resource leases. Key by SQL text; checked-out handles are not idle-cached. Failure, excessive length, eviction, and connection close release server IDs |
| Session changes | Clear idle cache before ordinary text/OUT paths and noncacheable prepared SQL; refuse reuse after NO_BACKSLASH_ESCAPES changes. Full reset discards physical sessions without migrating IDs/cache |
| Procedures and connection resources | CALL/anonymous blocks bypass transparent LRU because procedure session effects can invalidate old cache. Use explicit sql.Stmt for repeated calls with caller/standard-library lifetimes. OpenBlob/OpenClob, complex results, REF CURSOR, and Execution summaries retain complete-response/same-session boundaries |

No Java-style callable facade, global SQL cache, or shared server IDs. Failures after sending are reported directly; SQL errors never switch strategy. Corruption, cancellation, and desynchronization discard sessions. Local byte-protocol tests/encoding benchmarks do not prove real database performance/version compatibility. Four-target `TestExecuteStrategies` source/CI entry exists but is compiled only.

### 10.2 Placeholders and Named Binding

| Mode | Supported rules |
| --- | --- |
| MySQL compatibility | Positional `?`; explicitly reject named parameters rather than silently treating them positionally |
| Oracle compatibility | Positional `?`; `:name` with `sql.Named("name", value)`; numeric names such as `:1` follow positional plans |

Reject mixed positional/named binding, missing parameters, duplicate supplied names, and excess arguments before sending. Repeated SQL names map one input to every physical occurrence. Repeated OUT binding requires provably consistent server output semantics, otherwise fails.

Lexical plans cover single/double quotes, backticks, comments, escapes, Oracle q-quotes, and `:=`. Never replace colons textually or treat anonymous-block semicolons as multi-statement separators.

Ordinary SQL uses native server syntax. Explicit compatibility conversion functions provide Java JDBC escape equivalents through lexical state machines; Go SQL is not rewritten by default.

MySQL `CONVERT` boolean/floating expressions from `RewriteJDBCEscapes` always parenthesize complete operands, retaining OR/XOR/bitwise/comparison precedence; boolean conversion retains existing comparison-to-1 semantics. Both modes preserve newline after trailing operand line comments so generated parentheses/CAST suffixes are not commented out. Whitespace/comment-only missing operands return ErrBind. `TestJDBCConvertOperandBoundaries`, `TestJDBCConvertMissingOperand`, and `TestJDBCEscapes` check text, nesting, and argument counts, not real server evaluation.

Pinned Java `encloseParamInParentheses` defaults true but since 2.4.10 merely inserts a space before Oracle text parameters, without parentheses; see [ComQuery.addParenthesesArroundValue](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComQuery.java#L366). Go client preparation/batch rewriting always preserves spaces around each literal, preventing negative values plus preceding minus from forming comments or numbers plus aliases from becoming exponents. Always enabled in both modes; no switch disables boundary protection. Server preparation sends separate binary parameters without SQL-parenthesis/precedence changes. TestClientParameterBoundaries exercises public ExecContext/sql.Stmt in both modes/three streams, repeated names, negatives/numeric aliases, and semicolons in quoted strings. These prove encoding boundaries; live SQL interpretation still needs integration.

### 10.3 Routing Extensions Through the Same Core

The standard library cannot directly express batch, scroll fetch, or locator operations. Helpers may internally pass a **package-private request value** to ExecContext/QueryContext, accepted by `NamedValueChecker` and separated from SQL arguments before sending.

- Only this package constructs requests containing concrete operations/result pointers, not a public `any` command container.
- Do not carry operations through globals, goroutine IDs, context string commands, or special SQL text.
- Do not mutate connections during argument checking through `ErrRemoveArgument`, which could leave implicit state when later validation fails.
- Convert/validate every argument before executing; batch parameters share conversion rules.
- Use this mechanism only where standard interfaces cannot express a capability; retain standard connection selection, transaction binding, and concurrency management.

## 11. Results, Errors, and Value Conversion

### 11.1 Ordinary Results

`Rows.Next` yields `nil`, `int64`, `float64`, `bool`, `string`, `[]byte`, `time.Time`, and standard-protocol child-cursor `driver.Rows` where needed. Never pass arbitrary structs directly to `database/sql`.[G03]

Buffers may be reused between Next calls subject to `[]byte`/`sql.RawBytes` lifetime rules. Copy caller-owned values and data retained by type Scanners; Close must not overwrite independent memory still referenced by callers.

Parsers handle complete ordinary-result, update-count, and callable-OUT boundaries. `NextResultSet` switches ordinary row sets only, not public per-item counts; Execute (§11.4) exposes counts/order. OUT control results never masquerade as user row sets.

### 11.2 Type Mapping

| Data semantics | Ordinary Scan source | Exact/special usage |
| --- | --- | --- |
| Signed integers | int64 | Overflow errors |
| Large unsigned integers | int64 when representable, otherwise decimal string | Scan into uint64; NamedValueChecker preserves/checks uint64 input |
| DECIMAL, NUMBER | Exact decimal string | Lightweight `Number` or third-party Scanner; no float64 intermediate |
| FLOAT, DOUBLE, BINARY_FLOAT/DOUBLE | float64 | Protocol precision; type/configuration gates NaN/Inf |
| CHAR/VARCHAR, NCHAR/NVARCHAR2 | UTF-8 string | `NString` preserves NCHAR input type; server/column type defines fixed CHAR spaces |
| BINARY, RAW, inline BLOB | []byte | Explicit RAW/ROWID types select input protocol types |
| BOOL, BIT | bool or []byte, fixed by column/configuration | Never force all TINYINT(1) into bool |
| JSON | string | Scan into []byte/custom JSON Scanner; large numbers avoid JSON float64 conversion |
| Timezone-free DATE/DATETIME/TIMESTAMP | Canonical text; optional time.Time parsing | Preserve date/local-time semantics, not implicit UTC instants |
| TIMESTAMP with timezone | Canonical text retaining precision, offset, available region | `TimestampTZ` Scanner/input retains region rather than only a formatted offset |
| TIMESTAMP LTZ | Canonical text using explicit session timezone | `TimestampLTZ` retains identity distinct from timezone-free values |
| TIME, YEAR, zero dates | TIME text may be negative/exceed 24h; YEAR integer | Preserve zero dates by default; explicit time.Time parsing rejects invalid dates |
| INTERVAL YM / DS | Lossless canonical text | `IntervalYM` / `IntervalDS` preserve months/day-second precision rather than squeezing into time.Duration |
| OBJECT, ARRAY/collections | Lossless text with format version/type descriptors | `Object` / `Array` Scanner; binding uses real complex-type protocol |
| BLOB/CLOB/NCLOB | Bounded ordinary-query []byte/string materialization | Streaming/mutation uses Blob/Clob extensions, not connection handles disguised as bytes |
| REF CURSOR | Standard child driver.Rows or explicit output cursor | Read within parent resource/original session |
| GEOMETRY | Original []byte | Never assume UTF-8 |

`BIGINT UNSIGNED` has `ColumnTypes().ScanType()` uint64, covering 0–18446744073709551615; signed BIGINT uses int64. ScanType recommends a target and need not equal each row's actual driver.Value: unsigned values use int64 when possible, otherwise exact decimal strings, allowing lossless database/sql scanning into uint64. Nullable columns use pointers or `sql.Null[uint64]`; ScanType never encodes NULL as zero.

`Number` provides lossless representation, validation, and Scanner/Valuer, not a decimal arithmetic library. `NamedValueChecker` first recognizes extension values and preserves protocol identity; ordinary third-party `driver.Valuer` conversion then follows standard contracts.[G04]

Ordinary Scan representations of complex values must describe format version, schema/type name, field order/types, NULL, exact numbers, temporal values, and binary explicitly. The implemented `encoding/json` format=1 uses type tags, decimal text for numbers, and base64 for binary; `Object.Scan`/`Array.Scan` parse it. This is a documented public representation, not an in-process handle or fallback for unknown types.

Object/Array inputs retain explicit type names/field order. Do not reflect arbitrary structs into database objects or treat arbitrary slices as Oracle collections. Descriptor-cache keys include mode, schema, type name, and available version; refresh after DDL/type mismatch without blindly replaying business execution.

### 11.2.1 Implemented Scalar Conventions (D12)

Public values are lightweight: `Number`, `NString`, `RowID`, `TimestampTZ`, `TimestampLTZ`, `IntervalYM`, `IntervalDS` underlie string; `Raw` underlies []byte; all implement Scanner/Valuer. NULL uses `sql.Null[T]`/nil pointers; nil `Raw` is NULL and non-nil empty `Raw` is empty. The driver recognizes these types and their `sql.Null[T]` before generic Valuer to retain input identity; ordinary Scan still emits standard driver.Value only. Server semantics determine Oracle empty-string/RAW normalization to NULL; the driver does not fabricate empties.

Number validates decimal/exponent text, never reconstructs exact values from float64, and implements no arithmetic. Inputs encode exact DECIMAL/NUMBER; bounded exponent expansion precedes sending so MySQL does not interpret DOUBLE. Overflow, invalid encoding, and lossy conversion error rather than truncate/replace. RowID retains row-identifier text; Oracle binds it as a string, while clients explicitly construct CHARTOROWID.

Number budgets count normalized fixed-decimal bytes, avoiding false rejection from longer plus signs, trailing dots, or exponent spellings. Validate decimal syntax and signed 32-bit exponent, calculate output length from mantissa digits/decimal point/exponent, check remaining budget/platform MaxInt, then copy digits/pad zeros. Existing significant digits, signs, and mantissa scale remain through fixed conversion, never float64. Prefixes, complex fields, and entire commands retain cumulative limits. Independent result-message, LongData, and original-SQL limits are not relaxed.

Temporal values preserve text by default. `Session.ParseTime` (DSN `parseTime`) defaults false; enabling parses only timezone-free DATE/DATETIME/TIMESTAMP into time.Time using explicit `Session.Location`. Default UTC is actually applied as session time_zone on connection. Reapply connection charset, timezone, and NCHAR baseline after Variables/InitSQL for consistent codecs. Business SQL changing these settings must keep Config consistent; Managed reset does not parse arbitrary SQL to track them. Zero dates and DST gaps/overlaps return ErrType when parsed into instants; TIME/YEAR retain duration-text/integer semantics.

Oracle protocol type 12 means `DATE`; `ColumnTypes().DatabaseTypeName()` and server parameter metadata report `DATE`, preserving hours/minutes/seconds. MySQL reports `DATETIME` for the same protocol type. ParseTime changes text/time.Time Scan sources, never database identity. Evidence: pinned Java [ColumnType.getColumnTypeName](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java#L454). TestDateColumnTypes/TestDateParameterMetadata cover both modes/three streams, three execution paths, ParseTime/NULL. Live TestAllScalarTypes `DATE`/TIMESTAMP checks are compiled only, awaiting integration.

Timezone text example: `2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT`. Offset is required; region/abbreviation optional. Reject disagreement with known regions; preserve unknown regions and explicit offsets, with Time() using the offset without guessing. Native LTZ fields carry session wall time only: append offset for fixed sessions or region for transitioning sessions, e.g. `2024-11-03 01:30:00.000000001 America/New_York`. Time() rejects ambiguity. Same-session regions may bind unchanged, but converting local instants into ambiguous LTZ wall times also fails to avoid losing fold. Client/server preparation share session conversion: resolve different-region wall times unambiguously to instants, then convert into configured session timezone. Reject source gaps/overlaps, target overlaps, and resulting years outside 1–9999 before sending. Client preparation sends converted region inputs as session wall time and explicit-offset inputs as offset literals; both pass the same target-wall-time checks. Use TimestampTZ binding and server LTZ conversion to specify fold.

LTZ same-region checks use the actual published initialization timezone, not just `time.Location.String()`. A `time.FixedZone` name is merely a caller label: even `UTC` or an IANA name follows the actual fixed offset. Same-name region inputs still resolve source zones and convert, without skipping DST/year/offset validation. Preserve original wall time only for transitioning zones matching actual session regions.

Oracle temporal protocol preserves nanoseconds/original precision; ordinary time.Time binds native TIMESTAMP(9). MySQL supports microseconds only and explicitly rejects greater precision. IntervalYM/DS retain sign, up to nine year/day digits, and nine fractional-second digits without time.Duration conversion. Decode Oracle extended temporal values in binary/text rows as native field bodies, never character text.

The charset leaf package uses x/text v0.41.0; connection SQL, prepared SQL, parameters, results, and column names receive matching codecs. See [Compatibility Conditions](go-driver-compatibility-matrix.md) for mappings/limits. NString uses NCharset, falling back to connection Charset when unspecified. UTF16 is NCHAR/results-only, not an SQL client charset. Unknown encoding/collation is rejected. Parse syntax in Go UTF-8 then encode; never seek quotes/placeholders inside GBK bytes.

Even when client-prepared NString generates ASCII UNISTR, first validate representability using NCharset → Charset → utf8mb4, matching server binding. Unicode escapes cannot bypass GBK/ASCII/utf8mb3/latin1 restrictions. Explicit UTF16 national charset still allows Unicode unrepresentable in connection charset using UNISTR. Local rejection sends no execute command and preserves synchronized statements/sessions. Binary-parameter content and UNISTR SQL-expansion budgets remain separate; equal numeric limits need not accept identical input across paths.

Binary-bound ordinary strings, NString, and complex text fields consume post-transcoding parameter bytes, avoiding false rejection when GBK/latin1/UTF16 shrinks Go UTF-8. Before allocation, code-point counts check only a one-byte minimum; actual encoded content is strictly checked. Existing cumulative parameter/message budgets charge prefixes, type headers, and command overhead. Expansion, invalid input, and excess limits reject before sending without truncation/replacement or desynchronization. SQL/interpolation separately follow §10.1 encoded/cumulative checks; LongData.MaxBytes still limits source and encoded bytes.

### 11.2.2 Implemented OBJECT/ARRAY Conventions (D18)

Public values remain in the root package without public codec/descriptor packages:

```go
type Attribute struct {
    Name, Type string
    Value any
}
type Object struct {
    Schema, Type string
    Version uint64
    Fields []Attribute
    Null bool
}
type Array struct {
    Schema, Type string
    Version uint64
    ElementType string
    ElementVersion uint64
    Elements []any
    Null bool
}
```

Schema, Type, and field Name/Type use exact dictionary spelling; omitted Schema queries CURRENT_SCHEMA on the original session. Object input validates dictionary field order; Attribute.Type is optional. No arbitrary struct/slice reflection. Empty Array.Type means anonymous collection; ElementType is a built-in or explicit object type. Schema/ElementVersion describe anonymous object-collection elements. Named collections resolve element types from dictionaries. Version/ElementVersion come from protocol; 0 means current server version, not an invented dictionary number.

`Null=true` means whole SQL NULL; nil/empty Elements otherwise mean empty collections, with nil elements preserving element NULL. Object/Array implement Scanner/Valuer; Scan(nil) sets Null, and failed Scan leaves targets unchanged. Standard queries return format=1 JSON containing kind/schema/type/version/null, fields (name/type/value), or elementType/elementVersion/elements. Empty fields/elements may be omitted and normalize to empty containers. NUMBER uses decimal strings, RAW/BLOB base64, DATE/TIMESTAMP wall-time text, and nested objects/collections the same document format; order remains unchanged. Valuer supplies this lossless form; NamedValueChecker first preserves identity and sends native complex parameters, never JSON as server objects.

Native binding/results support Oracle mode using COMPLEX(160) and server-prepared binary results. Object headers carry schema/name/version; collection headers are anonymous plus element protocol type, with object-element names/versions. Text results lack a verifiable representation and return ErrUnsupported; client interpolation creates no complex literals and sent operations never replay to switch protocol. MySQL native complex inputs are also rejected explicitly.

Implemented field inputs: NUMBER/INTEGER/DECIMAL/NUMERIC, CHAR/VARCHAR/VARCHAR2, NCHAR/NVARCHAR2, RAW, DATE, nested objects/collections. Non-NULL TIMESTAMP/LOB field writes lack confirmed protocol and return ErrUnsupported. Built-in element headers currently support numbers, CHAR/VARCHAR/VARCHAR2, RAW, DATE only; NCHAR/NVARCHAR2/TIMESTAMP/LOB collections reject before sending even if empty/all NULL. Results add TIMESTAMP and bounded BLOB/CLOB/NCLOB materialization. Unsupported fields/elements explicitly fail, never unknown-byte fallback or fabricated success. These boundaries do not accept all complex types against real servers.

Before materialization, boundedly buffer the entire remaining response, including later row sets/hidden OUT; only then query dictionaries/read nested LOBs on the same session. Close/Exec draining shares this path; Close cleanup context reaches final complex OUT decoding, publishing no OUT on error. Root complex values share remaining budgets, cumulatively counting nodes, field metadata, ordinary values, and nested LOBs. Check budget before LOB reads, charge RAW/BLOB base64 expansion, and bound final JSON/full response again. Limits: depth 32, 65536 nodes, JSON 16 MiB, jointly constrained by smaller MaxMessageBytes/MaxLOBBytes; larger per-LOB limits cannot bypass totals.

NULL fields/elements each consume one node and obey depth limits; trailing NULLs cannot be accepted after prior subtrees exhaust budgets. Complex binary-row location, tree decoding, and native parameter encoding all check this; exact-limit values remain valid. [Stream boundary tests](../internal/wire/complex_test.go) cover both containers, location/materialization, 65536/65537 nodes, and following columns; [encoding regression](../complex_regression_test.go) and [fuzz tests](../complex_fuzz_test.go) add shared-budget/NULL-depth checks.

Descriptors reuse D13 type/attribute/collection/synonym query plans, preferring concrete types then private/PUBLIC synonyms and rejecting remote DB_LINK, cycles, or excessive dependencies. Cache lasts one business command on one physical session, keyed by mode/schema/name/protocol version, cleared per command/reset rather than reused across schema/permission changes. Frame/descriptor mismatch clears it and reparses buffered bytes only, never business SQL. Same-shape semantic type changes still require server-version/dictionary integration. Descriptor-query cumulative responses also obey MaxMessageBytes.

These capabilities have local protocol/standard database/sql regressions. `TestComplexTypes` is compiled only; Oracle/OBProxy type definitions, synonyms, versions, and original-transaction visibility remain untested live.


### 11.3 Errors

```go
type Error struct {
    Code     uint32
    SQLState string
    Message  string
}
```

Use `errors.As` for server Code/SQLState; transport, context, unsupported-capability, conversion, and batch errors retain their information and `Unwrap`. Do not force Oracle errors through a MySQL error table.

`Error.Message` preserves server text, which may itself contain SQL fragments. Default logs record only Code/SQLState and error category, never the full Message automatically.

### 11.4 Mixed Results, Per-Item Counts, and Execution State

Ordinary applications retain standard Exec/Query. Multi-statements, procedures, and executions needing complete summaries use a concrete result object without DB/Conn wrappers or another row-reading API:

```go
type ResultKind uint8

const (
    ResultRows ResultKind = iota
    ResultCommand
    ResultFailure
)

type ResultColumn struct {
    Catalog, Schema, Table, OriginalTable, Name, OriginalName string
}

type ResultInfo struct {
    Kind              ResultKind
    RowSetIndex       int // -1 for non-row results; row results start at 0
    RowsAffected      int64
    RowsAffectedKnown bool
    InsertID          string // Lossless decimal value, not limited to int64
    InsertIDKnown     bool
    Complete          bool
    Err               error
    Warnings          WarningSummary
    Columns           []ResultColumn // Row results only; labels/server origins in projection order
}

type ExecutionSnapshot struct {
    Results []ResultInfo // Original server-observable result order
    Done    bool
    Err     error
}

type Execution struct { /* private fields */ }

func Execute(ctx context.Context, session Session, query string,
    args ...any) (*Execution, error)
func (e *Execution) Rows() *sql.Rows
func (e *Execution) Snapshot() ExecutionSnapshot
func (e *Execution) Close() error
```

Session is defined in §14.1. Execute accepts only non-nil `*sql.Conn` or `*sql.Tx`, executing through QueryContext/package-private requests with standard-library-created Rows. It handles both ordinary procedure results and connection-bound output (§13.2), without separate Call/multiple-result entry points.

- `Rows()` always returns the same standard Rows using Next/Scan/NextResultSet. Successful command-only executions still return an empty Rows that must be consumed or closed.
- The parser reads command responses before the first row set, between row sets, and after the last, appending summaries in order. Summaries advance only when callers advance Rows or close execution; Snapshot never drives network reads.
- `UPDATE → SELECT → UPDATE` yields Command, Rows, Command summaries; only the middle item has RowSetIndex 0. Command counts remain separate, without fake row sets or counting SELECT rows as RowsAffected.
- Command results may have known zero counts. Known distinguishes zero from missing server information. Counts unrepresentable as int64 explicitly overflow. Generated IDs are known only from reliable protocol values, without follow-up queries/guessing.
- Procedure items follow protocol-observable results. Retain final CALL summaries with genuine count semantics; OUT/session-tracking control responses add no business items. Never invent per-statement counts from aggregates or indiscriminately drop trailing OK/zero counts.
- Rows items become Complete at full result boundaries, including successful drain even if callers did not read every row. Commands complete on full OK; independent explicit ERR may create Failure items. Mid-row failure leaves that item incomplete with its error and invents no later items.
- Snapshot is concurrency-safe and independently copies summaries and Columns slices. Done means execution ended, including failure; success requires Done and nil Err. Failure preserves confirmed prefixes, origins, and original errors; summaries cannot establish whether unobserved statements executed.
- Pre-send parameter/configuration failure may return nil,error. After sending starts, direct QueryContext failure still returns non-nil Execution plus error with confirmed information; Rows may be nil. ErrBadConn must not hide partial results.
- Close is idempotent, boundedly closes/drains Rows, and finalizes summaries, OUT, and resource publication. It returns read/finalization/cleanup errors even if Rows auto-closed. Cancellation/timeout/cleanup failure marks incomplete results, cleans unpublished resources, and discards unsynchronized sessions. After stopping iteration, call Close and inspect Execute error, Rows.Err, Close error, and final Snapshot.
- Snapshot copies driver memory only and may run concurrently with iteration; it does not query standard Rows. Standard-library transaction cancellation is not exposed to drivers. When explicit Commit/Rollback auto-closes unread Rows, Execution.Close reads Rows.Err and merges cancellation into final summaries while retaining previous driver errors/original timeout causes. Done means driver finalization finished; Close is the boundary for final summaries including standard-library errors.
- `Rows().Close()` uses the same finalization. Published Cursor/LOB lifetimes (§13.2) survive Execution.Close. Reading/closing Rows retains standard-library concurrency contracts; only Snapshot supports concurrent observation, not concurrent Next.
- At MaxResultItems, stop unbounded allocation, return a resource-limit error, retain recorded prefixes, and perform bounded cleanup. Never assume later items did not execute or silently discard them and report success.
- Publish column origins atomically after complete verified row-set metadata. Incomplete metadata, hidden OUT, commands, and Failure have no Columns. Execution metadata cumulatively obeys MaxMessageBytes using conservative row-set memory estimates including structs/extended descriptors. Excess preserves prior results and discards connections. Ordinary Query Rows stores no extra summaries.

Ordinary `sql.Result` still exposes standard LastInsertId/RowsAffected only. Multi-statement Exec returns the final command's known count/ID; without a usable command result, methods return explicit errors rather than adding counts across statements. Use Execute for per-item information; driver.Result methods cannot pass through standard-library wrapping.[G10]

Count policy is fixed independently of the reading method:

```go
type RowsAffectedMode uint8

const (
    MatchedRows RowsAffectedMode = iota
    ChangedRows
)
```

Config.RowsAffectedMode defaults MatchedRows. MySQL CLIENT_FOUND_ROWS chooses UPDATE matched/changed counts, matching Java defaults.[J13] INSERT/DELETE/upsert retain native server counts under that bit rather than normalizing to 1. Oracle retains native matched-row semantics without ChangedRows emulation. Explicit ChangedRows is rejected during post-identification initialization before business execution.

Standard Exec, Execute, ExecBatch, and cursor DML share effective policy; unknown, zero, and overflowing counts differ. Updatable cursor/emulated BLOB existence checks must follow §14.4/§15, never equating unchanged ChangedRows updates with nonexistent records.

## 12. Connections, Transactions, Retries, and Cancellation

### 12.1 Connection State and Concurrency

Core states distinguish at least `ready`, `commandRunning`, `rowsActive`, `transactionActive`, `broken`, `closed`, with separate XA/cursor/LOB state. These are composable constraints, not forced into a mutually exclusive enum.

`database/sql` serializes Conn calls. Drivers synchronize only cancellation callbacks, closure, and connection-bound extensions as necessary, without persistent per-query worker goroutines or command multiplexing.

Standard queries hold connections until Rows finishes. Extension cursors allow resource-matching commands only at fully consumed fetch-response boundaries; unread responses never interleave with other commands.

### 12.2 ErrBadConn and Uncertain Outcomes

| Scenario | Return and handling |
| --- | --- |
| Connection known invalid on entry, operation not started | May return `driver.ErrBadConn` for standard-library reacquisition |
| Connection establishment fails | Return specific connection errors; Connect/Open do not return ErrBadConn |
| Parameter/type/packet-size validation fails before sending | Return specific errors; reuse synchronized connections |
| Network error/lost response after sending starts | Return original transport errors and mark broken; never wrap as ErrBadConn |
| COMMIT response lost | Return `ErrCommitUnknown`, including cancellation/timeouts after full write before response read; retain underlying context errors without recommitting or rolling back and claiming reversal |
| Server error received | Return server error; connection retention depends on synchronization/session state |

Discarding bad connections and allowing retries are independent decisions. Validator discards connections; never convert every error to ErrBadConn merely to discard them.[G01][G02]

### 12.3 Local Transactions and Savepoints

- `BeginTx` validates isolation, read-only options, and mode capabilities; no silent isolation downgrade.
- All transaction operations retain the original physical session, without transparent rerouting, mode changes, or reconnection.
- Commit/Rollback restores driver-modified transaction settings; failed restoration discards connections while retaining transaction outcomes and restoration errors separately.
- `Savepoint`, `RollbackTo`, etc. operate on existing caller `*sql.Tx`, validating/mode-quoting identifiers and strictly encoding `Session.Charset`. Unrepresentable names fail before sending without invalidating the transaction. Unsupported RELEASE semantics return explicit capability errors.
- Savepoints monitor both call and BeginTx contexts. Cancellation/timeouts interrupt blocked responses and preserve causes; possible sending discards sessions without additional rollback/replay. Cooperate with standard Rollback on transaction cancellation without claiming server rollback if networking has failed.

### 12.3.1 D32 Autocommit, Read-Only Transactions, and Role Policies

Go uses standard `sql.Tx`; four Java options map as below without global autocommit switches or connection `SetReadOnly`. Matching Java DSN names are strictly rejected.

| Java option/default | Go equivalent and behavioral differences |
| --- | --- |
| autocommit=true | New connections/ChangeUser must finish initialization with autocommit enabled and no active transaction; Variables/InitSQL disabling it fail initialization. Use BeginTx normally. Manual control requires sql.Conn with mode-native SQL and completed COMMIT/ROLLBACK on that Conn. Never SET autocommit=0 through sql.DB and assume later calls share its session |
| assureReadOnly=true | `BeginTx(ctx, &sql.TxOptions{ReadOnly:true})` sets server transaction attributes on the current socket. MySQL uses START TRANSACTION READ ONLY; Oracle disables autocommit then SET TRANSACTION READ ONLY. Preserve write-rejection codes/SQLSTATE. Do not parse business SQL for read-only classification or treat role tags as authorization proof |
| failOnReadOnly=false | Java may fall back to read-only endpoints when primary is unavailable. Go explicit Routing.Role filters strictly with no unknown/opposite-role fallback. RoleAny imposes no role constraint and proves no writability. RoleReadOnly locally rejects BeginTx ReadOnly=false but does not automatically wrap ordinary SQL in read-only transactions. Applications hold separate standard sql.DBs for read/write services |
| oracleChangeReadOnlyToRepeatableRead=false | Do not migrate Java's optional Connection.setReadOnly reinterpretation as RepeatableRead. Go ReadOnly always means read-only transactions; Oracle explicitly rejects LevelRepeatableRead. Supported Oracle levels: default, ReadCommitted, Serializable. Reject ReadOnly+ReadCommitted; ReadOnly+Serializable uses Oracle read-only snapshots, whose real isolation awaits target verification |

MySQL supports default, ReadCommitted, RepeatableRead, Serializable, setting explicit levels before START TRANSACTION. Oracle read/write transactions explicitly requesting ReadCommitted/Serializable send SET TRANSACTION ISOLATION LEVEL. Unknown/unsupported combinations fail before transaction-setting SQL.

Manual autocommit=0 on fixed Conn is not reset between calls; BeginTx rejects nesting or autocommit-disabled sessions. Managed reuse first ROLLBACKs pending transactions from server state, then restores autocommit=1; reversing order could commit pending work. Standard transaction end also restores autocommit; failures return ErrTxCleanup and discard sessions. Full reset still physically replaces connections. Managed does not promise full restoration of arbitrary SQL-changed session state.

Source evidence: [four Java defaults](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/util/DefaultOptions.java#L287), [setReadOnly reinterpretation](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseConnection.java#L1215), [assureReadOnly settings](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L2068). `TestTransactionNative*`, existing Tx/Reset/Role tests, and external `ExampleNewConnector_readOnlyTransactions` provide local protocol/compile evidence, not live privilege, snapshot-isolation, or primary/replica-failure acceptance.

### 12.4 Session reset

Default Managed restores driver-managed settings and checks unfinished transactions, unread responses, and abnormal resources. It cannot recognize every arbitrary-SQL temporary-table/package/user-variable change; applications relying on these use `sql.Conn` or transactions to pin sessions.

Full performs verified server reset at pool-reuse boundaries and restores initialization baseline. Without reliable reset, discard old connections and let Connector create sessions. Old statement IDs are lost; internal generations invalidate them and standard sql.Stmt reparses on next use.

When `ResetSession` cannot restore baseline, mark invalid then return `driver.ErrBadConn`: the next business operation has not started, allowing safe reacquisition. Do not return only ordinary reset errors and assume standard-library reuse stops.

Never reset arbitrarily between fixed `sql.Conn` calls or within transactions. Guessing first SQL words cannot support claims that all session state was cleaned.

### 12.5 Context, Timeouts, and Closure

- Every network operation considers context deadlines and configured I/O/cleanup budgets.
- Check cancellation before operations; combine deadlines and `context.AfterFunc` to interrupt blocked I/O.
- Default cancellation closes/invalidates underlying connections so residual responses cannot become the next response.
- If AfterFunc stop returns false, coordinate callback completion so old callbacks cannot close reused connections during new requests.[G06]
- QueryContext context lasts through Rows reading/closure; Connect/Prepare contexts never persist in long-lived connections/statements.
- Rows.Close, Stmt.Close, and Conn.Close network cleanup is bounded. Close undrainable sessions, avoiding unlimited drains or unbounded Background network calls.
- Explicit CancelQuery provides active server `KILL QUERY` using previously obtained identity/generation. Auxiliary channels use caller-configured Connector and identical authentication/TLS policy. Separate from default context cancellation; successful KILL does not prove the original operation never executed.

Java `enableQueryTimeouts` defaults true and controls shared Statement.setQueryTimeout timers; [OceanBaseStatement.executeQueryPrologue](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseStatement.java#L271) also depends on server timeouts/batch paths. Go uses per-ExecContext/QueryContext contexts without global disabling, hidden server-timeout prefixes, or automatic KILL. Background has no business deadline but retains read/cleanup budgets. PrepareContext covers only preparation; execution needs its own context, while QueryContext covers Rows lifetime. Pre-send timeout preserves sessions; possible sends discard them without replay-triggering ErrBadConn and do not prove server abort/rollback. TestStatementContextDeadline covers fixed sql.Conn/sql.Stmt, both modes/three streams, both preparations, Ping after pre-expiry, execution deadlines before ReadTimeout, and unknown outcomes excluded from pools. Existing TestExecutionTxDeadline/TestTextNextResultUsesQueryDeadline cover transaction/Rows deadlines.

### 12.5.1 D32 Implemented Network Read Timeout

`Config.ReadTimeout` / DSN `readTimeout` uses Go durations, zero disabled, rejecting negative/invalid values; e.g. `readTimeout=5s`. It corresponds to pinned Java `socketTimeout` SO_TIMEOUT without accepting bare milliseconds or that historical alias. Java defaults 0 normally and 10000 ms in Aurora; Go defaults disabled across routes/modes, independently of ConnectTimeout/business contexts.

After built-in/custom DialContext returns net.Conn and before authentication/TLS, attach the underlying read budget. Handshake, TLS, initialization, ordinary/compressed/OB20, preparation, Rows, LOB, ChangeUser, and cancellation auxiliaries share it. Each underlying Read sets the earlier of now+ReadTimeout and request deadline, restarting on the next Read. Idle/caller Rows processing does not consume future budgets; TLS/decompression-buffer reads create no new network wait. Concurrent pipeline-write deadline refresh must not extend an already blocked read; clearing external deadlines cannot remove this cap.

This is not a whole-query/logical-message deadline: ongoing bytes may repeatedly reset it, so full commands still need caller contexts. Writes, dialing, discovery, credential/auth callbacks, and caller Readers retain separate contexts/budgets. TLS control-record writes also keep write budgets. Default 0 adds no transport wrapper, package, dependency, or goroutine; cancellation reuses existing AfterFunc/closure coordination.

Read timeout retains network errors for errors.As net.Error/Timeout rather than pretending context.DeadlineExceeded before context expiry. Earlier context expiry/cancellation retains its original error. Failure discards sessions without pool reuse or replay-triggering ErrBadConn for written operations. Post-write outcomes are unknown; batches retain known/unknown states. Initial handshake transport failures retain bounded address switching; initialization failures do not switch. Both retain one total establishment budget.

Pinned Java evidence: DefaultOptions SOCKET_TIMEOUT (L117–L127) and AbstractConnectProtocol.createSocket setSoTimeout (L255–L257). TestReadTimeout* / TestDSNReadTimeout and offline external Example cover both modes/three streams, handshake/initialization/TLS, Rows, batches, per-read resets, context precedence, and concurrent refresh. Four-target TestReadTimeoutSessions requires read-only slow-SQL fixtures and is compiled only. Live server execution, proxy buffering, and queries continuing after disconnect remain unvalidated.

## 13. Batching, Procedures, OUT, and RETURNING

### 13.1 Standard Procedure Calls

Oracle mode retains native anonymous blocks/named parameters:

```go
var out string
_, err := db.ExecContext(ctx,
    "begin demo_proc(:id, :result); end;",
    sql.Named("id", int64(42)),
    sql.Named("result", sql.Out{Dest: &out}),
)
```

INOUT uses `sql.Out{Dest: &value, In: true}`. Validate non-nil writable Dest before sending, read initial values, then write back after consuming all required responses.[G07]

Use small `Out` extension values for types not inferable from Go pointers, specifying database types/required lengths/precision. Prefer direct sql.`Out` for inferable ordinary types.

Ordinary OUT accepts writable pointers to bool, signed/unsigned integers of all widths, float32/64, string, []byte, time.Time, and sql.Scanner, with integer/precision bounds. NULL requires nullable targets such as sql.Null[T] or package nullable Scanners, never silent zero values. Reject unsupported targets before sending. Object/Array use their Scanners; handles do not use scalar assignment.

MySQL mode uses native CALL/function syntax and capability-appropriate OUT paths. Servers requiring intermediate variables use one fixed session and clean them up, never cross-connection operations across sql.DB.

Built-in scalar outputs finish responses/temporary conversion before all assignments, preventing partial writes on conversion failure. User Scanners may have irreversible effects: failure returns clear errors noting earlier targets may have changed, without arbitrary Scanner atomicity. Batch OUT retains per-item states.

D14 entry points and boundaries:

```go
type Out struct {
    Dest any
    In bool
    Type string
    Length int64
    Precision int64
    Scale int64
}
```

- Type is a closed set of SQL scalar names, ignoring edge whitespace/case; empty infers from destination. Custom Scanners must specify Type and implement driver.Valuer for INOUT. Number/NString/Raw/RowID/temporal/interval/sql.Null[T] preserve identity; ordinary unsigned inputs support full uint64.
- Length means text characters or binary bytes according to declared type; 0 adds no constraint. Precision/Scale enforce lossless NUMBER/DECIMAL ranges; Precision=0 requires Scale=0. Oracle maximum precision 38, scale -84..127; MySQL precision 65, scale 0..30 and no greater than precision. Overflow errors without truncation/rounding.
- Type/sizes constrain client mode/values. Pure OUT sends NULL per Java; SQL/routine parameters determine server types. Explicit SQL casts force database types. Later tasks implement handle/LOB/OBJECT/ARRAY binding; scalar entry points reject those targets before sending.
- Oracle OUT/INOUT/RETURNING requires actual ob_version() >=2.2.76 and COM_STMT_PREPARE_EXECUTE; old/unknown versions return ErrUnsupported before business sends, including PrepareClient. Consume parameter/column descriptors, hidden output, and mandatory trailing OK/ERR; publish after temporary statement close. Explicit server statements may reuse IDs.
- MySQL uses randomly named per-call private session variables, performing initialization, original SQL, output SELECT, and clearing on one connection. Main ordinary results remain streaming; auxiliary commands add no Execution items. Substitution shares strict current-sql-mode lexical/literal encoding and rejects unsafe SQL before sending. Cleanup failure discards sessions and preserves errors.
- Query/Execute output becomes available after final response consumption/Close; Exec finishes before return. Recheck cancellation after cleanup. Server failure, pre-publication cancellation, or output-shape errors leave built-in targets untouched. Repeated logical OUT physical values must agree and assign once; different logical OUT targeting the same destination is rejected.
- ErrOut identifies validation/conversion failures. OutError.Parameter is the logical SQL bind position; Partial=true indicates possible custom Scanner effects. Use errors.As for details, retaining underlying Scanner errors. Finish all built-in conversions first, then assign in first SQL occurrence order; custom Scanner failure stops subsequent publication.
- Recheck operation/transaction context before each assignment. If custom Scanner cancels within its callback, even successful Scan stops later scalar/Scanner assignments; resources retain unified pre-publication checks and context errors. Already published targets/Scanner effects are not rolled back; no cross-target atomicity is promised during callbacks. [TestOutCancelStopsPublication and TestOutTransactionCancelStopsPublication](../out_context_test.go) cover public and transaction assignment boundaries in both modes/three streams.

Layouts have been checked against pinned Java and synthetic peers; live server/OBProxy routine/RETURNING behavior still needs Compatibility Matrix integration.

### 13.2 REF CURSOR and Connection-Bound Output

Go 1.26 drivers cannot construct `*sql.Rows` for `sql.Out.Dest`. Ordinary procedure results, summaries, and one/multiple output cursors share Execute (§11.4):

```go
type CursorOut struct {
    Dest **Cursor
}
```

For example, pass `sql.Named("items", oceanbase.CursorOut{Dest: &cursor})` alongside ordinary `sql.Out` into Execute. Rules:

- Execute accepts non-nil `*sql.Conn`/`*sql.Tx` only, using standard-library-created Rows rather than internal standard-library objects.
- Ordinary results use Execution.Rows() Next/Scan/NextResultSet; Snapshot supplies summaries. Internal OUT control responses are not ordinary result sets.
- Assign outputs/cursor handles only after ordinary results, OUT, and final responses are fully consumed. Cursor destinations must initially be nil; multiple CursorOuts yield independent cursors.
- Without ordinary results, explicitly Execution.Close for bounded drain. Outputs appear only on successful completion. Read/finalization failure cleans all unpublished cursors and returns no usable handles; see §11.4 error checks.
- Published Cursors survive procedure Rows/Execution closure and require individual Close. They retain fixed Session, resource IDs, connection/transaction generations, never raw driverConn.
- If server cursor lifetime requires parent statements, core resource leases retain parents until the final cursor closes. Transaction end/session invalidation immediately invalidates all handles.
- Procedure response completion ends its cancellation callback; later Fetch receives new contexts, not finished procedure contexts.
- Only Execute accepts CursorOut. Ordinary DB/Conn/Tx Exec/Query reject it before sending; `sql.Out{Dest: &rows}` is not a REF CURSOR alternative.
- Streaming LOB output likewise requires Execute with explicitly typed Out to `**Blob`/`**Clob`, sharing publication, lease, and session rules. Ordinary sql.Out LOB reads bounded materialized content.

A queried REF CURSOR column may still use standard child `driver.Rows` scanning. Such child Rows closes with its parent, unlike explicit Cursor handles surviving procedure Rows closure; each follows its lifetime contract.

#### Current Implementation and Boundaries (D20)

- Oracle mode and known OceanBase >=2.2.76 use native PrepareExecute hidden OUT/final responses. MySQL/old/unknown versions return ErrUnsupported before business sends. CursorOut and explicit BLOB/CLOB/NCLOB Out reject INOUT, length/precision hints, or non-nil initial handles; SQL NULL stays nil.
- REF CURSOR uses binary type 163 with four-byte little-endian cursor ID. Nine-byte COM_STMT_FETCH uses separate cursor IDs and establishes metadata on first fetch. Close releases cursor IDs, then parent statements when the last output resource closes. Blob/Clob reuse native locators/streaming APIs; handles are not bound by ordinary total LOB materialization limits, but individual protocol/read operations remain bounded.
- Finish built-in conversions and custom Scanners before publishing all resources together. Scanner failure may have its own effects, but no handles are published. Recheck context/generations before publication; cleanup releases unpublished leases. Trailing server errors prevent publication; corrupt/unsafely recoverable responses discard connections.
- Resource OUT captures connection/session/transaction generations before prepare, encoding, or upload and rechecks before main sends/after final responses. Invalidity returns ErrResource without scalar/handle assignment, retaining auxiliary and generation errors. Complete simple responses still register cleanup-only child leases; releasing children/parents preserves usable sessions. Invalid generations stop dictionary queries/materialization of delayed mixed OBJECT/ARRAY binary rows. Ordinary resource results also check after full buffering, dictionary resolution, and each materialization stage; invalidity discards connections with unmaterialized children. Scalar-only OUT procedures may end their own transactions.
- Ordinary REF CURSOR columns require Oracle binary rows. After complete parent responses, boundedly fetch children on the original session, close server cursor IDs, then pass standard child driver.Rows to database/sql. Every layer shares MaxMessageBytes, counting rows, full metadata, and nested content; maximum depth 16 with cyclic-ID rejection. Excess errors. Standard parent Rows closes scanned children; use CursorOut for lifetimes beyond procedure Rows/batched fetch.
- Child Rows uses the parent's caller-iteration context; ending internal network contexts does not cancel child reads early. Static cursor caches count complete nested snapshots; each Fetch creates independent child-read state with its own context rather than reusing previously closed children.

Local protocol/failure tests remain distinct from live acceptance. `TestProcedureResources` includes both-mode support/rejection, multiple cursors, temporary BLOB/CLOB, failed publication, and standard child Rows. It is compiled only; live procedure syntax, locators, versions, and OBProxy behavior await integration.

### 13.3 RETURNING and Generated Keys

- Oracle uses `RETURNING ... INTO ...` with output binding; corresponding Java server APIs explicitly require PrepareExecute. D14 implements minimum protocol/version/capability gates. Ordinary separate prepare/execute is not equivalent without evidence; unsupported cases fail before business sends.[J17]
- Nonzero server OK insert IDs support LastInsertId. Zero means no usable generated ID and returns ErrUnsupported, regardless of successful affected counts. Unrepresentable int64 IDs overflow. RowsAffected/LastInsertId check overflow independently; Execute InsertID retains full decimal values. Text/prepared/multiple-result paths share this rule without guessing primary keys from SQL.
- Oracle does not query global sequences or guess keys merely to supply LastInsertId; callers explicitly RETURNING required columns.
- Java generated-column selection maps to explicit RETURNING bindings/small returned-column helpers without JDBC constants.

### 13.4 Batch Interface

```go
type Execer interface {
    ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ExecBatch(ctx context.Context, exec Execer, query string,
    rows [][]any, opts BatchOptions) (BatchResult, error)
```

`*sql.DB`, `*sql.Conn`, and `*sql.Tx` are accepted directly. Callers control atomicity through Tx; helpers never secretly begin/commit transactions.

BatchResult returns input-indexed affected counts or unknown states; BatchError identifies failed/confirmed items. BatchOptions explicitly chooses stop/continue, default stop-on-error.

Core strategies depend on mode, transaction, parameter types, and capabilities: sequential server execution, multi-send, SQL rewriting, bulk, Oracle array binding. Bound packet sizes, total parameters, and chunk rows. No strategy changes transaction boundaries, output order, or error statistics. Protocols unable to provide per-item counts report success/count unknown, never fabricated counts.

Use sequential execution without equivalent semantics. For example, autocommit cannot rewrite partially successful separate INSERTs into one wholly failing statement; unknown counts alone do not make it equivalent. Sequential paths retain stop/continue and uncertain-outcome rules, never replaying sent items.

Long-data Readers belong to one execution and are never automatically rewound/retried. Client rewrites need full lexical validation; arbitrary INSERT is not automatically safe to rewrite.[J09]

Implemented D22 interfaces and selection rules:

```go
type BatchOptions struct {
    ContinueOnError bool
    Strategy BatchStrategy
    ChunkRows int
    ChunkParameters int
}
type BatchItem struct {
    Status BatchStatus
    Started bool
    RowsAffected int64
    RowsAffectedKnown bool
    Err error
    InsertID string
    InsertIDKnown bool
}
type BatchResult struct {
    Items []BatchItem
    Strategy BatchStrategy
    Chunks []BatchChunk
}
type BatchChunk struct {
    Start, End int // Input range [Start, End)
    BatchItem      // Aggregated server result
}
type BatchError struct {
    Index int
    Completed int
    Err error
}
```

- Strategy is a preference: BatchAuto (zero), BatchSequential, BatchMultiSend, BatchRewrite, BatchBulk, BatchArrayBinding, BatchMultiValues. Append enums without changing existing values. Results report actual strategy; Bulk/ineligible requests fall back Sequential. ContinueOnError defaults false, sending no later items after failure. True still stops on cancellation, corruption, disconnect, or uncertain outcomes.
- ChunkRows=0 uses 128; ChunkParameters=0 uses 65535. Negative limits/invalid strategies fail before sending. Multi-send bounds rows, expanded physical parameter slots, and total encoded content; each command obeys MaxMessageBytes/value limits. Sequential execution limits each row without aggregating packets.
- *sql.DB pins one *sql.Conn for the entire batch. Native Conn/Tx holds the standard connection lock through one ExecContext private dispatch. No hidden transaction, commit, auxiliary session, or whole-batch retry. Custom Execer runs sequentially and owns physical-session/transaction guarantees; unknown errors conservatively become BatchUnknown and stop.
- Status: BatchNotExecuted, BatchSucceeded, BatchFailed, BatchUnknown. Started only means business sends may have begun, not prepare/upload/probing. After submitting rewritten multi-statements, server-confirmed unexecuted suffixes have NotExecuted/Started=true; unsent later chunks remain NotExecuted/false. Failed means a known error, not absence of side effects; Succeeded does not mean committed. Cleanup/cancellation after complete success preserves confirmed status/counts and returns Err rather than converting success to unknown.
- RowsAffectedKnown=false means no safely representable count, including uint64 overflow of int64; do not infer per-item numbers. BatchError.Index is the first errored zero-based input index, or -1 for finalization errors without an input. Completed counts all confirmed successes, including continued items. errors.Is/As retains SQL/context/cleanup chains.
- Auto/MultiSend requires ContinueOnError=true, at least two rows, PrepareServer, explicit scalar-only inputs, and one INSERT/UPDATE/DELETE/REPLACE. Reuse normal validation before shared prepare; local invalid items or Oracle physical slots exceeding chunk limits fall back the whole batch sequentially. Shared-prepare failure belongs to first item; later items follow sequential continuation. Lex under both quote interpretations, rejecting semicolons/executable comments/RETURNING/CALL/queries/session SQL. OUT, long parameters, complex values, custom Valuers fall back while retaining binding/output/one-consumption rules. Resource outputs remain Execute-only.
- Multi-send prepares once, concurrently writes independent COM_STMT_EXECUTE commands and reads responses in order, avoiding write-all-before-read deadlock under bidirectional backpressure. Reset sequence per command; confirm only complete OK/ERR. Server ERR may continue. Disconnect/cancellation/corruption/extra results discard connections, retain confirmed prefixes, mark only possibly sent/unconfirmed items Unknown, keep unsent chunks NotExecuted, and wait for writer exit.

Native optimization evidence and conservative choices: pinned Java [AbstractQueryProtocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L1287) explicitly says OceanBase lacks COM_STMT_BULK_EXECUTE; MariaDB version checks do not prove OceanBase support, so Bulk stays sequential. Multi-VALUES changes partial success, triggers, and error boundaries and uses its own explicit policy below. Oracle columnar array layout/save-exception responses have pinned Java/OceanBase source evidence; source/local tests do not replace live acceptance.

Public generated-key and multi-VALUES contracts:

- BatchItem.InsertID/InsertIDKnown preserves nonzero IDs from complete confirmed per-item responses as full uint64 decimal strings. Sequential/multi-send/multi-statement/single-item array chunks share recording. Custom Execer retains only valid positive int64 LastInsertId. Missing IDs, failures, and multi-item aggregates cannot infer IDs or manufacture auto-increment ranges.
- Explicit BatchMultiValues combines simple INSERT INTO [schema.]table [(columns)] VALUES (?,...) only for MySQL, at least two rows, ChunkRows>=2, scalar whitelist, and enough physical slots. Only placeholder tuples qualify. IGNORE/REPLACE/expressions/existing multiple tuples/ON DUPLICATE KEY/RETURNING/multi-statements/Oracle/OUT/streams/custom Valuers fall back. No multi-statement permission required; defaults/BatchRewrite stay unchanged.
- ChunkRows, ChunkParameters, and encoded MaxMessageBytes (including COM_QUERY/prefix/commas) bound each chunk using existing safe charset/literal encoding. Confirm valid prefixes before reporting later local errors; reparse session SQL mode per chunk. No implicit Begin/Commit or sent-chunk replay.
- Chunks records [Start,End), aggregate state/count/ID/errors only for submitted multi-VALUES commands. Single-item chunks may also expose per-item counts/IDs; multi-item Known flags are false. Succeeded means its command succeeded, not one affected row each or commitment. Server ERR marks chunk items Failed without guaranteeing no side effects; explicit continuation requires complete server errors/healthy sessions. Disconnect/cancellation/corruption/extra results mark unconfirmed chunks Unknown and stop without retry.

Restricted multi-statement rewriting is implemented only for explicit BatchRewrite, ContinueOnError=false, at least two rows, ChunkRows>=2, MySQL, AllowMultiStatements=true, and negotiated CLIENT_MULTI_STATEMENTS. Accept scalar-whitelist single INSERT/UPDATE/DELETE/REPLACE only; lexically reject semicolons/executable comments/RETURNING. Custom Valuers/OUT/streams/complex objects remain sequential. Explicit rewrites use existing client-literal encoding without changing ordinary PrepareMode or implicitly enabling permissions. Oracle/ContinueOnError=true remain sequential.

- Before each combined chunk, read @@SESSION.ob_enable_batched_multi_statement on the same session; combine only explicit 0/OFF. Unknown-variable error 1193, NULL, or other values fall back before the first chunk; other server errors propagate. If settings change later, send remaining items as single text commands without replaying prefixes. Probe disconnect/corruption/cancellation fails without business sends. Driver never changes this variable; callers may explicitly use Session.Variables.
- Pinned Java [ComQuery.sendRewriteCmd](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComQuery.java#L117) concatenates semicolons. Pinned OceanBase [ObMPQuery](https://github.com/oceanbase/oceanbase/blob/e78e4568b663baeef5497ce1a69868c59292bf69/src/observer/mysql/obmp_query.cpp#L226) stops after the first parse/execution error, and [server aggregation optimization](https://github.com/oceanbase/oceanbase/blob/e78e4568b663baeef5497ce1a69868c59292bf69/src/observer/mysql/obmp_query.cpp#L401) may return aggregate responses. Hence probe per chunk rather than relying only on versions; this does not accept arbitrary historical servers/proxies.
- Reuse safe interpolation/string/binary literals, encoding SQL once in session charset. Separate statements with newline/semicolon/newline so trailing comments cannot swallow successors. After probing, revalidate boundaries/placeholders using latest SQL mode; reject newly introduced boundaries before sends. Chunk by ChunkRows/ChunkParameters/MaxResultItems/encoded MaxMessageBytes including command/separators.
- Local invalid parameters never hide earlier server errors: send valid prefixes first, reporting later local errors only after prefix success. Read strict per-item OK/ERR/MORE_RESULTS, retaining actual counts or unknown on overflow. ERR preserves successes/failure/confirmed unexecuted suffix. Missing/extra responses, corruption, disconnect, or cancellation discards sessions, marking only submitted unconfirmed items Unknown. No later chunks/replay; cleanup/cancellation preserves confirmed results/full chains.
- Standard/compressed/OB20 reuse serial streams without pipelines. Callers retain original sql.Tx commit/rollback. Combining independent DML does not guarantee whole-batch atomicity; explicitly use transactions if needed. Additional per-chunk probe round trips mean local encoding tests cannot establish throughput gains.

Oracle native array binding is implemented locally:

- Requires explicit BatchArrayBinding, ContinueOnError=true, at least two rows, PrepareServer, Oracle, known OceanBase >=4.2.1, caller writable sql.Tx, and autocommit off. Retain single-DML lexical/scalar rules. No-parameter SQL/MySQL/read-only/no transaction/old or unknown versions/custom Valuer/OUT/long/complex inputs stay sequential. uint/uint64 also stay sequential because anonymous array headers lack unsigned flags; narrower unsigned values retain safe scalar conversion.
- Pinned Java [ComStmtExecute](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComStmtExecute.java#L165) defines anonymous COMPLEX headers, empty schema/name/version, element type, columnar row count, and bit-0-based NULL maps. Pinned [OceanBase v4.2.1](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/observer/mysql/obmp_stmt_execute.cpp#L529) defines standard COM_STMT_EXECUTE array=8/save-exceptions=16, combined flag 24, distinct from combined PrepareExecute batch flags. The gate follows checked implementation, not extrapolated older versions.
- Prepare once on the same session, then standard COM_STMT_EXECUTE per chunk with full type declarations, without polluting scalar reuse/cache. Chunk by physical slots, ChunkRows, MaxResultItems, MaxMessageBytes, and 65535 rows; type changes including NULL start new chunks. Scalars retain charset/exact-number/temporal/value validation. Size accounting is linear; encode one full message per chunk. Existing SQL-checksum policy applies.
- [Server prerequisites](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/observer/mysql/obmp_stmt_execute.cpp#L255) demand writable DML, autocommit off, anonymous arrays. [Ordinary aggregation gates](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/observer/mysql/obmp_stmt_execute.cpp#L1593) exclude this standard path; never probe/change ob_enable_batched_multi_statement or implicitly Begin/Commit.
- [Saved-exception responses](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/observer/mysql/obmp_stmt_execute.cpp#L369) contain binary sql_no/error_code/error_message columns, zero-based positions, native legacy EOF with MORE_RESULTS, then aggregate OK. Metadata also uses legacy EOF even with ordinary EOF deprecation. Strictly validate names/types, no OUT, ranges/duplicates/codes/NULL/charset/EOF/final status. Metadata/details/final OK cumulatively obey MaxMessageBytes.
- Publish all chunk successes/failures only after final OK, then continue. Details lack SQLSTATE, so Error.SQLState is empty. Aggregate multi-success counts cannot be distributed: RowsAffectedKnown=false; representable single-item counts remain actual. ContinueOnError=false remains sequential to retain prefixes and avoid later sends.
- Top-level ERR cannot establish completed inputs: preserve its chain, mark submitted chunk Unknown, discard connection, retain later NotExecuted. Missing/corrupt/extra results/disconnect/cancellation likewise never replay. Cancellation/cleanup after complete OK preserves confirmed per-item states. Boundedly close original statements; caller controls transaction.

Local failure peers cover states/counts/original transactions/OUT/streams/chunks/multi-send/cancellation/no replay. Four-target `TestBatchExecution` provides partial-success/strategy/original-rollback source compiled only; live SQL/server/OBProxy behavior remains unaccepted.

### 13.5 One-Shot Long Parameters (D21)

Public `*LongData` is an IN parameter for standard `ExecContext`/`QueryContext`, `Execute`, server `Stmt`, or `OpenCursor`, retaining session/transaction. Ordinary explicit PrepareClient returns ErrUnsupported before Reader consumption. OUT already requires native protocol, and mixed long parameters retain that path.

```go
input := &oceanbase.LongData{
    Reader: file, Length: -1, MaxBytes: 256 << 20,
}
_, err := tx.ExecContext(ctx, "INSERT INTO files(id, content) VALUES (?, ?)", id, input)
```

- Callers always close Readers. Nil and typed-nil pointer/function/map/slice/channel Readers return ErrLongData before prepare/upload without consumption; non-nil empty Readers upload empty streams. Length is exact source bytes, -1 through EOF, 0 empty-only. Known lengths probe at most one extra byte for excess; shortage returns io.ErrUnexpectedEOF, never truncated success.
- MaxBytes separately caps source/converted bytes per input; 0 uses Limits.MaxLOBBytes. Explicit increases allow streams beyond ordinary materialization limits, while each chunk obeys MaxMessageBytes. Oracle hex expansion does not count as content but packets remain bounded.
- Text=false sends binary. Text=true incrementally converts UTF-8 through Session.Charset, rejects invalid/unrepresentable characters, and retains incomplete code points across chunks. Never mix NCharset bytes into ordinary text streams. Server CLOB/NCLOB conversions still need live validation.
- Upload start atomically consumes objects; neither success nor failure permits reuse, copying, seek, or rewind. Repeated Oracle named positions receive the same chunks without rereading sources. Caller reconstruction grants no driver replay authorization.
- MySQL uses COM_STMT_SEND_LONG_DATA(24), no per-chunk response; Oracle known >=2.2.76 uses COM_STMT_SEND_PIECE_DATA(162) with OK per chunk. Oracle binary is hex-encoded, text uses session charset, execute headers use STRING(254); both omit corresponding execute value bodies. Oracle minimum reuses capability gates, not Java option-introduction versions.
- Ordinary execution, forward/sensitive cursors, Oracle OUT pre-prepare+command 161, and MySQL OUT session variables/server CALL stream placeholders upload/execute within one gate. Prepare/upload never masquerades as executed business results in Execution.
- Reader errors/limits/invalid text/chunk errors/partial writes/cancellation invalidate upload sessions and clear uncertain server long data. Cancellation after upload before execute also discards sessions. Reader errors retain errors.Is; special ErrBadConn never reaches database/sql as a retry signal. No second business execution.
- Never start abandonable background goroutines for arbitrary blocked Readers. Callers must make Readers return on cancellation through their own contexts/closure; drivers cannot close borrowed sources. Do not concurrently mutate LongData fields/Reader during execution.

Evidence: pinned Java [binary streams](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBStreamParameter.java), [character streams](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBReaderParameter.java), [MySQL long data](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/StreamParameter.java), and [native execution](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java). Bounded chunks expose no Java chunk-size switches and never materialize whole streams. Actual limits, empty storage semantics, LOB conversion, and proxy behavior await four-target integration.

## 14. Go 1.26 Design for LOBs and Complex Types

### 14.1 Separate Ordinary Values from Connection Handles

Ordinary BLOB/CLOB queries return contents. Oracle out-row LOB queries use server cursors/complete fetch boundaries, then read on the original session under automatic materialization limits. If text streaming cannot safely insert LOB commands, use bounded full-response buffering or cursor paths.

Use explicit Blob/Clob extensions for huge objects, random access, streaming upload/download, empty LOBs, or locator modification. Go 1.26 need not place connection-identified objects into Scanner.

```go
// Session describes existing standard-library fixed-session calls; it creates no new Session object.
type Session interface {
    ExecContext(context.Context, string, ...any) (sql.Result, error)
    QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func OpenBlob(ctx context.Context, session Session, query string, args ...any) (*Blob, error)
func OpenClob(ctx context.Context, session Session, query string, args ...any) (*Clob, error)
```

Resource entry points check concrete types, accepting non-nil `*sql.Conn`/`*sql.Tx` only. Structurally compatible `*sql.DB` must fail before networking; wrappers are also outside this contract. Ordinary batching/functions without persistent handles are unrestricted. Open queries must return exactly one required LOB, otherwise explicitly error and clean all results.

Blob/Clob retains caller standard-session references, native locators or table/row identities (§14.4), and generations; later calls go through that session and recheck connection ownership, never storing raw driverConn from `Conn.Raw`. Without native MySQL locators, ordinary BLOB bytes cannot impersonate locators; OpenTableBlob provides table chunk access.

Resources own locators, not Conn/Tx. Close resources before closing sessions/ending transactions; resource Close never closes sessions. Validate physical identity/session generation/transaction generation per call. Old transaction handles cannot survive into new transactions on the same connection. Session close/reset invalidates outstanding resources; unreliable cleanup discards physical connections.

Use `errors.Is(err, oceanbase.ErrResource)` for invalid resources; ended Conn/Tx may first return `sql.ErrConnDone`/`sql.ErrTxDone`. Close is idempotent and never reconnects ended/invalid sessions. Pre-send cancellation leaves another close attempt possible. Physical sessions own internal records; handles retain only standard Session/opaque identity/generation, without public resource managers. Clean unpublished records when outer operations fully finish; dictionary/LOB auxiliary responses preserve outer resources. Observed transaction changes also invalidate old generations. Begin/end/reset cleanup obeys CleanupTimeout.

### 14.2 Large-Object Operations

- Blob provides byte-defined Reader/Writer, ReadAt/WriteAt, length, truncation, and release with context-aware network entry points.
- Clob/NCLOB provides character reads/writes/lengths, distinguishing character offsets from UTF-8 byte offsets rather than borrowing Blob indexing.
- Internally distinguish Locator V1/V2, inline/out-row, empty, temporary/persistent objects.
- Distinguish freeing server temporary LOBs from closing local readers. Close is idempotent and never commits caller transactions.
- Invalid session/transaction/generation returns resource errors; relogin cannot establish old locator validity.
- Select PIECE_DATA/LONG_DATA/DBMS_LOB by mode/capability, preserving original transactions.

LONG_DATA/PIECE_DATA transport parameters, not MySQL table-BLOB random access. See emulated locators in §14.4.[J14]

### 14.3 Scheduling Active Results and LOBs

Never insert DBMS_LOB/other commands into unread responses. Cursor fetch first consumes one bounded batch response, then materializes required LOBs while remaining rows stay boundedly buffered. Explicit OpenBlob/OpenClob finishes locator queries before returning handles.

Keeping Oracle cursors open while executing same-session LOB operations needs live verification. Unsupported versions must use bounded post-query locator reads, never another physical connection pretending to preserve original-transaction visibility.

Complex descriptors likewise execute only at command boundaries. Complete current responses before internal root-package dictionary/LOB scheduling. Even then, QueryContext/Rows.Next may hold database/sql connection locks: never reenter public Metadata/Session/sql.DB/Conn/Tx methods. Use the current physical executor directly, without reacquiring standard locks. Scheduler owns one session operation without recursively acquiring its own lock. Share plans/descriptors/cache only; see §16.1.

D16 implements the native entry points above and `Blob.Reader(ctx)` / `Blob.Writer(ctx)`. Each adapter retains its own sequential offset and the context supplied for that adapter, without borrowing Open's context or owning the locator. `Blob.Truncate` only shortens objects. Short reads return bytes read and io.EOF; chunked write failures return the completed prefix without replaying the failed chunk. LOBs from ordinary queries and scalar `sql.Out` / `Out` return content, with a default limit of 16 MiB per value. Explicit handles can process larger objects in chunks.

Current implementation invokes DBMS_LOB.READ/WRITE/TRIM/FREETEMPORARY through existing Oracle COM_STMT_PREPARE_EXECUTE, requiring confirmed actual ob_version >=2.2.76. Counts accept native integers/lossless integer NUMBER, rejecting fractions/overflow/count-content mismatches. Each block obeys DBMS_LOB 32767 bytes and MaxMessageBytes. V2 temporary flags trigger server freeing; V1 uses same-session ISTEMPORARY. Persistent locators release local records only. Cleanup failure discards connections without ending caller transactions.

Each internal LOB command captures generations before sending and compares after complete responses. Transaction/autocommit changes invalidating generations return ErrResource without adopting content/new locators or sending later blocks. Preserve completed prefixes without inferring rollback of current side effects. Ordinary materialization uses identical checks before public handles exist; failures retain result-reading discard rules. [Transaction regressions](../lob_epoch_test.go) cover explicit BLOB/CLOB chunk reads/writes and ordinary materialization.

Until D19 server cursors, first native LOB results boundedly buffer all remaining row sets, commands, hidden OUT, materializing only after full boundaries. Count columns/row containers/fields/materialized content against MaxMessageBytes/MaxResultItems/MaxLOBBytes. Internal commands retain gates without public Session/Metadata or auxiliary connections. Buffered rows retain caller context independently of internal network cancellation. Open finishes the entire locator query before atomic publication and cleans temporary objects on wrong cardinality.

Character off/count/returned counts are Unicode code points. Known UTF16 collations convert server offsets to UTF-16 units; other supported charsets use code points. Out-row paths scan in chunks from the beginning and validate server quantities/encoding lengths, never UTF-8 bytes as characters. UTF-16 overwrites require matching surrogate-pair widths at existing/new positions; width changes return ErrUnsupported before writes. Append/same-width overwrite work. Unrepresentable text, invalid pairs, and mismatched units fail explicitly. TestNativeLOB must still establish target NCLOB units/collations.

OpenBlob/OpenClob captures generations before prepare/query, rechecking before locator queries/after complete responses. If prepare ended the transaction, do not execute. Metadata/end rows/combined final OK invalidity returns ErrResource without registering/publishing locators, preserving sessions at complete boundaries. On parameterized-open failure, Rows owning the operation lock cleans automatic statements rather than recursively locking Close; full ERR must not spuriously discard due to cleanup self-deadlock. [Open regressions](../lob_open_epoch_test.go) cover both LOBs, text/client/server/combined execution, three streams, and original-session reuse after complete errors.

### 14.4 Table BLOBs and MySQL Emulated Locators

Use explicit target descriptors for reliable row identities, never inferring tables/keys from arbitrary queries, aliases, or contents. This API choice returns the same Blob type:

```go
type BlobTarget struct {
    Schema string
    Table  string
    Column string
    Key    map[string]any // Complete primary key: column names to bound values
}

func OpenTableBlob(ctx context.Context, session Session,
    target BlobTarget) (*Blob, error)
```

- Fixed-session rules match OpenBlob. Omitted Schema resolves actual schema on-session, then pins identity. Table/Column required; fields are identifier components, never SQL fragments. Oracle uses exact dictionary spelling; MySQL resolves columns case-insensitively, while schema/table follow dictionary comparison.
- Mode metadata verifies one base table, readable binary BLOB column, complete primary key. Reject views/joins, missing/excess/NULL keys, unsupported types. Target BLOB cannot be a key/generated column. Quote dictionary-resolved names; bind all keys.
- Validate inputs on open; evaluate Valuer once, copy maps/slices/mutable bytes, and retain normalized immutable identity. Later caller Key changes cannot retarget handles; no global token registry.
- Open reads metadata/existence/NULL/length or native locator only, never whole BLOBs. Missing row returns sql.ErrNoRows; SQL NULL returns nil,nil; non-NULL empty content yields valid zero-length handles. Initialize NULL with explicit ordinary SQL.
- MySQL chunks use key-based OCTET_LENGTH/SUBSTRING; Oracle may query native locators from the same descriptor. Writable Tx target queries use FOR UPDATE for required persistent-locator locks; read-only Tx/Conn opens do not. MySQL is not LONG_DATA and does not promise arbitrary queried BLOBs become persistent handles.[J14][J15]
- Blob offsets are Go zero-based; SQL one-based conversion/off+len arithmetic checks overflow. Each block obeys logical-message/LOB-block limits; no hidden whole-object materialization when chunking is unsupported. Current maximum 32767 bytes shrinks under message budgets or smaller MaxLOBBytes; whole handles exceed individual materialization limits.
- MySQL ReadAt and length queries access the primary key again on every call. `*sql.Conn` does not guarantee snapshots across chunks; callers requiring stable reads use a `*sql.Tx` with suitable isolation and explicit locking where necessary. Outside transactions, the driver does not guarantee detection of a row deleted and reinserted under the same primary key.
- Emulated MySQL WriteAt/Truncate requires the caller's `*sql.Tx`; otherwise it fails before mutation without implicit Begin/Commit. Each mutation first executes key-based SELECT ... FOR UPDATE in that transaction, confirms the row and non-NULL content, reads its locked length, then issues a parameterized UPDATE. Read-only transactions or unavailable locking semantics reject modification. Currently only MySQL base tables with dictionary ENGINE=InnoDB allow mutation; unknown engines remain readable.
- WriteAt allows `0 <= off <= current byte length`, including append, without sparse padding. Binary replacement may use `CONCAT(LEFT(col, off), ?, SUBSTRING(col, off+len(p)+1))`, safely quoting col and binding all other values. Truncate only shortens within `0..current length`. Empty or identical writes may succeed after locked existence is confirmed; ChangedRows=0 does not mean the row is absent.
- Before writes, validate complete-plan capacity: TINYBLOB 255, BLOB 65535, MEDIUMBLOB 16777215, LONGBLOB 4294967295 bytes. Excess returns ErrLOB before first UPDATE. Complete UPDATE responses with warnings also return ErrLOB without counting that block; server may truncate or make CONCAT NULL, so callers should roll back existing transactions. Non-strict warning OK is not complete success.
- Blocks are separate SQL; caller Tx controls atomicity/rollback. Consume responses before later sends; never retry network failures. Internal queries/UPDATE capture generations before sends and recheck full responses. Invalid responses do not copy buffers/publish lengths or handles/count completed prefixes/report short-read EOF. Schema/dictionary/length/Oracle locator opens share checks even before public registration. Preserve prior blocks without inferring current side-effect reversal. Complete-boundary invalidity retains usable sessions for further calls/rollback. [Table BLOB transaction regressions](../lob_table_epoch_test.go) cover first/midblock invalidity, short reads, lengths, and both-mode opening. DDL/privilege/type changes retain errors/resource invalidity without retargeting guesses.
- Targets share connection/session/transaction generations. Close is idempotent without Conn/Tx closure or implicit commit. Emulation has no native locator to free and clears local identity/occupancy only; native backends still release server resources.

This covers Java emulateLocators table/key/column targeting and chunked I/O while preserving ordinary Go SQL. Extra SQL round trips and transaction requirements are public behavior.

## 15. Cursors, Scrolling, and Updatable Results

Opening cursors establishes generations before prepare and rechecks after prepare/execution. If prepare ended transactions, do not execute; invalid execution responses publish no handle or static prefetch. Oracle sensitive initial responses consume final OK, then recheck before LOB/complex/child materialization. Complete-boundary invalidity closes this statement and preserves usable sessions for further work/rollback, without replay or claims of reversed side effects. [Open invalidation regressions](../cursor_open_epoch_test.go) cover both-mode forward/static, Oracle sensitive, three streams, and final-OK invalidity.

Standard `sql.Rows` remains forward-only. Dedicated Cursor supplies Java scroll/sensitive/updatable capabilities without adding Java getters/setters to every Rows.

```go
func OpenCursor(ctx context.Context, session Session, query string,
    opts CursorOptions, args ...any) (*Cursor, error)

func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error)
func (c *Cursor) Close() error

type ColumnChange struct { Name string; Value any }
func (c *Cursor) Update(ctx context.Context, key map[string]any, changes ...ColumnChange) error
func (c *Cursor) Delete(ctx context.Context, key map[string]any) error
func (c *Cursor) Insert(ctx context.Context, changes ...ColumnChange) error
func (c *Cursor) Refresh(ctx context.Context, key map[string]any) (*sql.Rows, error)
```

Design rules:

- Cursor binds supplied sql.Conn/Tx and returns only after complete opening control responses.
- Each Fetch returns a standard sql.Rows batch. Closing batches does not close server cursors; close previous batches before fetching again.
- Position explicitly expresses next/absolute/relative, using available Oracle offset fetch. Static scroll uses bounded caches and errors on excess, never unbounded full-result storage.
- Sensitive reads/Refresh use genuine supported server reposition/refresh, never static caches masquerading as sensitivity.
- Updatable cursors require unambiguous single-table identities, generating mode-correct parameterized DML from keys/ROWID.
- Update/Delete/Insert/Refresh take explicit changes/identities, binding values and safely quoting identifiers. Never infer targets for joins/aggregates/missing identifiers.
- Expect exactly one located row and validate matched semantics/identity; zero/multiple matches explicitly report concurrent-change/location errors. ChangedRows UPDATE=0 cannot prove missing rows: use reliable same-transaction locked existence checks to distinguish unchanged/unmatched. Without guarantees, reject capability before mutation, never guess through post-autocommit queries or unconditional success.
- Execution failure/close/transaction end/reset invalidates corresponding cursor state; unsupported types return capability errors.

Current D19 implementation boundaries:

- `CursorOptions.Mode` is CursorForward (default), CursorStatic, CursorSensitive. `Updatable` explicitly enables mutation/Refresh. `MaxCacheBytes` limits static snapshots only; zero uses MaxMessageBytes. No second pool/public resource manager; new files stay in root.
- `CursorPosition{Origin, Offset}`: Next requires offset=0. Absolute is 1-based; 0 returns empty batches/before-first position; negatives reject. Relative is based on the previous batch's last fetched row, even if closed before scanning all fetched rows. Counts must be positive uint32. Past static end yields empty batches; subsequent Next never restarts. Position arithmetic checks int64 overflow.
- Forward uses COM_STMT_EXECUTE cursor flag=1/COM_STMT_FETCH and Next only. Known Oracle >=2.2.5 uses 19-byte offset extensions/trailing OK; older/unknown uses nine-byte forward fetch without assuming offsets. MySQL retains column descriptors; Oracle rereads them.
- Static opening builds bounded snapshots via real native fetch, counting columns/rows/value overhead; excess cleans unpublished resources and errors. Update/Delete/Insert/Refresh never changes snapshots; explicit Refresh reads subsequent changes.
- Sensitive requires Oracle known >=2.2.76, real combined command 161/cursor flag=1/scrollable executeMode=8, fully consuming initial responses. Every nonzero position sends ABSOLUTE32 fetch, limited to int32; final OK supplies last-row positions. Repeated positions still contact servers. MySQL/unknown/old versions ErrUnsupported. Visibility of other transactions depends on server cursors/isolation and remains live-unverified.
- Fetch/Refresh receives, decodes, and materializes full batches before returning standard Rows. Batch/LOB/complex/descriptor limits remain. Auxiliary descriptors/LOBs execute only after full responses within original gates. Batches get new contexts, not Open's; each public command clears descriptors.
- Native fetch captures generations before sends and rechecks full responses before any LOB/complex/child materialization. Invalidity returns ErrResource without publishing batches/later fetches/child reads. Ordinary REF CURSOR shares checks without public handles. Failed static opens release parent statements by operation resource identity even when current generations expired. Full-response resource errors do not end transactions. [Invalidation regressions](../cursor_epoch_test.go) cover ordinary children, explicit forward/static, three streams, and later original-session calls.
- Updatable currently requires a writable caller `*sql.Tx`, a simple single-base-table projection, consistent schema/original-table/original-column metadata, and complete projected primary keys. MySQL additionally requires dictionary ENGINE=InnoDB. This uses primary keys, without an added ROWID-only alternative; missing complete keys reject. Joins, subqueries, DISTINCT, aggregates, set operations, duplicate columns, and derived columns never imply guessed targets. Generated columns can be read but not modified. Reliable row identity determines support, rather than guessed SQL fields.
- `key` is the complete primary `key`. `ColumnChange.Name` uses dictionary base-column names, never aliases/expressions. Quote by actual mode; DML/Refresh use genuine COM_STMT_PREPARE/EXECUTE parameters. Insert explicitly supplies all non-NULL keys without implicit generated IDs. Same-transaction SELECT 1 ... FOR UPDATE uses existing typed queries and fully finishes before DML. Update=0 may succeed; Delete/Insert require 1; multiple counts/warnings error. Missing rows support errors.Is(sql.ErrNoRows).
- Refresh reads current projections by full key without original WHERE/ORDER, snapshot or position changes; currently Updatable-only and requires writable Tx. Failed mutations never auto-commit/rollback; callers should roll back warnings/unknown outcomes. Business failures release cursors; pre-send position/argument errors retain handles. Recheck generations between prepare/existence-lock/DML and after full DML responses, preventing mutation after transaction end or reporting invalid responses as success, without implying side-effect rollback. Refresh validates parents before materialization and generations afterward; invalid rows are unpublished without old child reads. Complete-boundary invalidity/Refresh missing rows close cursors/temporary statements only and preserve sessions. [Mutation/refresh regressions](../cursor_mutation_epoch_test.go) cover both modes/three streams/Oracle LOB.
- Unclosed previous batches immediately make Fetch/Refresh/mutation/Close return ErrCursorBusy, avoiding database/sql self-deadlock. Close is idempotent; transaction end/reset/physical close releases through existing generations/parent leases. Ordinary forward Rows behavior stays unchanged.

This is local protocol implementation, not acceptance of live OceanBase/OBProxy cursors, locks, or sensitivity. Four-target `TestCursorModes`/`TestCursorUpdates` source is compiled only without databases.

This extension object is necessary because the standard library has no equivalent scrolling surface. Query data still uses standard Rows.Scan.[J10]

## 16. Metadata, XA, and Session Helpers

### 16.1 Metadata

`Rows.ColumnTypes()` describes result columns; functions/explicit Filters return database structure as Go structs or ordinary Rows.

Coverage includes catalog/schema, tables/views/synonyms, columns, primary/foreign keys, indexes, privileges, routines/parameters, type definitions/UDTs/array elements, capabilities, versions. Like Java, unavailable server information stays unknown/unsupported rather than fabricated.

Maintain mode SQL separately and parameterize filters; use mode quoting for necessary dynamic identifiers. Identify mode on fixed sql.Conn and query there, never identify on one Auto-pool session then randomly query another.

Complex schema/synonym/nested descriptors are shared metadata/codec data, not duplicate dictionary caches.

Public `Metadata(ctx, session, filter)` validates fixed `*sql.Conn` / `*sql.Tx` inputs, then enters the standard QueryContext core through package-private requests and returns ordinary `*sql.Rows`. Internal type-descriptor resolvers and LOB materializers call the root dictionary/command executor directly on the current physical session, only at legal response boundaries in §14.3. They cannot call public Metadata or standard session methods, or open another connection to read the original transaction's dictionaries. Both paths reuse parameterized plans, descriptor types, and caches isolated by session privileges/mode/schema/type version, without sharing high-level call stacks.

D13 fixes public/internal boundaries. D16/D18 must use real database/sql plus fault peers to verify cache misses inside QueryContext/Rows.Next finish boundedly on the same session without lock reentry or commands inserted into unread responses. Live servers separately prove uncommitted visibility; mocks cannot.

### 16.1.1 D13 Implemented Go Metadata Contract

The only public entry point is `Metadata(ctx context.Context, session Session, filter MetadataFilter) (*sql.Rows, error)`. It accepts non-nil `*sql.Conn` / `*sql.Tx` and rejects `*sql.DB`, wrappers, and typed nil values. `MetadataKind` is a closed enum. Ordinary results retain `Rows.ColumnTypes()` without introducing JDBC-style DatabaseMetaData objects.

```go
pinned, err := db.Conn(ctx)
if err != nil {
    return err
}
defer pinned.Close()
rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{
    Kind:          oceanbase.MetadataColumns,
    SchemaPattern: "APP",
    NamePattern:   "ORDER/_%", // '/' escapes '_'; trailing '%' is a wildcard
})
if err != nil {
    return err
}
defer rows.Close()
// Scan in the fixed order below; use sql.Null[T] for nullable values.
```

`MetadataFilter` fields: `Kind`, `Catalog`, `SchemaPattern`, `NamePattern`, ColumnPattern, `ReferencedSchemaPattern`, `ReferencedNamePattern`, `TableTypes []string`, `Query`. `Catalog` matches exactly; other patterns preserve case and use LIKE with '`/`' escaping, empty matching all accessible objects. Callers escape underscores/literal names by LIKE rules. `SchemaPattern` filters schema/owner; `NamePattern` tables/routines/types; ColumnPattern columns/parameters/attributes. Referenced fields apply only to foreign keys; TableTypes only to tables with TABLE/VIEW/SYNONYM/`MATERIALIZED VIEW`. Reject inapplicable nonempty fields. `Query` is StatementParameters-only.

Tables/Columns always return dictionary remarks, preserving Chinese text, quotes, edge spaces, empty strings, and NULL without a remarks switch. Empty Catalog does not bind current schema; Oracle catalogs are NULL, so nonempty Catalog matches nothing. [TestMetadataCatalogAndRemarks](../metadata_contract_test.go) checks requests/conversion through public APIs in both modes/utf8mb4/GBK/three streams and rereads changed remarks; fixed local row sets do not evaluate real dictionary SQL. [TestMetadataCatalog](../tests/integration/metadata_test.go) adds DDL remarks/Catalog comparisons and is compiled only.

The following column order is fixed. '`#`' marks `int64` columns; others are strings, and '`#`' is not part of names. All columns permit SQL NULL and retain missing values. Dictionary Rows uniformly reports BIGINT/`VARCHAR` and `int64`/`string` ScanType, hiding NUMBER/VARCHAR2 dictionary differences.

| MetadataKind suffix | Returned columns in order |
| --- | --- |
| Catalogs | catalog |
| Schemas | catalog, schema |
| Tables | catalog, schema, name, type, remarks |
| Synonyms | catalog, schema, name, target_schema, target_name, database_link |
| Columns | catalog, schema, table_name, name, #ordinal_position, data_type, #length, #precision, #scale, #nullable, default_value, remarks, type_owner |
| PrimaryKeys | catalog, schema, table_name, name, column_name, #ordinal_position |
| ForeignKeys | catalog, schema, table_name, name, column_name, #ordinal_position, referenced_catalog, referenced_schema, referenced_table, referenced_column, update_rule, delete_rule, deferrable, initially_deferred |
| Indexes | catalog, schema, table_name, name, #non_unique, column_name, #ordinal_position, ordering, #cardinality, type |
| TablePrivileges | catalog, schema, table_name, grantor, grantee, privilege, #grantable |
| ColumnPrivileges | catalog, schema, table_name, column_name, grantor, grantee, privilege, #grantable |
| Routines | catalog, schema, name, specific_name, package_name, type, return_type |
| RoutineParameters | catalog, schema, routine_name, specific_name, package_name, name, #ordinal_position, direction, data_type, #length, #precision, #scale, type_owner, type_name, #data_level |
| Types | catalog, schema, name, type, #attribute_count, #final, #instantiable |
| TypeAttributes | catalog, schema, type_name, name, #ordinal_position, data_type, type_owner, #length, #precision, #scale |
| CollectionElements | catalog, schema, name, collection_type, #upper_bound, element_type, element_owner, #length, #precision, #scale |
| ServerInfo | oceanbase_version, compatibility_mode, #parameter_dictionary, #type_dictionary |
| StatementParameters | #physical_ordinal, #logical_ordinal, name, direction, data_type, #nullable, #length, #precision, #scale, #protocol_type, #protocol_direction |

- MySQL uses information_schema, catalog `def`, and databases as schemas. Synonyms/Types/TypeAttributes/CollectionElements return ErrUnsupported without synthesized dictionaries. Oracle uses ALL_*; catalog queries return empty rows, other catalogs NULL. Select views/materialized views through TableTypes; synonyms have separate targets; Columns never implicitly expands them.
- Routine/parameter specific_name shares dictionary identity; Oracle combines object_id/subprogram_id, retaining package_name/data_level/return position 0. Directions normalize `IN`/`OUT`/`INOUT`/`RETURN`. Unknown update_rule/default properties/grantor remain NULL. Key/index positions are 1-based; ordering ASC/DESC/NULL.
- MySQL PARAMETERS requires initialization `ob_version()` confirming >=4.1, never greeting MySQL 5.7/8.0. Old/unknown uses SHOW CREATE for exact schema/routine requests, not wildcard enumeration. Preserve privilege/dictionary SQL errors. ServerInfo bits describe driver query paths, not current dictionary privileges.
- StatementParameters prepares Query once, saves descriptors, closes temporary statements, and returns independent memory Rows without executing Query, including PrepareClient configurations. Physical/logical positions are 1-based; repeated Oracle names share normalized logical positions. protocol_type/protocol_direction retain raw bytes without guessing IN/OUT; direction is NULL, so query RoutineParameters for directions. MySQL nullability is unknown; reported types/sizes reflect prepare metadata, not guaranteed inferred business columns.
- Columns.length uses MySQL CHARACTER_MAXIMUM_LENGTH; Oracle CHAR_LENGTH for character semantics, otherwise DATA_LENGTH bytes, interpreted with data_type. Routine/type/collection lengths retain dictionary units without invented defaults. Ordinary ColumnTypes lengths use protocol/known-charset character or binary-byte semantics; unknown/uninterpretable gives ok=false. DECIMAL precision excludes sign/point; Oracle NUMBER uses explicit precision/signed scale, invalid/unspecified gives ok=false. Oracle FLOAT binary precision/-127 floating scale is not reported as decimal precision.
- `metadataLocked` runs with existing gates after complete responses, reusing parameterized plans/charset/safe literals. Returned Rows closes its callback only, not outer gates. Reject unread responses; no public Metadata/Session or new connections. Public entry transfers lock ownership to Rows. D18 caches descriptors within one command; D13 caches no arbitrary dictionary rows.
- UDT attribute/element projections use OceanBase NUMBER_PRECISION, still aliased precision, based on pinned [v4.3.5_CE attribute views](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/share/inner_table/ob_inner_table_schema.25001_25050.cpp) and [collection views](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/share/inner_table/ob_inner_table_schema.25051_25100.cpp), avoiding incompatible field assumptions.

Ordinary/StatementParameters MySQL string names use charset 63: protocol 15/253 VARBINARY, 254 BINARY; other charsets retain VARCHAR/CHAR even with BINARY_FLAG. ScanType stays []byte/string, lengths byte/character-based, empty bytes distinct from NULL. Oracle retains VARCHAR2/CHAR. Pinned Java evidence: [type names](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java#L425), [binary detection](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/ColumnDefinition.java#L715). TestBinaryStringColumnTypes/TestBinaryStringParameterMetadata provide both-mode local evidence; TestAllScalarTypes BINARY/VARBINARY checks compile only.

Protocol peers cover these paths. Live dictionary fields/version differences/privileges need integration; D16/D18 separately verify Query/Next cache misses.

### 16.1.1a Java Metadata and Named Procedure Compatibility

MetadataColumns retains its original 13 columns and appends numeric auto_increment/generated flags (1/0/NULL). MySQL uses explicit EXTRA markers; Oracle uses ALL_TAB_COLS VIRTUAL_COLUMN, excludes hidden columns, and leaves uncertain auto-increment NULL. Appended MetadataTypeInfo enumerates ten built-in columns: type_name/category/precision/minimum_scale/maximum_scale/radix/literal_prefix/literal_suffix/create_parameters/auto_increment. Static SQL families are not server-capability probes; unknown maxima remain NULL. MetadataTypes stays Oracle UDT dictionaries.

Old/unknown MySQL MetadataRoutineParameters accepts exact schema/routine patterns (undo LIKE '/' escapes, reject %/_ wildcards), safely quoting same-session SHOW CREATE PROCEDURE and trying FUNCTION only for nonexistent error 1305. Never swallow privilege/network/cancellation errors. Missing schema reads same-session DATABASE(). Parse declared names/positions/directions/types within response/metadata budgets; hidden/unsupported/incomplete definitions reject and unknown fields remain NULL.

MySQL simple CALL sql.Named maps declared case-insensitive names to placeholders, supporting IN/OUT/INOUT and position-preserving scalar constants; every placeholder must be named. Ordinary SQL still rejects names; no guessing complex expressions/session variables/function returns. Server/client, direct/explicit prepare, Exec/Query/output share in-gate mapping and read exact metadata each time without stale schema/signature caches, extra sessions, or validation-time CALL execution. Existing transaction/cancellation/budgets apply.

MySQL parameter dictionaries join ROUTINE_TYPE for same-name routines; named CALL reads PROCEDURE only. Failed legacy named CALL SHOW CREATE PROCEDURE never switches to same-name FUNCTION; public parameter metadata may. Declaration parsing accepts YEAR/YEAR(4). Column/routine TIME/DATETIME/TIMESTAMP scale uses DATETIME_PRECISION; other types use NUMERIC_SCALE.

### 16.1.2 D32 Result Origins and Product Identity

`Rows.Columns()`/`Rows.ColumnTypes()[i].Name()` always return server labels. Origins use `Execute(...).Snapshot().Results[i].Columns` in projection order. `ResultColumn.Name` is label; `OriginalName` server base name; `Table` protocol table (possibly alias); `OriginalTable` base table; `Schema`/`Catalog` retain protocol values. Strictly decode all six strings using session charset into UTF-8 without case folding or automatically treating catalog as business database.

Empty origins mean absent server information; expressions/views/versions may omit it. Never infer from SQL/labels/other fields. Every row set has independent Columns available after snapshots/execution close. Complete metadata neither means complete rows nor proves writability/privileges/unique base identity. See §11.4 for completeness/concurrency/budgets.

Java `useOldAliasMetadataBehavior` defaults false: MySQL getColumnName prefers original names, falling back to labels for empty/switch/forceAlias; Oracle always labels. getTableName also depends on this and forceAlias. [Source](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseResultSetMetaData.java#L270). Go retains both for callers without changing standard labels. Java `blankTableNameMeta` defaults false; Go adds no clearing switch, leaving presentation hiding to applications.

Java `useCompatibleMetadata` defaults false and changes only JDBC getDatabaseProductName OceanBase versus MySQL/Oracle, not columns/dictionaries. [Source](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4DatabaseMetaData.java#L1966). Go product is always OceanBase; MetadataServerInfo separately exposes `oceanbase_version`, `compatibility_mode`, dictionary capabilities without product impersonation. All three Java DSN names reject.

TestExecutionColumn* covers both modes/text/binary/GBK/combined/multiple results, incomplete metadata rejection, cumulative budgets, snapshot isolation. TestMetadataServerIdentity checks fixed projections/mode-version separation; OUT tests exclude auxiliary leakage. TestMetadataCatalog adds public origin checks permitting server-absent empties; compiled only, without real fields/version/proxy acceptance.

### 16.2 XA

Provide `XID` values and explicit Start/End/Prepare/Commit/Rollback/Recover/Forget operations taking fixed `*sql.Conn`, `XID`, and standard XA flags. XA does not accept `*sql.Tx`; XA-enabled sessions cannot simultaneously BeginTx.

- MySQL uses XA commands; Oracle uses DBMS_XA/complex XIDs.[J11]
- Start through End/Prepare retains physical-session constraints; callers own external coordination/durable logs.
- Recover returns actual XIDs; closing Go objects must not treat prepared branches as local uncommitted transactions and roll them back.
- Define branch states/reuse conditions; abnormal or uncertain disassociation prevents pooling.
- Adapt 1PC/2PC, join/resume/suspend, supported timeouts/codes individually; reject unsupported flags.
- No transparent replay after XA failure; coordinators recover uncertain commits.

### 16.2.1 D31 Implemented Contracts and Boundaries

The public root package provides `XID`, `XAFlags`, `XAVote`, and these seven explicit functions on fixed `*sql.Conn`, reusing private requests and connection gates without new packages, dependencies, pools, or transaction managers. `XID` FormatID is int32 with -1 reserved; GlobalID is 1–64 bytes and BranchID 0–64 bytes. Copy input/recovery slices; binary identity never undergoes character conversion.

- MySQL uses hex binary literals/32-bit FormatID bit representation with exact JOIN/RESUME, never Java `pinGlobalTxToPhysicalConnection` silently converting JOIN to RESUME. Preserve rejection errors; actual versions determine support.
- Oracle calls `SYS.DBMS_XA` through fixed PL/SQL, first native OUT returning integer codes; Java useLocalXID-style SYS.DBMS_XA_XID uses NUMBER/RAW/RAW, avoiding repeated dictionaries/literal concatenation. Native PrepareExecute requires actual >=2.2.76; unknown/old ErrUnsupported. Live applicability of this PL/SQL awaits integration.
- Start accepts NoFlags/Join/Resume; Oracle also accepts XAOracleReadOnly (256), XAOracleSerializable (1024), and XAOracleLoose (65536). End accepts Success/Fail/Suspend; Oracle additionally accepts Migrate or Suspend|Migrate. Reject all other combinations before sending. Oracle Start disables autocommit and restores it after confirmed End; restoration failure returns ErrTxCleanup and discards the session.
- Track only locally confirmed phases; one active branch at once. Resume known suspended branches, Join ended branches; prepare requires ended, 1PC known ended, 2PC known prepared. New connections may prepare/2PC-commit/rollback unknown branches for recovery; local maps are not global RM truth. Bound maps by MaxResultItems.
- End(Fail) marks local rollback-only; prepare/commit reject until rollback. Oracle sends supported Success-equivalent disassociation. Marks are not durable/cross-connection; coordinators must persist failed decisions and prevent new-connection commits.
- Prepare codes 0/3 mean prepared/read-only; MySQL success reports prepared only. Oracle negative and positive rollback/heuristic codes return XAError, not arbitrary-positive success. Retain integer XA codes. MySQL 1397–1402 maps standard codes, 1440 DUPID(-8), 1613 RBTIMEOUT (106), 1614 RBDEADLOCK (102). Pinned OceanBase [protocol definitions](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/mysql_errno.h#L457)/[XA definitions](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.def#L1695) classify, without reproduced server conditions. Unknown MySQL/Oracle SQL errors retain Error and RMERR. Do not map same-number ORA codes as MySQL; retain DBMS_XA returns directly.
- Recover reads real four-column/native OUT collections, strictly checking lengths/NULL/FormatID/SYS/type/order. Oracle accepts named or explicitly SYS.DBMS_XA_XID anonymous elements. NULL BQUAL is valid empty, NULL GlobalID invalid. Bound rows/content by MaxResultItems/MaxMessageBytes. Malformed/excess/extra results discard without partial lists.
- Public errors support errors.Is(ErrXA) and retain server/context causes. Possibly written changes with I/O/cancellation/malformed/trailing failures return ErrXAOutcomeUnknown, additionally ErrCommitUnknown on commits, discarding without replayable ErrBadConn. Full rejections retain XA/server codes without assuming completion; failed recovery does not prove rollback.
- After first XA attempt sessions become permanently dedicated, rejecting sql.Tx/ChangeUser/setters and exiting original pools on close. XA boundaries invalidate LOB/Cursor. Prepared Close closes statements/physical connections without local ROLLBACK/autocommit reset. Never mix raw XA/COMMIT/ROLLBACK/autocommit-changing SQL; drivers do not parse global transaction semantics. Read-only configured services allow recovery only; mutations need read/write.
- Forget returns ErrUnsupported; no RM transaction timeout setter. Java also lacks these, so do not copy no-op success. Context limits client I/O, not RM timeouts. Callers own XARecover/durable logs/decisions/unknown outcomes; absence from recovery alone proves neither commit nor rollback.

`xa_test.go` covers both modes, standard/compressed/OB20 streams, 1PC/2PC, suspend/resume, phases/flags, rollback-only, native XID/OUT, return codes, recovery data/bounds, partial writes, lost acknowledgments, and trailing failures. Four-target manual CI includes `TestXATransactions` / `TestXARecoveryAfterDisconnect` for DML visibility, closing prepared sessions, and recovery on new physical connections. They are currently compiled only, without databases. Examples are in `examples/xa/` and explicitly omit a durable coordinator. D30's D28 platform dependency remains open; D31 local implementation and complete dependency closure are recorded separately.

[TestXACommitContextBoundary](../xa_context_test.go) adds 18 local both-mode/three-stream cases. Pre-canceled contexts are not unknown outcomes and preserve sessions. Cancellation/deadlines after complete commit receipt preserve context and both unknown classifications, close original transport, and forbid extra commands/redial. Tests verify real EOF/closure before cleanup, not cleanup as disposal evidence. They prove neither live commit/cancel nor every cancellation point after driver entry but before writing.

### 16.3 Other Session Features

Schema/catalog, user switching, client info, warnings, variables, and version/capability helpers operate on fixed sql.Conn. ChangeUser reauthenticates/rebuilds baseline/invalidates all old statements/locators/cursors, never implicitly switching ordinary pool identities. Explicit ChangeUser makes sessions ineligible for their original pool; sql.Conn.Close discards them to prevent wrong-identity reuse.

Expose warnings as structured diagnostics without arbitrarily failing successful warned operations or losing queryable warning information when logging is disabled.

### 16.3.1 D30 Session Helpers and Warning Count Snapshots (Initial Implementation)

These functions accept only non-nil `*sql.Conn`. Callers must consume/close Rows first and serialize calls on the same connection. No new session wrapper or public arbitrary-protocol command entry is introduced:

```go
func CurrentSchema(context.Context, *sql.Conn) (string, error)
func SetSchema(context.Context, *sql.Conn, string) error
func CurrentCatalog(context.Context, *sql.Conn) (string, error)
func SetCatalog(context.Context, *sql.Conn, string) error
func GetClientInfo(context.Context, *sql.Conn) (ClientInfo, error)
func SetClientInfo(context.Context, *sql.Conn, ClientInfo) error
func SessionVariable(context.Context, *sql.Conn, string) (sql.NullString, error)
func SetSessionVariable(context.Context, *sql.Conn, string, string) error
func Warnings(context.Context, *sql.Conn) (WarningSummary, error)
```

- CurrentSchema queries Oracle SYS_CONTEXT current schema or MySQL current database, returning empty if none. SetSchema takes exact names without case folding: Oracle escapes double-quoted identifiers; MySQL COM_INIT_DB. Encode with connection charset without multi-SQL concatenation. Catalog is MySQL-only; Oracle ErrUnsupported.
- ClientInfo contains ApplicationName/ClientUser/ClientHostname strings mapped to same-name session user variables. Set replaces all fields; empty sends NULL and reads NULL as empty. Values reuse current-mode/NO_BACKSLASH_ESCAPES escaping/strict encoding, never globals.
- SessionVariable accepts ASCII system-variable identifiers only and preserves NULL. SetSessionVariable and initialization Session.Variables normalize MySQL decimal integer strings within int64/uint64 range into unquoted integer literals; all other values are escaped strings. Oracle values remain strings. Servers validate mutability/ranges. Config.Session owns time_zone/ncharacter_set_connection/character_set_*/collation_*; runtime setters reject them to prevent codec/state disagreement. MySQL wait_timeout passed real testing; other variables and Oracle still need target-specific evidence.
- Read helpers publish only after complete one-row responses, returning zero values on failure. Complete server ERR before/after first rows preserves Error and Rows reuse rules, without extra disconnection from scalar shape checks. Empty/extra rows/results/malformed responses reject/discard; shared Rows handles network/cancellation. [TestSessionReadResponseBoundary](../session_ops_failure_test.go) covers 84 local entry/mode/stream error/shape scenarios, not live SQL acceptance.
- Batch rewrite probes share this reader; complete server errors permit existing safe fallback without replaying confirmed prefixes. [TestBatchRewriteGuardRowError](../batch_rewrite_test.go) covers 12 three-stream initial/post-chunk probes and pre/post-first-row ERR, checking item results/session reuse.
- Setters require no managed/server transaction and autocommit enabled. Before sending, mark sessions unpoolable; success/full ERR still allows original pinned use, then discard on sql.Conn.Close. Validation failure does not mark. All setters clear automatic statement/descriptors before sends, including sql_mode parsing changes; explicit sql.Stmt retains native server semantics. Reconnection replaces incomplete restoration without changing ordinary SQL reset contracts.
- WarningSummary{Count uint16, Known bool} captures OK/EOF counts; false means no complete response supplied counts. Warnings reads the latest complete result from latest ordinary SQL. New SQL clears/replaces; Ping/helpers/internal cleanup do not, and pool reset clears. Execute ResultInfo.Warnings independently captures each complete result; incomplete remains unknown. Warned success stays success; nil logger does not affect snapshots.
- Count snapshots invent no codes/SQLSTATE/messages; see §16.3.3 for detail/clear, §16.3.2 ChangeUser, §16.3.4 explicit cancellation. Track subitems separately from platform authentication/transport completion.

Implementation reuses closed root requests, operation locks, Rows/error state machines, and wire leaves without new dependencies/packages. Four-target TestSessionOperations exists but is compiled only, without live mode/proxy/version validation.

### 16.3.2 D30 ChangeUser and Session Identity Generations

```go
func ChangeUser(ctx context.Context, conn *sql.Conn, credentials Credentials, database string) error
```

ChangeUser reauthenticates on original physical/TLS/standard-compressed-OB20 streams without dialing/replay/original Connector mutation. Callers supply current credentials/exact database; ProxyUser is explicit each time, empty clearing old targets (§6.3). Empty database selects none in MySQL; Oracle uses new authenticating username before @ (whole name if absent). No explicit-name case folding; usernames/databases strictly encode connection charset; server validates privileges/names. Original Config.Credentials is not called; switched passwords are not stored in connection config.

- Consume/close Rows and serialize fixed sql.Conn. Reject active managed/server transactions or autocommit off without implicit commit/rollback.
- Reuse D28 plugin factories, initial/multiround/switch authentication, password encoding, per-round TLS gates, io.Closer lifetimes. Retain final negotiated plugin/nonce; each switch creates fresh state. Authentication obeys earlier calling context/ConnectTimeout; callbacks/platform synchronous calls must cooperate.
- wire follows Connector/J COM_CHANGE_USER: command 17, terminated username, capability-dependent auth, always terminated database, negotiated plugin/attributes. Unlike generic MySQL, no two-byte charset. [Source](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L195)
- After validation/auth-response preparation, clean fixed resources/automatic cache under old identity, then send. Before sending, increment statement identity generation/mark unpoolable. Old server/client statements/LOB/Cursors cannot cross generations; old statement.Close is local only, never sending possibly reused IDs into new sessions.
- After server success, mode must remain unchanged. Rebuild charset/autocommit/version/timezone/variables/InitSQL baseline and clear descriptor/automatic-statement/warning caches. Use held-lock core, not database/sql reentry or renegotiation of active packet streams.
- After switch sends, any authentication rejection/timeout/cancellation/malformed response/mode change/plugin-close error/initialization failure discards sessions. Success retains pinned usability and exits original pools on sql.Conn.Close. New pool connections still use original Connector identity. Local validation/initial TLS rejection leaves identity unchanged.

TestChangeUserIsolation/Failure/Validation/Encoding/Database/PluginLifecycle/Cancel/VerifiedTLS/Framing provide local protocol/real-TLS-peer evidence. Four-target TestChangeUserSessions checks actual identities/old statement invalidation/pool isolation. Each profile needs CHANGE_USER_DSN for another same-service identity; only username/password/database are used, inheriting TLS/auth policy. Compiled only, no databases. Real plugins/LOB-cursor cleanup/proxies/privileges/versions await integration.

### 16.3.3 D30 Warning Details and Local Clearing

```go
type Warning struct {
    Level string
    Code uint32
    Message string
}
func ReadWarnings(context.Context, *sql.Conn) ([]Warning, error)
func ClearWarnings(context.Context, *sql.Conn) error
```

ReadWarnings sends MySQL SHOW WARNINGS on fixed connections, decoding levels/codes/messages by column charset. SQL supplies no SQLSTATE; never infer mappings. Messages may contain sensitive SQL/values and return only on request, without logging. Oracle retains protocol-count snapshots and ReadWarnings ErrUnsupported without guessed detail SQL. This matches [Connector/J getWarnings](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseStatement.java#L1263): MySQL details, Oracle generic warning indication represented in Go by WarningSummary.Count.

- This reads the **current server diagnostic area on request**, not historical details. Call promptly after consuming/closing Rows, serialized on pinned connections. Other commands/helpers/internal SQL may replace diagnostics. Warnings/ResultInfo local counts remain independent and need not correspond to current details.
- Do not skip MySQL reads at count 0; server count/detail retention differs. max_error_count/other limits may produce fewer details; never invent missing ones. No other connections, transaction changes, original SQL reinterpretation, or logger requirement.
- MaxResultItems bounds detail rows; MaxMessageBytes bounds decoded levels/messages/codes cumulatively. Excess returns wire.ErrMessageTooLarge without partial lists and discards. Empty is valid; NULL/invalid codes/wrong columns/extra results reject/discard. Full ERR permits reuse; post-send cancellation discards. Three streams share existing Rows.
- ClearWarnings only clears local counts (Known=true,Count=0) and suppresses MySQL ReadWarnings until next ordinary SQL send. It sends no SQL or server-area clearing. Helpers/Ping retain suppression; new ordinary SQL, including failures, removes it. New/reset/ChangeUser suppress old details. Oracle clears counts but details remain ErrUnsupported. Historical Execution.Snapshot is unchanged.

TestWarningDetails/Response/Clear/Modes/Validation/Cancel/Framing and TestSessionOperationNoRowset cover local behavior. Four-target detail integration requires successful nonzero-warning WARNING_SQL (MySQL must retain details). Compiled only without real modes/proxies; explicit cancellation follows.

### 16.3.4 D30 Explicit Cancellation and One-Shot Targets

```go
type CancelTarget struct { /* Opaque; callers cannot specify connection IDs */ }
func QueryTarget(context.Context, *sql.Conn) (CancelTarget, error)
func CancelQuery(context.Context, driver.Connector, CancelTarget) error
```

Call QueryTarget on idle pinned connections before starting SQL, then CancelQuery from another goroutine. Target capture sends no SQL and cannot occur while original queries/Rows occupy the connection. QueryTarget replaces earlier targets. Each covers only the next ordinary SQL send/response cycle; copies add no attempts, and each allows one cancellation attempt including dial failures, without retries. Unsent/completed/attempted/expired targets return ErrResource and cannot cancel subsequent operations. Prepare/Ping/helpers do not consume the cycle. Sequential batches cover first SQL only, multi-send first chunk only, proving neither whole-batch cancellation nor nonexecution.

- Targets retain original session/mode/user-tenant identity/actual endpoint/TLS state, not new credentials. Reset/user-switch/close/first SQL completion invalidates them via cancellation state independent of protocol gates. SQL completion waits for sent KILL acknowledgment before cleanup/LOB materialization/gate release; unsent auxiliary dialing is canceled. Default context cancellation still closes original sessions without hidden KILL.
- Connector must come from this driver's NewConnector. Reuse credential sources/plugins/TLS/baseline/limits but dial the exact recorded endpoint once without routing/discovery/failover. Network/mode and authenticated user/tenant must match; auxiliary IDs must differ. After ChangeUser supply matching new-identity Connector. Verified original TLS requires verified auxiliary TLS without downgrade. Auxiliary channels are private/unpooled/always closed.
- Dial/auth/init/KILL share earlier call context/ConnectTimeout; close uses CleanupTimeout. Custom dial/credential/auth callbacks must honor context and never reenter the canceled connection; no abandonable dialing goroutines. Private auxiliary channels disable Logger/Diagnostics callbacks to avoid observer reentry while original sessions wait; original logging remains. Preserve complete failures such as permissions while original queries continue independently. Partial KILL writes/lost acknowledgments/malformed-extra responses invalidate originals without replay. Cancellation after KILL write before reading is also unknown.
- Success proves acknowledged KILL only, not SQL nonexecution or original outcomes. Protocol has connection IDs but no server generation/CAS; local coordination cannot eliminate remote disconnect/ID-reuse windows. Real OBProxy/version evidence is especially needed; no absolute cross-server atomicity claim.

TestCancelQueryIdentity/Failure/CompletionBeforeSend/AckCancellation/Guards/AuxIdentity/FixedEntry/WaitsForAck/VerifiedTLS/ResetAndClose and ChangeUserIsolation cover both modes/endpoints/TLS/permissions/one-shot/races/lifecycles. Four-target TestCancelQuerySessions/manual CI source compiles only; each target needs long read-only CANCEL_SQL and evidenced CANCEL_CODE, verifying interruption/session reuse rather than treating any SQL error as cancellation.

## 17. Routing, HA, Authentication, and Import

### 17.1 Address Discovery and HA

- Sources include static lists, extended descriptions, TNS files, OCP APIs; parsing/refresh requires no per-connection threads.
- Strategies include random, round-robin, affinity, groups, primary/replica and read-only roles. Explicit topology adapters carry Java replication/Aurora discovery.
- Roles constrain Connector establishment. Callers hold two standard DBs for primary/read-only access, not SQL-routing pools.
- Go ReadOnly is transaction-level; ordinary SELECT never automatically moves sockets. BeginTx validates/sets current sessions; compatible primaries may host read-only transactions. Callers choose proper DBs rather than BeginTx replacing sockets.
- Bounded concurrency-safe blacklists/health are Connector-shared, never globally spreading tenant errors.
- Address retries share total budgets; authentication/mode/configuration failures never loop endlessly.
- Refresh uses Connector lifecycle context; io.Closer lets DB.Close stop shared refresh. Disabled TNS/OCP starts no goroutines.
- Connector close allows delivered active sessions to finish/close boundedly; discovery cancellation never directly cancels active queries.
- Established physical sessions never fail over internally. Bad sessions exit; pools acquire new ones. Restoring a few variables cannot hide migration/transaction/unknown-execution semantics.

### 17.1.1 D26 Static Multi-Address Contract

Current root API: `Config.Routing RoutingOptions`:

```go
type Endpoint struct {
    Addr string
    Role EndpointRole
    Group, Weight uint16
}
type RoutingOptions struct {
    Endpoints []Endpoint
    Role EndpointRole
    Strategy BalanceStrategy
    MaxAttempts int
    AttemptTimeout time.Duration
    RetryDelay time.Duration
    BlacklistTimeout time.Duration
    ValidationInterval time.Duration
}
```

| Configuration | Semantics, defaults, boundaries |
| --- | --- |
| Endpoints | Nonempty replaces Addr candidates; otherwise derive one Addr candidate with current Role. Maximum 1024, rejecting empty/duplicates after lowercase DNS/IPv6/decimal-port normalization; omitted port 2881. Config.Addr must remain valid. NewConnector copies lists |
| Role | Zero RoleAny; RoleReadWrite/RoleReadOnly select matching explicit labels only, no unknown/opposite fallback. No candidates fails configuration. Labels declare logical services, not actual replication/privilege proof |
| Group / Strategy | Ascending groups. BalanceOrdered retains input; Random shuffles; RoundRobin rotates per shared Connector; Affinity produces weighted nonrepeating order. Weight=0 means 1. No SQL-time rerouting |
| MaxAttempts | Zero uses eligible-role candidate count, maximum 4096. Count actual dials only, not cooldown; one default address still dials once |
| AttemptTimeout | Covers dial/TLS/auth/init. Zero is ConnectTimeout/min(candidate count,MaxAttempts), minimum 1 ns. Attempts/backoff share earlier total/context limits. Initialization timeouts stop, never replay |
| RetryDelay | Zero means 50 ms, cancelable fixed backoff for temporary failures without restarting totals; no wait after MaxAttempts |
| BlacklistTimeout | Zero means 1 second; temporary establishment failures cool down, complete initialization clears. All cooling waits earliest expiry/context. State contains configured addresses only, isolated per Connector |
| ValidationInterval | Zero disabled. Idle since last completed operation including Rows triggers same-socket PING14 under ResetSession lock, CleanupTimeout/context. Failure ErrBadConn lets standard pools reacquire; no in-place rerouting |

Only temporary dialing/handshake disconnects/timeouts on new sessions may switch candidates. Permanent DNS/custom ordinary dial errors/auth rejection/TLS certificate-protocol rejection/mode/configuration/credential callback/initialization failures return immediately with errors.Is/As chains. Derive TLS ServerName from selected endpoints unless explicit. TLS alerts wrapped in net.OpError/wire.IOError still never retry as networking. TCP disconnect without protocol rejection can only be classified as transport failure.

Initialize/lock modes within candidate budgets. Any initialization failure exits candidate loops because InitSQL may have side effects. Existing queries/transactions/statements/LOB/cursor failures never invoke routing/replay.

RoleReadOnly rejects BeginTx ReadOnly=false before sends; true retains sockets. No SELECT analysis/routing; servers enforce ordinary write privileges. Separate standard DBs serve roles; Connector role is fixed. RoleReadWrite still permits standard read-only transactions.

DSNs support `addresses` (comma DNS/IPv4/bracketed IPv6, optional ports), `role=any/readwrite/readonly`, `balance=ordered/random/roundrobin/affinity`, `maxAttempts`, duration `attemptTimeout/retryDelay/blacklistTimeout/validationInterval`. Every `addresses` entry receives common role regardless of parameter order. Mixed roles/Group/Weight use typed Config. Unknown/duplicate/invalid options reject without Java forwarding.

Java `serverAffinityOrder` defaults null. Nonempty [ConfigParser.getLoadBalanceInfoByDefault](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/failover/utils/ConfigParser.java#L426) replaces URL addresses with host:port[:weight] and retains loadBalanceStrategy; it is not an always-ordered switch. Go equivalents are replacing Routing.Endpoints, Endpoint.Weight, explicit Strategy. Ordered follows lists; Affinity weights; Group adds priorities. Java defaults random, Go Ordered. Validate existing uint16 weights; use bracketed IPv6 rather than Java colon splitting. No duplicate list/policy fields or matching Java DSN.

Example: `oceanbase://app@fallback/?addresses=entry-a:2881,entry-b:2881&role=readwrite&balance=roundrobin`. Callers configure addresses/weights; D27 owns refresh/topology proof. Java retry/maxReconnects converge into bounded attempts/total budgets without copying global blacklists/long retries. allowMasterDownConnection cannot cross roles; galeraAllowedState is external-product capability, not guessed OceanBase state SQL.

Root routing.go/failover.go composes config/dial/auth/init. internal/routing owns Address/Options/order/health and standard-library dependencies only, never root/wire/auth/charset. No public subpackages/pools/background goroutines.

### 17.1.2 D27 TNS, OCP, and Topology Refresh Contract

Public APIs stay in one root package; discovery introduces no pool:

```go
type Topology struct {
    Service string
    Endpoints []Endpoint
}
type TopologySource func(context.Context) (Topology, error)
type DiscoveryOptions struct {
    Source TopologySource
    Service string
    RefreshInterval, RequestTimeout, MaxAge time.Duration
}
```

Enable `Config.Discovery` when Source is non-nil. Service must be explicit and Routing.Endpoints absent. Addr is still validated but never a discovery-failure fallback. Without discovery, no goroutine starts. NewConnector only validates/snapshots; first Connect starts one serial refresher. Source returns exclusively owned Topology/Endpoints and honors context. One Connector invokes it serially; callers ensure concurrency safety when sharing functions across Connectors.

| Rule | Current implementation |
| --- | --- |
| Default timing | RefreshInterval=30s, RequestTimeout=5s, MaxAge=5min; reject negatives and MaxAge below RefreshInterval |
| Budget | Discovery waits/dial/auth/init share ConnectTimeout. HTTP requests obey Connector lifecycle/RequestTimeout. Canceling one waiter does not cancel shared refresh for others; each waiter returns promptly under its context |
| Cache/publication | At most 1024 normalized unique addresses; exact Service match/strict Role filtering. Invalid/failed refresh retains successful snapshots within MaxAge; expiry closes fallback. Existing refresh errors fail immediately, otherwise wait for bounded refresh. Failures never trigger caller request storms. Refresh never changes User/Database/Credentials/mode/business sessions |
| Selection | Each Connect pins one successful candidate generation; refresh affects later snapshots only. Dynamic MaxAttempts=0 means one pass through eligible candidates; AttemptTimeout=0 derives from their count/total budget. Generations share retained-address cooldown/rotation; late old dial results feed current state. Trim shared health to current membership to avoid unbounded removed-address accumulation |
| Closure | Connector io.Closer is called by DB.Close. Idempotent Close cancels discovery and waits at most RequestTimeout; noncooperative custom Sources may cause timeout and cannot be forcibly killed. Later Connect returns ErrConnectorClosed. Delivered sessions have no discovery-context lifetime and remain usable/closable. driver.Open closes its private discovery facilities immediately after success/failure |
| Errors/privacy | Return fixed discovery types/categories without URLs/file paths/HTTP bodies/raw Source errors. Authentication/dial errors retain existing errors.Is/As. No implicit logs/disk cache/global registry |

Built-in entry points and formats:

- `ParseAddressDescription(description)` performs no I/O and returns Topology from standalone `(DESCRIPTION=...)`. `NewTNSFileSource(path, alias)` reads explicit regular local files each time, matching aliases exactly case-insensitively. Supports `alias=(DESCRIPTION=...)`, Java `(alias=(DESCRIPTION=...))`, multiple aliases, and `#` comments; standalone DESCRIPTION never replaces missing aliases. No environment paths/default files/global watchers. Follow caller-configured symlinks to regular files only; reject devices/directories and check type/size before/after open. Callers trust/manage files. Go context cannot interrupt filesystem syscalls; stalls/concurrent replacement have no enforceable cancellation guarantee.
- `NewOCPSource(endpoint, client)` requests explicit HTTP(S) URLs. Nil client allowed; construction copies Client while sharing caller-managed transports/callbacks. Standard context covers connection/response reads; injected transports may shorten dial timeouts. Reject embedded credentials/fragments/redirects. Read at most 1 MiB without disk writes. Accept actual Java `data.NET_SERVICE_NAME` + `data.DESCRIPTION` wrappers and direct DESCRIPTION JSON. Explicit wrapper failure statuses reject; metadata parsing is bounded. Wrapper NET_SERVICE_NAME is logical identity; CONNECT_DATA.SERVICE_NAME never automatically switches databases. Direct DESCRIPTION uses SERVICE_NAME as identity. Actual server variants need integration.
- TNS/OCP share ADDRESS_LIST/ADDRESS, TCP HOST/PORT(default 2881), WEIGHT, CONNECT_DATA/SERVICE_NAME, explicit TYPE=master/slave/readwrite/readonly; occurrence order maps groups. Reject duplicate/unknown fields, bad ports/roles, duplicate addresses, trailing junk, invalid UTF-8. Depth maximum 16/nodes bounded; JSON duplicate keys reject case-insensitively. Never guess quoted/escaped TNS or non-TCP descriptions.
- Java OBLB/LOAD_BALANCE/strategy/retry/blacklist fields are source hints: recognize/validate format, but **Go RoutingOptions alone determines execution policy; server descriptions cannot override local retry/security settings**. Do not copy every Java intra/intergroup/blacklist algorithm. Configure RoutingOptions/Endpoint.Group/Weight for required behavior; unknown descriptions reject. Outer wrapper metadata never selects addresses.

Usage:

```go
source, err := oceanbase.NewTNSFileSource("tnsnames.ob", "orders")
if err != nil { return err }
connector, err := oceanbase.NewConnector(oceanbase.Config{
    User: "app", Mode: oceanbase.ModeOracle,
    Discovery: oceanbase.DiscoveryOptions{Service: "orders", Source: source},
})
if err != nil { return err }
db := sql.OpenDB(connector)
defer db.Close()
```

DSNs add mutually exclusive `tnsFile`+`tnsAlias` or `ocpURL`, requiring `discoveryService`, optionally `discoveryRefreshInterval/discoveryTimeout/discoveryMaxAge`. URL-encoded static `addressDescription` is exclusive with these sources/addresses. Prefer typed Config for dynamic sources/mixed roles/groups. Static addresses retains D26 common-role tagging. Unlabeled DESCRIPTION/TNS/OCP entries do not inherit role: restricted Role needs source labels or explicitly wrapped TopologySource based on real service evidence. Service identity is not tenant-authentication proof; credentials/server identity evidence still establish tenancy.

Java replication maps to explicit role snapshots/two DBs. Aurora replica_host_status/innodb_read_only/domain rules are Aurora contracts and never guessed OceanBase SQL. Custom control planes/replication/Aurora services provide addresses/roles/stable Service through TopologySource and the same validation/cache/close flows. No built-in Aurora detector pretending to be native OceanBase topology. Queries/privileges/role changes/tenant proof require evidence per adapter; callback/local-fixture success is not server compatibility.

Dependencies remain root → internal/routing → standard library. Routing owns discovery snapshots/parsing/HTTP/lifecycle without root/wire/auth/charset imports. Connector adapts public configuration/internal types; discovery never enters single-session command paths.

### 17.2 Authentication, TLS, and Extension Points

- Provide Go adaptation paths for Java-registered native, SHA-256, caching SHA-2, clear/old password, Ed25519, GSSAPI, and PAM authentication.
- Actual support combines server negotiation/caller policy. Factories create mutable state per connection, never one concurrently shared handshake object.
- Plugins declare algorithm/stage transport-security requirements, centrally checked before credentials. Public extensions receive auth messages, controlled sending, read-only transport state, and context, not raw sockets/whole Conn. Server requests cannot bypass gates.
- Platform adapters carry GSSAPI/SSPI, OS identities, and transport without Java runtime class loaders.
- TLS uses cloned caller tls.Config without mode-based validation disabling. Weak legacy algorithms require explicit enabling, never silent negotiation.
- External callbacks obtain credentials for new authentication/explicit ChangeUser; never secretly reauthenticate active transactions.

Transport classification and sensitive authentication share this contract; default policy is this design's security choice:

| Scenario | Required behavior |
| --- | --- |
| nil TLSConfig | Do not initiate TLS. Ordinary TCP cannot satisfy plaintext-credential requirements; suitable non-TLS challenge-response remains available |
| Non-nil TLSConfig | Driver must handshake and validate certificate chain/server name. Missing TLS/certificate/hostname failures terminate without plaintext downgrade |
| Custom DialContext | Returns raw protocol transport; driver performs same TLS upgrade/verification. Wrappers, ConnectionState-like methods, or encryption claims are not proof |
| Custom CAs/validation callbacks | RootCAs/ServerName configure private trust/names; callbacks may tighten checks. NewConnector rejects InsecureSkipVerify=true; callbacks cannot replace standard chain/name verification |
| Clear passwords/password-bearing PAM stages | Send only after driver-verified TLS, otherwise fail before stage credentials. Configured TLS objects/server flags/trusted plugins do not replace actual handshakes |
| SHA-256/caching SHA-2 full-auth | Plaintext passwords share TLS gates; non-TLS allows only supported encryption with explicit trusted/pinned public keys. Unauthenticated key retrieval is not identity proof; never fall back to plaintext |
| IPC/platform/external encrypted channels | Same proof required; locality/custom Dial promises do not authorize plaintext. Without verifiable TLS use suitable challenge-response/platform authentication, never implicit plaintext exemptions |

Gates cover initial authentication, every switch, multistage authentication, and ChangeUser, rechecking temporary server algorithm changes. Rejection never calls sensitive-stage credential encoding/sending and closes incomplete sessions. Never send passwords before checking TLS. Earlier challenge digests do not authorize later plaintext full-auth. Logs/packet diagnostics always filter passwords/authentication/keys.[J16]

#### Current D28 Authentication Extensions

`AuthOptions.AllowOldPassword` defaults false and gates `mysql_old_password`, including matching custom factories. Explicit enabling permits eight-byte legacy responses/single-byte old switches without weakening TLS. `client_ed25519` requires 32-byte challenges/password-derived signatures using pure-Go `filippo.io/edwards25519 v1.1.0` scalar/curve operations; standard `crypto/ed25519.Sign` seed rehashing is unsuitable. Algorithm/local-peer success does not prove server plugin installation.

`AuthOptions.PasswordCharset` defaults `utf8mb4` and uses strict existing encoders for built-in passwords/PAM answers; DSN `passwordCharset`. Old-password computation uses encoded bytes, not Java platform encoding/UTF-16 iteration. Custom plugins receive original Go strings and choose encoding. `allowOldPassword` accepts only true/false.

PAM `dialog` factories create independent round state. `AuthOptions.PAM func(context.Context, PAMChallenge) (string, error)` receives a 1-based Round, UTF-8 Prompt, Echo, and Last. Types 2/3 echo and 4/5 hide; the low bit indicates the last question. Empty initial data follows Java's first-password flow with an empty Prompt. Nonempty prompts require valid question types; strip the terminator once only. Distinguish raw `dialog` packets from auth-more-data and verify TLS before every answer. Callback errors, cancellation, malformed prompts, and NUL answers send nothing. A nil callback uses Credentials.Password once only; later prompts reject rather than automatically reusing the password as an OTP. Last never replaces final OK. Callbacks must honor context and concurrent connections; the driver starts no detached authentication goroutines.

Windows `auth_gssapi_client` uses current native SSPI identity, not database passwords. `AuthOptions.ServicePrincipalName` (DSN `servicePrincipalName`) pins SPN over server SPN. Both require bounded UTF-8 then platform UTF-16 conversion. Server data contains NUL-terminated SPN/mechanism plus optional extra terminator; empty mechanism means Kerberos. Accept Kerberos/Negotiate/NTLM only, never arbitrary Windows security packages.

Kerberos requests mutual authentication and verifies final actual SSPI mutual flags, failing without it. NTLM/possibly-NTLM Negotiate requires driver-verified TLS before every token generation; challenge-response alone is not server identity. Create/free credentials/contexts per plugin. Import only `github.com/alexbrainman/sspi` base package, avoiding high-level Kerberos global platform queries/panics. Adapter stays in internal/auth platform files without root/wire dependencies.

Raw multiround plugins may implement `AuthConversation` (AuthPlugin plus Complete()bool). While incomplete, pass full packets including initial 0/1/fe/ff bytes to Respond, applying Security each round and maximum 32 rounds without confusing tokens with OK/more/switch/ERR. Once complete, accept final OK/ERR only. Nil continuation sends nothing; non-nil empty sends explicit empty packets. Initial response still includes auth fields. Existing factory policy/challenge-copy/io.Closer contracts apply.

Unix providers explicitly use `-tags=gssapi` on Linux amd64/arm64/386 and macOS amd64/arm64, limited to Java-standard Kerberos. `github.com/ebitengine/purego v0.11.1` calls RFC2744 name/credential/context/buffer APIs without C compilers/headers at build time. Runtime needs native loader/C runtime; load fixed GSS library names on use. Default builds omit purego, retaining CGO_ENABLED=0 static deployment. Linux tries MIT libgssapi_krb5.so.2 then Heimdal libgssapi.so.3/libgssapi.so; macOS GSS.framework. Never accept server library paths. Missing tags/library/symbols/platforms ErrUnsupported; missing tickets/credentials are authentication errors. Windows defaults SSPI.

Unix acquires default system-cache identity per auth with GSS_C_INITIATE, imports SPN as NT_USER_NAME, requests Kerberos OID/MUTUAL, without database passwords/environment changes/ticket writes/delegation. Deployment sets KRB5_CONFIG/KRB5CCNAME before startup; isolate identities by process/cache, never per-connection os.Setenv. Raw tokens capped at 16 MiB; pin input only during native calls, copy/free output immediately. Clean returned handles/buffers even on native errors. Verify actual mutual flags; close deletes context, releases credentials/names/library references, without extra network tokens.

Native local SSPI NTLM initial tokens and complete driver-verified-TLS peer exchanges have run. TestAuthPluginGSSNativeSessions adds both-mode standard-stream full database/sql initialization, repeated ChangeUser, baseline/Ping, final rejection, TCP closure/pool isolation. Missing sandbox SSPI credentials skips only relevant native tests; current-user processes provide unskipped positive evidence. Database-user changes prove neither Windows identity switching nor authorization. Unix state/cleanup and independent C ABI fixtures have Windows amd64 runtime evidence; Linux/macOS GSSAPI/real-ticket mutual exchanges remain unverified, not replaced by cross-compilation. Synchronous SSPI/GSSAPI ticket/KDC calls may exceed strict context budgets. Check before/after, free expired contexts/refuse delivery, without detached goroutines. GSS errors expose operation/major/minor codes only, excluding system strings with identities/paths/tickets.

Per-instance AuthFactory rejects nil/typed nil. Optional `io.Closer` is invoked exactly once per created instance across switch/success/failure. Close must promptly free local state without networking. Close errors prevent delivery, merge original errors, then close transport. Factories return independent state rather than mutable globals. D30 ChangeUser reuses gates/instance ownership (§16.3.2).

D28 locally implements these auth subitems, Windows SSPI, listed Unix GSSAPI targets, Unix/Named Pipe (§9.0.1), Windows Shared Memory (§9.0.5). Separate-host OBProxy VC (§9.0.6) has offline validation; live acceptance remains. TCP/basic TLS reuse existing implementation; IPC grants no plaintext exemption.

### 17.2.1 D32 Native Go TLS Configuration

Twelve Java TLS options map through existing `Config.TLSConfig`/`crypto/tls`/`crypto/x509` without driver certificate-file/store-password/JVM-type/factory layers. Both modes share SSLRequest, driver TLS, auth gates. Nil does not initiate TLS; non-nil demands standard chain/name validation. Missing support/verification/version-suite/callback failure terminates without plaintext fallback.

| Java option | Go equivalent and differences |
| --- | --- |
| useSsl | Non-nil TLSConfig explicitly requires TLS; programmatic only, no automatic security downgrade |
| serverSslCert / trustStore / trustStoreType | Applications construct RootCAs with x509.NewCertPool/AppendCertsFromPEM/AddCert; nil uses Go/OS trust. Custom pools replace roots; append via SystemCertPool with error checking. DER uses ParseCertificate. No classpath/JVM properties/Java-store interpretation |
| keyStore / keyStoreType | Applications load PEM chain/private keys with tls.LoadX509KeyPair/X509KeyPair into Certificates or GetClientCertificate. Leaf first; hardware keys may implement crypto.Signer under application responsibility |
| trustStorePassword / keyStorePassword / keyPassword | Applications/deployment unlock/convert stores; drivers receive loaded pools/certificates/signers, not passwords. Standard Go loaders do not directly decode JKS/PKCS12/encrypted PEM keys. Ignoring passwords/using empty certificates after failures is not equivalent support |
| enabledSslProtocolSuites | MinVersion/MaxVersion define continuous ranges; Go 1.26 defaults TLS 1.2 minimum/current TLS 1.3 maximum, with explicit minimum recommended. Unconfigured Java follows JSSE, not old comments. VerifyConnection can reject discrete-version omissions without disabling standard checks |
| enabledSslCipherSuites | CipherSuites controls TLS 1.0–1.2 only; Go ignores order, TLS 1.3 ignores field. VerifyConnection may reject actual suites but cannot change TLS 1.3 ClientHello lists. No shared TLS 1.2-or-earlier suites/versions fails handshake. Unknown IDs add no support |
| tlsSocketType | Default pinned Java provider maps to crypto/tls. Callbacks provide additional verification/client-certificate selection, not class/SPI loading, DialContext verification claims, or whole-engine replacement |

The [compilable file-loading example](../tls_example_test.go) reads application CA/client certificate/private key, immediately returning file/empty-trust/certificate/key errors without TLS-less fallback. The example is compiled only, reads no external project files, and stays offline; TestTLSExampleFiles executes the same loader using temporary files. Callers manage real keys/permissions/rotation, excluding sensitive material/full configurations from logs.

NewConnector snapshots certificate bytes/pools/mutable slices/maps. Callers keep referenced callbacks/private keys/parsed x509.Certificate (Leaf/AddCert certificates)/ClientSessionCache immutable or concurrency-safe. Pool Clone isolates later AddCert, not parsed certificate objects deeply. Clone again per session; callbacks cannot mutate shared config. GetClientCertificate honors CertificateRequestInfo.Context; validation callbacks return promptly since drivers cannot interrupt unbounded synchronous application callbacks. VerifyConnection/VerifyPeerCertificate only tighten post-standard validation and retain errors. TLS rejection occurs before database Credentials requests.

TestTLSNativePolicy covers both modes/TLS 1.2-1.3/static-dynamic client certificates/server-verified full chains/encrypted authentication. TestTLSNativeRejection covers TLS 1.2 validation/certificate callbacks, missing shared versions/suites, before credentials. Snapshot tests full init/Ping/QUIT against later config changes; Cancellation tests certificate-context cancellation. Existing VerifiedAuthentication/Required/Failover/FailoverClientCertificateReject/AuthRejectsForgedTLS/ReadTimeout TLS cases retain chain/name/downgrade/routing/missing-identity/read-budget coverage.

Reject all 12 Java DSN names, validation-disable switches, and five DefaultOptions-comment legacy aliases; URLs never load keys/certificates. Pinned evidence: DefaultOptions(L188,L263,L385–L434,L682–L693,L740), AbstractConnectProtocol(L350–L404,L673–L716), DefaultTlsSocketPlugin, OceanBaseX509TrustManager. Registered Java providers contain default only. Local evidence does not accept real certificate deployments/mutual identity mapping/system trust/Java-store conversion.

### 17.2.2 D32 Credential Sources and Pinned RSA Keys

Java `credentialType`/`serverRsaPublicKeyFile` both default null, using credential SPI/application public-key files. Go uses `Config.Credentials`/`Auth.RSAKey` without class/file/global registries; both modes share authentication. Pinned [ENV](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/credential/env/EnvCredentialPlugin.java#L59), [PROPERTY](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/credential/system/PropertiesCredentialPlugin.java#L91), and [RSA parsing](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/authentication/Sha256PasswordPlugin.java#L87) establish mapping.

Nil Credentials uses static User/Password/ProxyUser; callbacks fully replace all three without merging. NewConnector never calls sources; every physical authentication gets fresh credentials, including reconnection/candidate switching/cancellation auxiliaries, never global caching or refresh on ordinary SQL/Ping/transactions. ChangeUser takes explicit Credentials, ignoring sources; later original Connector uses originals (§16.3.2). Configured TLS verifies before sources. Callbacks obey budgets/cancellation/concurrency and cannot reenter connections waiting for auth/cancel. Errors propagate unchanged without static fallback; applications exclude password/tokens from error text.

The [application example](../auth_example_test.go) reads OB_USER/OB_PASSWORD/optional OB_PROXY_USER as complete identity. First two must exist, username nonempty, explicit empty password valid. Application chooses OB_ADDR or retains driver default. No implicit MARIADB_USER/MARIADB_PWD, Java userKey/pwdKey dynamic names, or static-user fallback; implement such policies explicitly in callbacks. Environment is process state, never concurrent per-connection os.Setenv. Other config/secret services share callbacks. Java properties/SPI classes have no Go runtime equivalent; no PROPERTY parsing/Java imports.

Auth.RSAKey defaults nil without file reads/unauthenticated retrieval. Applications os.ReadFile/pem.Decode/x509.ParsePKIXPublicKey trusted provisioned keys into rsa.PublicKey; errors precede NewConnector rather than continuing nil. Example accepts one PKIX PUBLIC KEY with no PEM headers (Java format), rejecting leading/trailing junk/multiple blocks/malformed skipped leading blocks/non-RSA/private keys/certificates. Other formats such as PKCS1 may use application standard parsers; driver accepts parsed keys only without container claims. Validate modulus>=2048/exponent odd>=3, copy RSAKey/N. Later mutation does not affect Connector; rotation needs new Connector/DB without file watching.

Without verified TLS, sha256_password uses pinned keys; caching_sha2 full-auth shares RSA OAEP-SHA1/nonce masking, while cache hits retain challenges. Verified TLS sends protocol passwords protected by TLS; RSA never replaces chain/name validation. Initial/switch/ChangeUser share snapshots/gates. Missing keys require verified TLS. Extra server keys cannot replace pinned ones and are neither requested nor trusted; failure never switches auth methods. RSA protects passwords only, proving neither whole-session encryption/server identity/SQL-result confidentiality. Configure required TLS separately; retain OAEP long-password errors without truncation.

TestAuthExampleCredentials/RSAFile, TestAuthNativeRSAPaths, CredentialRotation/Failure, RSARejection, AuthJavaDSNRejected provide both-mode initial/switch/ChangeUser peer decryption, nonce/identity, key-mutation isolation, same-Connector refresh/failure evidence. Local protocol/examples only: real plugin enabling/privileges/key distribution-rotation/secret-service availability remain unaccepted and do not close remaining D28/D32 scope.

### 17.3 LOCAL INFILE

Retain local import, default-denying arbitrary server-requested local file reads. Callers explicitly supply Readers/request-name authorization callbacks, scoped per request/Connector without global file whitelists.

Imports have context, size/time limits, explicit Close ownership, and correct completion/discard on protocol rejection/interruption. Do not remove the feature because undesigned, or enable arbitrary reads for completeness.

#### 17.3.1 D29 Implemented Controlled Import

The public entry point is `Import(ctx, exec, query, input)`. `exec` accepts `*sql.DB`, `*sql.Conn`, or `*sql.Tx`, and input is a one-shot `*LocalInfile`. Configuration is separate from application-provided SQL. Import never begins, commits, or reexecutes transactions. DB inputs pin a physical connection; Conn/Tx retains the caller's original session. No additional driver pool or package is introduced.

OceanBase MySQL LOCAL INFILE was observed implicitly committing both the import and earlier DML. The driver therefore requires autocommit, rejects active local/manual transactions and XA-used sessions with ErrUnsupported before SQL/upload, and leaves prior DML available for rollback. A failed autocommit upload can still leave partial effects. Oracle retains its transaction path pending real validation.

```go
input := &oceanbase.LocalInfile{
    Name:     "input",
    Reader:   strings.NewReader("1\tvalue\n"),
    MaxBytes: 8 << 20,
}
result, err := oceanbase.Import(ctx, db,
    "LOAD DATA LOCAL INFILE 'input' INTO TABLE target", input)
```

- Caller-provided `Reader` remains caller-owned and is never closed by the driver; an exactly matching `Name` is required. Alternatively, `Open func(context.Context, string) (io.ReadCloser, error)` authorizes the current request name and returns input. The driver closes every non-nil returned `Reader` exactly once, including when the callback also returns an error. These sources are mutually exclusive; a nonempty `Name` still requires exact matching in callback mode.
- Strictly decode names via session charset; require nonempty/no NUL/bounded length before matching/callback. Contents remain raw bytes, not transcoded. Driver maps no names to files/URLs/global lists.
- Handshake negotiates offered `CLIENT_LOCAL_FILES` as capability only. Ordinary Exec/Query/init/internal commands have no input authorization. Only the single Import command accepts one request; repeated/malformed/mismatched requests discard without sending their data.
- `MaxBytes=0` uses `Limits.MaxLOBBytes`; chunks are bounded by 64 KiB and the single-message limit, reusing long-parameter bounded reads, exact-limit probes, and cancellation checks. `Timeout=0` means 30 seconds, covering connection pinning, waiting, sending, and final acknowledgment under any earlier context deadline. Reader/Open/Close are synchronous and must cooperate with cancellation and return. The driver does not conceal blocking in detached goroutines, and callbacks cannot recursively access the same constrained connection.
- After EOF, close driver-owned Readers before sending empty terminators, then require final OK/ERR; other responses discard. Continued messages retain identity/trace/diagnostics across sequence wrap, reusing all streams.
- Input errors, cancellation, limits, or Close errors send no successful terminator and immediately discard the physical session. Complete final server ERR preserves structured errors and allows reuse. `ErrLocalInfile` identifies errors and suppresses source-provided `driver.ErrBadConn` retry signals. Input instances cannot be reused or consumed concurrently. Failures may already have database effects; the driver claims no automatic rollback. Actual server/storage-engine behavior must establish transaction effects.

Local peers cover both modes/full compression-OB20 round trips. MySQL 4.4.2.2 real tests passed transaction refusal/earlier-DML rollback and autocommit content/counts; see the [live report](mysql-live-validation-2026-10-19.md). Four-target `TestLocalImport`/manual CI has default MySQL SQL; Oracle needs profile `LOCAL_IMPORT_SQL` containing `{table}`, fixed request name `input`. Mandatory acceptance fails missing configuration; other target combinations remain unverified.

## 18. slog, Diagnostics, and Full-Link Tracing

### 18.1 Logger Injection Contract

```go
type Config struct {
    // Common connection fields and advanced configuration omitted.
    Logger *slog.Logger
}
```

- `Logger == nil` emits no driver logs.
- Use `cfg.Logger` or derived With loggers. Never call `slog.Default()`, `slog.SetDefault()`, package-level `slog.Info/Error`, or fall back to `log.Default()`, fmt.Print, or stderr.
- No global `SetLogger`; DBs/Connectors may use different loggers.
- No custom Logger interface, forced JSON/Text handler, changed caller levels/destinations.
- Check Enabled to avoid unnecessary work; use LogAttrs/operation contexts for structured data.
- Driver defines timing/fields; caller handlers own output/sampling/persistence/rotation.

### 18.2 Logged Content

Suggested fields: driver, operation, mode, server/connection/request IDs, duration, rows_affected, error_code, sql_state, retry_stage. Default excludes passwords/auth packets/DSNs/parameters/LOB content/raw SQL/full server errors.

Profiling uses durations/command categories/optional fingerprints. Explicit independently enabled size-bounded callbacks handle necessary SQL/packet diagnostics, still filtering authentication; Debug never automatically leaks sensitive contents.

Logs primarily diagnose internal/slow requests, not Error-log every returned application failure. Collect fields under network locks and invoke external handlers after unlocking. Handlers avoid synchronous recursion into the same limited pool. No unbounded asynchronous goroutines/queues.

### 18.3 Full-Link Tracing

OB20 Full Link Trace and slog are distinct capabilities:

- Trace/span/ExtraInfo/sampling/server control follow OceanBase protocol; enabled tracing works without slog.
- Typed context helpers carry request trace/application module/action/client info, never global mutable state.
- Optional bridges/caller data integrate OpenTelemetry without mandatory exporters.
- Query/fetch/LOB pieces/batches/establishment have explicit span/request boundaries, never leaking previous request tracing across pooled connections.[J12]

### 18.3.1 D25 Implemented Diagnostics and Tracing Contract

Root APIs are `DiagnosticOptions`/RequestDiagnostic/`PacketDiagnostic`/`TraceInfo`/`WithTraceInfo`. wire only handles protocol/sampling/bounded snapshots without root/external handlers. No dependencies/global tracer/exporter/background queues.

- Explicit `Config.Diagnostics` Request/Packet callbacks enable metadata diagnostics: SQL SHA-256 fingerprints/encoded sizes only; packets expose direction/bytes/logical start sequence/error flags. Always exclude auth/raw SQL/parameters/LOB/server-error text. Debug adds no data/extra diagnostic SQL.
- Ordinary events use Debug. When `SlowThreshold > 0` and the threshold is reached, use Warn; zero disables slow-request upgrades. `MaxRequests` bounds requests per locked interval and `MaxPackets` bounds messages per request. Each defaults to 64 at zero, with legal range 0–1024. Reaching limits drops diagnostics only, reports drop counts, and never interrupts business operations. Without a logger or callbacks, no diagnostics are collected.
- Actual protocol commands define requests: prepare,execute/161,fetch,long pieces,LOB SQL,batch items,cleanup timed separately. Multi-send joins write/read snapshots and waits writers before delivery. Rows duration includes caller consumption, measuring connection occupancy rather than pure server execution. One establishment-total event includes dial/auth/init without auth contents.
- `Enabled`/handlers/Request/Packet run synchronously after driver unlocks. Callers handle concurrency/avoid same-pool recursion. Callbacks may observe other requests; no global cross-callback ordering.
- `Protocol.FullLinkTrace`/DSN `fullLinkTrace=auto|off|required` defaults auto; request OB20 bit 8/FLT bit 14/new-extra bit 15/ShowTrace bit 18. FLT requires confirmed OB20; missing required/invalid echoes fail before work. Logging-off retains FLT; unconfirmed keeps ordinary behavior.
- New extra FULL_TRC key 2003; old extras use `full_trc` ObObj varchar pairs. Validate nested TLV/control lengths/probabilities/time ranges within 64 KiB; skip unknown new fields by length. Enabled FLT old objects accept implemented null/varchar only. First outer command frame carries app/span, continuations do not. Reserve extra budget while retaining CRC/identity/sequence/actual byte evidence.
- UTF-8 byte limits ClientIdentifier/Module/Action/ClientInfo=64/64/64/256; reject NUL/invalid UTF-8/excess, never truncate. Current context provides all four per-command fields, including empties to clear pooled remnants, excluded from logs. TraceID/ParentSpanID are 16-byte OceanBase wire-order IDs; bridges convert external conventions.
- Server control is per-session with atomic partial merges. Level/sample/record policy(1 all, 2 slow, 3 sampled+slow, 4 off)/print sample/slow threshold/ShowTrace determines sampling/output. No sampling before initial control; ShowTrace can force it. Sampled commands get new spans, retaining caller TraceID or generating one, ParentSpanID only as parent. Request callbacks bridge feedback query-start/end microseconds/diagnostic IDs; next commands clear timing.
- Completed-span JSON driver logs(group 1/field 1)/ShowTrace(group 2005/field 2050) contain IDs/fixed driver name/timing only. Retain at most one completed span per connection, sent once in next extra with original ID. No extra SQL/replay. Last spans may never upload before close/discard; establishment auth has local total events only, not fabricated server spans.

Local verification covers fixtures/nil loggers/handler-callback reentry/sampling updates-off/pool clearing/fragments-CRC/malformed data/fuzz. Four-target `TestFullLinkTrace` registers direct off/proxy required; server traces must separately prove app clearing/IDs. Client events do not prove server visibility. No database/proxy integration results or real acceptance are claimed.

## 19. Java-to-Go Complete Feature Coverage Matrix

This table is a delivery checklist. Work may proceed in phases, but later implementation order does not remove capabilities from scope.

| Java module | Go implementation approach | Completion evidence |
| --- | --- | --- |
| M01 Driver/DataSource/JDBC API | Registration, DriverContext, Connector, standard sql objects; concrete extension types/functions | Shared entry points in both modes/standard-library contracts |
| M02 URL/Options/address descriptions | Strict DSN, typed Config, TNS/OCP, topology | Option migration/escaping/error/refresh tests |
| M03 Transport/TLS/authentication/credentials | net.Conn, verified TLS where required, unified credential gates, platform adapters, per-instance auth factories/credential callbacks | Initial/switch/multistage auth, zero plaintext credentials without TLS, no downgrade, platform tests |
| M04 Handshake/mode/capabilities | Identify actual mode, validate expectations, separate versions/negotiated capabilities | Mismatch/missing-capability/OBProxy tests |
| M05 Connections/sessions/local transactions | sql.Conn/Tx, BeginTx, savepoints, schema/catalog, warnings, ChangeUser, reset, count policy | Transaction attributes/restoration/matched-changed counts/resource invalidation |
| M06 Ordinary/client-server preparation | COM_QUERY, client prepare, server prepare/execute, PrepareExecute, cache/checksum | Text/binary/combined equivalence/no duplicate execution |
| M07 Binding/batching/long data | ?/:name, sql.Named, Parameter encoding, batch strategies, LONG/PIECE_DATA | Lexical/count/long-data/per-item-count/failure matrices |
| M08 Results/keys/multiple results/cursors | Standard Rows, Execute/Execution summaries, OUT/CursorOut, REF CURSOR columns, Cursor, mutation/refresh | Mixed ordering/count/error prefixes, unread cleanup, scroll/update conditions |
| M09 All types/encoding/time | Standard values, Scanner/Valuer, explicit special types, charsets/timezones | All-type round trips, NULL/empty/precision/region-timezone matrix |
| M10 LOB/complex types | Blob/Clob, Locator V1/V2, OpenTableBlob/MySQL emulation, Object/Array, dictionaries | Same-transaction reads, key targeting, chunk I/O/truncation, nested/synonym/invalidation tests |
| M11 Database/result/parameter metadata | ColumnTypes, structure functions, parameter plans/dictionaries | Compare real dictionaries in both modes |
| M12 Multi-address/HA | Sources/topology/load/groups/blacklists/connection failover | Fault injection/refresh/no transaction migration |
| M13 Pooling | sql.DB pool; reset/Validator reuse contracts; DB.Stats instead of custom JMX | Pool stress/cancellation/sql.Stmt reuse after reset |
| M14 XA | XID/explicit functions, DBMS_XA/native commands/recovery | 2PC/join/resume/process-network failures |
| M15 Logging/packet diagnostics/OB20/FLT | Injected slog, bounded callbacks, OB20/checksums/request tracing | Nil silence/isolation/trace leakage/checksums |
| M16 Build/SPI/platform/performance | Go modules/interfaces/build tags/test/fuzz/bench | Go 1.26/target-platform CI |
| LOCAL INFILE | Explicit Reader/callback, request authorization/size limits | Arbitrary-file rejection/interruption/cleanup |
| JDBC scroll/updatable/legacy APIs | Cursor/parameterized DML, capabilities without copied JDBC names | Real support/semantics/concurrent-change tests |
| OSGi/JMX/Java serialization/compatibility classes | Go modules/interfaces, DB.Stats/diagnostics; caller-explicit serialization | Observable Go capabilities without Java runtime dependencies |

Maintain a separate per-option mapping identifying Go configuration, standard alternatives, platform adaptation, or Java-API-only compatibility. Missing same-named configuration must not be mistaken for feature coverage. This high-level matrix alone cannot establish completed implementation.

## 20. Verification Matrix and Implementation Dependencies

### 20.1 Verification Layers

1. **Standard-library contracts**: interface assertions, conversion, NumInput, NextResultSet, ErrBadConn, Validator, SessionResetter.
2. **Protocol tests/fuzz**: packet boundaries, compression, OB20, truncation/oversize/unknown fields, NULL bitmaps, types/nesting.
3. **Real servers in both modes**: ordinary/client/server/combined execution, all types, procedures, batches, LOB, cursors, metadata, XA.
4. **Direct/OBProxy**: negotiation, tracing, usernames, session behavior, routing, failures.
5. **Fault injection**: pre-send disconnect, partial writes, lost responses/COMMIT responses, cancellation-completion races, LOB interruption, XA recovery.
6. **Resources/concurrency**: early Rows closure, leaks, pool reuse, reset generations, credential refresh, logger isolation, shared addresses.
7. **Toolchains/platforms**: Go 1.26, Linux/Windows, supported socket/auth adapters, critical parsing/boundaries on 386.
8. **Performance**: CRUD, prepare caches, batching, streaming LOB, fetch. Verify correctness before throughput. Local protocol benchmarks cover both-mode basics, timing/allocations including peers/assertions. Offline resources cover rebuilt sessions and continuous same-session mixed execution, directly checking ownership/cache boundaries/pools/physical closure, recording post-GC heap/goroutines; see [Local Benchmarks](go-driver-benchmarks.md). Real performance, OS handles, full concurrency/fault combinations, and production long-duration resource trends await verification.

Type matrices cross mode, text/binary, NULL/empty, max/min/overflow, encoding, timezone, precision at minimum. Mock-only complex/locator tests cannot claim server compatibility.

D01 establishes CI installing actual Go 1.26.x for Linux/Windows amd64 test/vet/dependencies. D02 adds import-edge checks/protocol fuzz/386; D04 requires explicit integration; D06 adds supported-platform concurrent resources. Driver is pure Go without `C` imports, while `-race` requires cgo by toolchain design. CI continuously verifies `CGO_ENABLED=0`; run races manually in cgo environments. Extend checks with capabilities and complete D32 matrix rather than delaying CI until delivery. Required combinations/status aggregation are fixed in [Development Progress §5](go-driver-development-progress.md#5-validation-combinations-and-evidence), not implementer-selected successful subsets.

### 20.2 Acceptance Scenarios for These Contracts

| Contract | Required evidence |
| --- | --- |
| Mixed results/summaries | UPDATE→SELECT→UPDATE order/RowSetIndex; zero/unknown counts; command-only; final CALL versus OUT controls; full uint64 IDs/count overflow |
| Errors/finalization | Midstream ERR/partial disconnect/direct QueryContext failure retains summaries; NextResultSet/Rows.Close/Execution.Close consistency; bounded early drain; limits/cancellation/incomplete/error prefixes never misreported unexecuted |
| Connection-bound outputs | Publish Cursor/LOB after full consumption/Close only; no leaks on conversion failure; multiple handles/parent leases/new Fetch contexts; handles survive Execution close |
| Counts | Real both-mode behavior; MySQL matched/changed no-op UPDATE/INSERT/DELETE/upsert; Oracle ChangedRows rejection; cursor/emulated BLOB zero-change is not missing |
| Authentication gates | No sensitive clear/switch-native-to-clear/PAM/full-auth writes without TLS; missing support/certificate-name failure never downgrades; forged Dial security rejected; ChangeUser rechecks |
| MySQL emulated BLOB | Complete/composite keys, quoted identifiers, missing/NULL/excess/nonexistent keys, mutated caller Key, NULL/empty contents; chunk beyond materialization; first/middle/end ReadAt/WriteAt, identical writes, append/truncate, no disconnect replay |
| BLOB transactions/resources | Conn reads/Tx-only writes; uncommitted visibility, locked deletion/concurrent updates, rollback/isolation/generations; no implicit commit or auxiliary original-transaction reads |

These are implementation acceptance requirements, not yet executed. Simulation verifies protocol gates/boundaries; real OceanBase/OBProxy verifies SQL/counts/auth/transactions. Neither substitutes for the other.

### 20.3 Implementation Dependency Order

```text
Public contracts + mode/capability model
  → Packet streams/handshake/authentication/basic types
  → Session state + ordinary/prepared execution + Rows + local transactions
  → Exact/extended types + OUT/RETURNING + metadata
  → Batching + LOB + complex types + Cursor
  → Complete HA/platform authentication + XA + FLT
  → Full integration/fault/performance matrix
```

This is dependency order, not permission to deliver early items only. Logger injection, cancellation, cleanup, error boundaries, and real functional validation accompany each module rather than being postponed.

Each completion record includes server/OBProxy versions, mode, path, evidence, limitations. Without real-server evidence, mark implemented/pending integration, never completed adaptation.

## 21. Protocol Conditions Still Requiring Evidence

Implementation must establish these through source/server evidence; they do not justify removing features:

- Exact target-version combinations for reset, PrepareExecute, offset fetch, new metadata, LOB V2, array binding.
- Oracle same-session cursor retention/interleaving with LOB/type dictionaries/DML.
- Complete OUT/INOUT/REF CURSOR response order across both modes/standard-extended execution.
- Region/LTZ bytes and semantics, complex versions/synonyms, locator lifetimes.
- Actual platform targets/necessary dependencies for authentication/special transports.
- Ignored/unsupported Java test scenarios; source class existence alone is no behavior commitment.

Core/extensions have local implementations. Pre-upgrade Go 1.25/1.26 Windows builds/tests/lint were run; current Go 1.26 verification is in Development Progress. Platform runs/live compatibility need separate evidence. [Development Progress](go-driver-development-progress.md) governs implementation/dependency/acceptance status; architecture/local peers are not server acceptance.

## 22. Core Evidence Index

Detailed Java references:

- [J01: Actual authenticated mode and OB capabilities][J01]
- [J02: Standard/extended commands and execution][J02]
- [J03: Mode-specific type codes and unknown fallback][J03]
- [J04: Ordinary/OUT/cursor response dispatch][J04]
- [J05: Reset and prepared-cache cleanup][J05]
- [J06: Auxiliary LOB connections][J06]
- [J07: Java failover query replay decisions][J07]
- [J08: PrepareExecute/offset fetch conditions][J08]
- [J09: Batch/array-binding dispatch][J09]
- [J10: Updatable DML and row identity][J10]
- [J11: XA in both modes][J11]
- [J12: OB20 full-link fields/control][J12]
- [J13: CLIENT_FOUND_ROWS/Java useAffectedRows][J13]
- [J14: MySQL emulateLocators/native BLOB paths][J14]
- [J15: Emulated BLOB table/key/column identity and chunk SQL][J15]
- [J16: Plain-password SSL requirements/handshake gates][J16]
- [J17: Explicit Oracle RETURNING PrepareExecute requirement][J17]

Go contracts, pinned to Go 1.27.2 source while restricting design to APIs already in Go 1.26:

- [G01: ErrBadConn duplicate-execution constraints][G01]
- [G02: database/sql ErrBadConn retries][G02]
- [G03: driver.Value and child cursors][G03]
- [G04: NamedValueChecker contract][G04]
- [G05: Go 1.27 RowsColumnScanner, not required here][G05]
- [G06: context.AfterFunc stop/race rules][G06]
- [G07: Scanner/sql.Out][G07]
- [G08: ResetSession/Validator][G08]
- [G09: Raw must not leak driverConn][G09]
- [G10: sql.Result/driver.Result wrapping][G10]

[J01]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823
[J02]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L470
[J03]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java#L326
[J04]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L2483
[J05]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L152
[J06]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseConnection.java#L2733
[J07]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/failover/AbstractMastersListener.java#L445
[J08]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778
[J09]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ServerPreparedStatement.java#L701
[J10]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4UpdatableResultSet.java#L1125
[J11]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseXaResource.java#L162
[J12]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/flt/FullLinkTrace.java#L69
[J13]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L312
[J14]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ResultSet.java#L2154
[J15]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/BlobFromLocator.java#L90
[J16]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/authentication/ClearPasswordPlugin.java#L80
[J17]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ServerPreparedStatement.java#L328
[G01]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L152
[G02]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L1579
[G03]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L49
[G04]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L404
[G05]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L458
[G06]: https://github.com/golang/go/blob/go1.27.2/src/context/context.go#L315
[G07]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L452
[G08]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L299
[G09]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L2072
[G10]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L3581

### 2026-10-19 Charset Compatibility Addendum

HKSCS/HKSCS31 use UTF-8 payloads according to Java Options.getCharacterEncoding, with OceanBase protocol IDs 152/153. GB18030_2022 uses its separate 2022 mapping/default ID 217 (other known 2022 collations 216–222 share encoding), retaining actual SET NAMES names. Existing GB18030/x-text mappings stay unchanged. Go still encodes handshake identities by configured charset, not Java's separate UTF-8 handshake-selection policy.

Mapping sources/pinned versions are in internal/charset/gb18030_2022.go: OceanBase charset implementation and OpenJDK 21. Tests include independent byte vectors, all BMP scalar encoding digests, 23940 double-byte positions and 39420 BMP four-byte decode digests, plus auth/SQL/text-binary parameters/results. Independent comparisons/local peers do not prove real database/proxy charset support.
