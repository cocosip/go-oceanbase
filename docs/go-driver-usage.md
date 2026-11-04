# OceanBase Go driver usage guide

This guide describes the current public API. See [development progress](go-driver-development-progress.md) for implementation status and the [compatibility matrix](go-driver-compatibility-matrix.md) for target-specific limits. The minimum Go version is 1.26. Networked examples have compilation evidence; live acceptance is limited to the explicitly recorded tests in the [recovery report](live-validation.md) and [charset/NUL follow-up](live-validation.md).

## 1. Connections, compatibility modes and logging

Import `github.com/cocosip/go-oceanbase`. A named import registers the `oceanbase` driver with `database/sql`; no additional blank import is needed. Use `*sql.DB`, `*sql.Tx`, `*sql.Stmt` and `*sql.Rows` for ordinary application operations.

- `NewConnector(Config)` validates and snapshots configuration without network access; pass it to `sql.OpenDB(connector)` to create a pool.
- `sql.Open("oceanbase", dsn)` accepts string configuration and also connects lazily. Use `PingContext` with a timeout to verify connectivity.
- `ModeMySQL` and `ModeOracle` select OceanBase compatibility modes. A mismatch with an explicitly requested mode fails the connection. `ModeAuto` pins the actual mode on each physical connection after authentication; it does not translate SQL dialects.
- In MySQL mode, `Database` selects the initial database. For Oracle initial-schema, quoting and case rules, see [architecture §6](go-driver-architecture-design.md#6-configuration-model-and-defaults). Do not treat an Oracle schema as a MySQL database operation.

DSNs use URL syntax. In these placeholders, `%40` escapes `@` within the username:

```text
oceanbase://app%40tenant:password@127.0.0.1:2881/demo?mode=mysql
oceanbase://app%40tenant:password@127.0.0.1:2881/?mode=oracle
```

URL-escape password characters such as `@`, `#`, `/` and `?`. Prefer `Config`, or construct URLs with `net/url.UserPassword`; do not concatenate credential-bearing URLs manually. Supported DSN option names are case-insensitive (`connectTimeout`, `connecttimeout` and `CONNECTTIMEOUT` are equivalent), while option values retain their original case and characters. Duplicate names after case folding are rejected, including duplicates with the same value. DSNs reject unknown keys; supported settings are the documented Go option names in the [architecture design](go-driver-architecture-design.md). Do not log DSNs or complete configurations.

Compression defaults off, matching Connector/J 2.4.14. To enable it when offered by the server, set `cfg.Protocol.Compression = oceanbase.FeatureAuto` or DSN `compression=auto`; absent server support falls back to ordinary packets. `FeatureRequired` / `compression=required` is an explicit Go extension that fails when support is absent. OB20 still takes precedence over optional compression. The zero policy `FeatureDefault` resolves to compression off and all other protocol policies auto; use named constants instead of numeric values. Off=1 and Required=2 remain stable; Auto moves from 0 to 3, while zero now selects the per-option default.

Do not copy option names or aliases from other drivers: only the documented Go option names are accepted, unknown keys and historical aliases are rejected, and the database/port come from the URL path and host:port or from `Config` before `NewConnector`. Configure TLS through TLSConfig. Durations are Go durations: 30 seconds is `connectTimeout=30s`, not 30000; `connectTimeout=0`/`0s` keeps the 10-second default. Booleans accept only true/false, not empty values, 1/0 or quoted values.

Logging is supplied by the caller. A nil logger is silent; the driver does not call `slog.Default`, register a global logger or write log files:

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelWarn,
}))
cfg.Logger = logger
```

Configure standard pool settings such as `db.SetMaxOpenConns`, `SetMaxIdleConns` and `SetConnMaxLifetime` in the application. Pass an operation-specific `context` to each call instead of a shared context without a deadline. Establish application state after a connection or commit failure before deciding whether to retry; never replay non-idempotent writes unconditionally.

The complete [pool example](../pool_example_test.go) shows standard setters, caller-supplied slog, a one-time readiness check for multiple connections and db.Stats. Go defaults to unlimited open connections, at most two idle connections and no idle-time or lifetime expiration; choose explicit deployment limits. SetMaxIdleConns is a ceiling, not a minimum. Repeated DB.Ping calls do not warm multiple sessions. At startup, the example holds multiple sql.Conn leases concurrently and pings each; failure/cancellation releases all leases, while healthy sessions may remain in the pool. The timeout must include waiting for pool capacity. This does not maintain a long-term minimum connection count.

Reuse DB over the application's lifetime and Close it at shutdown. sql.Conn.Close returns a lease; it does not establish that the remote physical session has closed. Idle/lifetime expiration is deferred and does not interrupt a held transaction/result. Create separate Connector/DB pairs to isolate identity, compatibility mode, tenant and lifetime. Every address in one Connector's `Routing.Endpoints` or discovery snapshot must be deployment-verified for the same logical OceanBase service and tenant; the driver does not infer tenant identity from the address or username. Do not reuse one Connector for independent pools: closing one DB closes the shared Connector and prevents subsequent connections in the others. Routing.ValidationInterval defaults to 0, disabling borrow validation; Java poolValidMinDelay=0 instead validates on every borrow. Managed reset does not clear arbitrary session variables; use ResetFull for full isolation. See [architecture §5.1](go-driver-architecture-design.md#51-d32-standard-connection-pool-and-lifecycle) for default differences and the actual meaning of staticGlobal.

For Windows Shared Memory, use `Config{Network: "sharedmemory", Addr: "MYSQL"}` or DSN `network=sharedmemory&socket=MYSQL`. Addr is the server object base name and may have a Local/Global namespace prefix. It cannot be combined with multiple addresses/discovery and does not fall back to TCP. Set TLS ServerName explicitly; shared memory receives no exemption for plaintext authentication. **After sending an allocation request, the dialer holds the lock while waiting for a response without a correlation ID. Cancellation/connection timeouts are soft budgets: an unresponsive server can keep the dial blocked.** Established I/O supports deadlines and Close. Auto/MultiSend batch modes fall back to sequential execution; Close does not guarantee delivery of the final block. See [architecture §9.0.5](go-driver-architecture-design.md#905-d28-windows-shared-memory) for resource and interoperability limits. Live-service acceptance is pending.

For embedded OBProxy VC, follow the [NewOBProxyDialer example](../obproxy_example_test.go) and [architecture §9.0.6](go-driver-architecture-design.md#906-d28-embedded-obproxy-vc). First build the separate C host. On Linux amd64/arm64, build the Go application with `-tags=obproxy`, supply absolute paths to the trusted host and native library plus the native `-o` configuration, and assign the callback to `Config.DialContext`. Set `Network="unix"` and `Addr=".obproxy/obproxy.domain"`. The factory does not start a process; the first dial starts the host and loads the native library, isolating its signal handlers, locale and allocator from Go. Application data uses a standard Unix connection for the common authentication, TLS and SQL paths. TLS requires an explicit ServerName; IPC receives no plaintext-credential exemption.

Waiting for initialization respects context cancellation, but native initialization may continue. DB.Close does not stop the host; EOF on the control pipe when the parent exits terminates it. Only one host/library/configuration/working-directory combination can be selected, and failures are cached. Different configurations require separate parent processes and directories. Startup replaces `.obproxy/obproxy.domain`; use an exclusive working directory and do not chdir at runtime. The absolute socket path is limited to 107 bytes; proxyConfig is limited to 4095 bytes of UTF-8 without NUL. Native stdout/stderr are discarded in the host. Native configuration controls native file logs; the Go Logger does not. Go continues to use only the supplied slog logger. The ordinary driver needs no C compiler. Real native-library and service acceptance remains pending.

To bound network read waits independently, set `Config.ReadTimeout = 5 * time.Second` or DSN `readTimeout=5s`. The default 0 disables it in both modes. Each underlying Read starts a new timer, and an earlier context deadline still applies. Long results that keep receiving data still need a business context to limit total duration. A timeout closes the connection and preserves the network error (use errors.As to net.Error and check Timeout); already-sent operations are not replayed automatically. See [architecture §12.5.1](go-driver-architecture-design.md#1251-d32-implemented-network-read-timeout).

## 2. TLS and authentication configuration

Applications that require TLS pass a `*tls.Config` through `Config.TLSConfig`, with trusted CAs and a server name. This example assumes the application has loaded `caPEM`:

```go
roots := x509.NewCertPool()
if !roots.AppendCertsFromPEM(caPEM) {
    return errors.New("no valid CA certificate")
}
cfg.TLSConfig = &tls.Config{
    MinVersion: tls.VersionTLS12,
    RootCAs: roots,
    ServerName: "oceanbase.example.com",
}
```

Do not substitute `InsecureSkipVerify` for CA/hostname verification. An unset `TLSConfig` does not enable encryption; configure TLS to match deployment requirements. See the [architecture](go-driver-architecture-design.md) for authentication plugins, credential callbacks and platform requirements.

For mutual TLS, load the client certificate chain/private key with `tls.LoadX509KeyPair` into `Certificates`. For dynamic selection, use `GetClientCertificate` and respect `CertificateRequestInfo.Context()`. The complete [file-loading example](../tls_example_test.go) checks CA, certificate and private-key errors. The application/deployment must unlock and convert JKS/PKCS12/encrypted private keys; the driver neither reads Java keystore passwords nor parses their DSN aliases. MinVersion/MaxVersion, CipherSuites and additional verification callbacks use Go semantics directly; CipherSuites does not control TLS 1.3. See [architecture §17.2.1](go-driver-architecture-design.md#1721-d32-native-go-tls-configuration).

Use standard `net.Dialer` / `net.TCPConn` with `Config.DialContext` for TCP source interfaces, keep-alive, buffers and NODELAY; no extra driver configuration structure is needed. For example, `cfg.DialContext = (&net.Dialer{KeepAlive: -1}).DialContext` disables default TCP keep-alive while the driver still handles TLS and initialization. The complete [compilable example](../tcp_example_test.go) shows explicit LocalAddr, KeepAliveConfig and cleanup after setter failures. This is programmatic configuration; Java TCP DSN parameters do not take effect. The source IP must belong to a local interface. Captured configuration must be immutable and safe for concurrent callbacks. See [architecture §9.0.2](go-driver-architecture-design.md#902-d32-native-go-tcp-configuration).

The `User`, `Password` and `ProxyUser` returned by `Credentials` **replace the complete static identity**. Oracle proxy login uses a separate `ProxyUser`, keeping the authenticating user distinct from the proxy target. Bracketed username syntax is unsupported; ordinary connection attributes cannot override the target. On exactly OceanBase 4.4.2.2, `ChangeUser` with a nonempty `ProxyUser` returns `ErrUnsupported` before sending because that target accepts the command but leaves the authenticating account effective (B21); initial proxy login and clearing the target remain available. Prefer keyed struct literals so that adding fields does not break positional literals. See [architecture §6.3](go-driver-architecture-design.md#63-d32-implemented-proxy-user-login).

Credential sources and pinned RSA public keys use the existing configuration; see the [application example](../auth_example_test.go). Credentials reads the application source for each physical authentication and never falls back to static identity after a callback error. The example requires OB_USER/OB_PASSWORD to exist, allows an explicitly empty password and treats OB_PROXY_USER as optional. Callbacks must respect context and support concurrent calls; environment variables are not per-connection isolated storage. A Connector pins the first successful `User`/`ProxyUser`, so password rotation can stay on the same Connector while a logical identity change requires a new Connector. Auth.RSAKey accepts an application-loaded *rsa.PublicKey, copied by NewConnector; return loading failures directly. RSA protects SHA authentication passwords only; it does not replace TLS session encryption or server identity verification. Automatic server-public-key retrieval is unsupported. The example parses one PKIX PUBLIC KEY; use the appropriate standard parsers for other formats. See [architecture §17.2.2](go-driver-architecture-design.md#1722-d32-credential-sources-and-pinned-rsa-keys).

`Session.Collation` applies only to MySQL mode and must match `Session.Charset`; Oracle explicitly rejects it. `Session.CompatibleMysqlVersion` selects Connector/J-compatible MySQL 5 or 8 result metadata and temporal text conversion, defaults to 5, and is also accepted as DSN `compatibleMysqlVersion=5|8`. Go keeps YEAR values as `int64`. The option does not override the OceanBase server version or protocol capability gates. `ParseTime` and `Location` keep their Go semantics; zero dates remain unrepresentable when parsing to `time.Time`. `Interactive` requests the server's interactive timeout policy, not client keep-alive. `ConnectionAttributes` carries application metadata only; do not inject protocol-reserved keys.

MySQL 5 compatibility returns `TIME` at whole-second precision, including when the server sends a nonzero fraction. Select `compatibleMysqlVersion=8` to retain `TIME` fractions. In MySQL 8 compatibility, all-zero temporal fractions are omitted and binary temporal results use the column's declared fractional precision without discarding nonzero digits. These formatting rules apply only to MySQL mode. The scalar acceptance scenarios explicitly select 8 to verify microsecond round trips.

Supply usernames/database names as Go UTF-8 strings without pre-encoding. Both the initial handshake and ChangeUser encode them using `Session.Charset` and reject unrepresentable names. Passwords use the separate `Auth.PasswordCharset`.

To explicitly create the initial MySQL database, set `Config.CreateDatabaseIfMissing=true` or DSN `createDatabaseIfMissing=true` and specify a nonempty Database. It is disabled by default and rejected in Oracle mode. After authentication, the same connection executes IF NOT EXISTS, selects the database, then runs InitSQL. The account needs the corresponding privileges. Initialization failure may leave a created database; the driver does not delete it automatically or retry creation. Replacement pool connections repeat creation/selection. ChangeUser and CancelQuery helper connections do not create databases. See [architecture §6.4](go-driver-architecture-design.md#64-d32-implemented-initial-database-creation).

SOCKS5 uses `Config.DialContext`: create an explicit proxy with `golang.org/x/net/proxy.SOCKS5` and use `proxy.ContextDialer.DialContext`, as in the [compilable example](../socks_example_test.go). Include a port in the proxy address (for example 1080). Preserve the candidate address passed by the driver and use context-aware Dial. The example does not read ALL_PROXY/NO_PROXY or bypass the proxy on failure; the proxy resolves target hostnames. Optional `proxy.Auth` is for the proxy only; do not reuse database Credentials/ProxyUser. Proxy negotiation does not encrypt data; database TLS still verifies the target independently. The default driver connects directly and has no equivalent Java DSN switch. See [architecture §9.0.3](go-driver-architecture-design.md#903-d32-go-socks5-proxy-support) for boundaries and the Go 1.26 dependency baseline.

If the target does not expose the initialization version query, explicitly set `Session.SkipVersionProbe = true` or DSN `skipVersionProbe=true`. The default still probes and never skips automatically on failure. This option omits only initialization `ob_version()`, retaining authentication, actual-mode detection and the required baseline; it does not implicitly enable SOCKS or another proxy. The driver has no server-version override: capability gates use the actual `ob_version()` returned by each physical session. With an unknown version, ordinary SQL/transactions and standard prepared statements remain available; version-gated Oracle OUT, sensitive cursors, native long parameters and XA are rejected, and optimized paths fall back conservatively. Explicit application queries are unaffected. Evaluate these limits before deployment; see [architecture §9.0.4](go-driver-architecture-design.md#904-d32-explicit-version-probe-suppression).

## 3. Queries, parameters and local transactions

Use `QueryRowContext` / `QueryContext` for queries and `ExecContext` for writes. Both modes support `?`; Oracle also supports `:name` with `sql.Named`. Placeholders bind values, not table names, column names or keywords. Validate dynamic identifiers independently using trusted rules; do not concatenate application values into SQL.

`PrepareMode` defaults to server-side preparation. Explicitly select `PrepareClient` when client interpolation is required; parameterized operations still use the same standard-library API. `EmulateUnsupportedPstmts` is opt-in and only falls back from a standalone server `PREPARE` returning error 1295, on both the execution path and an explicit `sql.Stmt`, to that client path; output, stream, combined and other-error cases remain server errors. Close `sql.Stmt` before its owning transaction/connection, and never retain server statement IDs across sessions.

For MySQL top-level multi-statements, `PrepareClient` plus `AllowMultiStatements` may materialize a `LongData` reader once within `MaxBytes`/`MaxLOBBytes` and encode it through the normal client literal path. Single statements, Oracle blocks, and disabled multi-statements keep rejecting client-side streams; the reader is never replayed.

In MySQL mode, ordinary `time.Time` parameters (including `time.Now()`) are converted to `Session.Location` and truncated to microsecond precision in both preparation modes. For example, `.123456789` becomes `.123456`; `.999999999` becomes `.999999` without advancing the second. Applications do not need to truncate timestamps before binding. Years outside 1–9999 after timezone conversion still return `ErrBind`. The destination column can further reduce precision according to server rules. Oracle mode retains nanoseconds for ordinary `time.Time` parameters.

Set `cfg.Session.ParseTime = true` before `NewConnector` to read date/timestamp columns into ordinary Go time fields (DSN `parseTime=true`). The default is false. `ColumnTypes().ScanType()` follows the returned value; database type names stay unchanged.

| SQL type | ParseTime=false | ParseTime=true |
| --- | --- | --- |
| Oracle DATE / TIMESTAMP | Canonical string | time.Time in Session.Location; DATE retains hours/minutes/seconds, TIMESTAMP retains nanoseconds |
| Oracle TIMESTAMP WITH TIME ZONE | Canonical string | time.Time using the value's explicit offset and validated region |
| Oracle TIMESTAMP WITH LOCAL TIME ZONE | Canonical session string | time.Time resolved in actual Session.Location |
| MySQL DATE | Date string | time.Time at midnight in Session.Location |
| MySQL DATETIME / TIMESTAMP | Date/time string | time.Time in Session.Location; TIMESTAMP session conversion is performed by the server |
| MySQL TIME / YEAR | Signed duration string / int64 | Same duration string / int64 |
| Oracle INTERVAL YEAR TO MONTH / DAY TO SECOND | Specialized canonical string | Same string; not time.Time or time.Duration |
| SQL NULL | nil | nil; use sql.NullTime, sql.Null[time.Time] or a time pointer |

For example, with a connector configured above and a non-NULL Oracle TZ column:

```go
var createdAt time.Time
if err := db.QueryRowContext(ctx,
    "SELECT CREATED_AT FROM APP_USER WHERE ID=?", id).Scan(&createdAt); err != nil {
    return err
}
```

Compatibility: with ParseTime=true, Oracle TZ/LTZ now reach custom Scanners as time.Time rather than string. TimestampTZ/TimestampLTZ accept both forms, including through sql.Null[T]. Parsed time preserves the instant, offset and nanoseconds, but cannot retain original fractional scale or every zone token. For exact canonical text, keep ParseTime=false and use the specialized Scanners. Ordinary time.Time input still binds as TIMESTAMP; use TimestampTZ/TimestampLTZ inputs to request those native identities explicitly. Unrepresentable dates and unresolved LTZ daylight-saving gaps/overlaps fail conversion rather than becoming zero time or selecting an offset. Keep Session.Location consistent with the database session timezone.

MySQL TIME precision follows the existing compatibility profile: the default CompatibleMysqlVersion=5 removes fractional seconds from TIME results; explicitly select 8 to retain nonzero fractions. ParseTime does not change this policy or turn negative/>24-hour durations into timestamps.

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil { return err }
defer func() { _ = tx.Rollback() }()
if _, err := tx.ExecContext(ctx, "UPDATE app_table SET v=? WHERE id=?", "new", 1); err != nil {
    return err
}
return tx.Commit()
```

Use `tx` for every operation in the transaction; do not mix in `db` queries to observe uncommitted data. A Commit failure may indicate an unknown outcome. Check `errors.Is(err, oceanbase.ErrCommitUnknown)` first: cancellation/timeouts after sending also retain `context.Canceled` / `context.DeadlineExceeded`. Do not replay the transaction as an ordinary cancellation; Rollback cleanup does not prove that the remote transaction rolled back. The deferred rollback above handles only a transaction that has not ended.

For read-only transactions, `db.BeginTx(ctx, &sql.TxOptions{ReadOnly:true})` sets server transaction properties on the existing physical connection. `Routing.Role` constrains new connection endpoints; a role label does not enforce SQL privileges, and an explicit role failure never falls back to another role. A RoleReadOnly DB must begin transactions with ReadOnly=true; ordinary Query/Exec does not automatically become a read-only transaction. See ExampleNewConnector_readOnlyTransactions in `transaction_example_test.go`; default tests compile this networked example only.

New sessions always start with autocommit enabled. Normally use standard BeginTx/Commit/Rollback. For manual autocommit control, pin a `sql.Conn`, execute all mode-native transaction SQL on it, then release the lease. `db.Exec("SET autocommit=0")` cannot maintain a transaction across calls. Initialization Variables/InitSQL must not leave autocommit disabled or an active transaction. Oracle rejects LevelRepeatableRead and ReadOnly+ReadCommitted. See [architecture §12.3.1](go-driver-architecture-design.md#1231-d32-autocommit-read-only-transactions-and-role-policies) for mode differences and Java mapping.

MySQL `Session.Variables` and `SetSessionVariable` inspect the current variable's result metadata before assigning decimal integer input within int64/uint64 range. Numeric variables receive normalized integer literals; string variables retain quoted values, including leading zeros. Other input and Oracle values are quoted and escaped. For example, `wait_timeout="600"` sends an integer, while `ob_trace_info="000123"` remains a string. A failed type query stops the assignment without retrying it.

`Import` in both MySQL and Oracle modes requires autocommit outside local/manual transactions and XA-used sessions. It accepts a single `LOAD DATA` statement with an optional trailing semicolon; multiple statements, executable comments and ambiguous quoting are refused before SQL/input. It returns `ErrUnsupported` for these unsafe requests because OceanBase LOCAL INFILE can implicitly commit earlier DML. A failed autocommit upload may leave partial effects; rollback cannot provide atomic import. See [controlled import](go-driver-architecture-design.md#1731-d29-implemented-controlled-import).

Use `Number` / `Decimal` or exact strings for money/high-precision numbers; do not convert through `float64`. Use standard `sql.Null*` for nullable values. Oracle `Object` / `Array` use `Null` to distinguish SQL NULL, and a nil collection element represents NULL. Choose date parsing and time zones explicitly for the application; do not assume every date returns `time.Time`.

Oracle `URowID` explicitly binds native type 209; `RowID` retains its existing string/CHARTOROWID behavior. Use `sql.Null[oceanbase.URowID]` or a nil `*oceanbase.URowID` for a typed NULL. `Out{Dest: &id}` infers UROWID from an `URowID` destination; a string destination can use `Type: "UROWID"`. Both reject in MySQL mode. Wire type 209 cannot distinguish ROWID from UROWID results, so result metadata continues to report ROWID. UROWID storage and PL INOUT passed on the Oracle 4.4.2.2 direct target; see G03 in the classified matrix.

Oracle system XMLTYPE results (ownerless or SYS metadata) return strict UTF-8 strings in text and binary queries, with SQL NULL preserved; `DatabaseTypeName` reports XMLTYPE. The driver preserves the serialized text, including server-added whitespace, and does not parse XML or resolve external entities. A user OBJECT named XMLTYPE keeps the ordinary complex-type path. XMLTYPE text/server/client queries and function results passed on the Oracle 4.4.2.2 direct target. To avoid silent XMLTYPE row loss (B14), the driver disables the optional general combined optimization for exactly server version 4.4.2.2 and uses separate PREPARE/EXECUTE. Explicit, automatic and cached queries now return both XML and NULL rows. Mandatory native OUT/RETURNING keeps its original version gate and passed live regression; no SQL is replayed. XMLTYPE PL OUT/INOUT is outside this result adaptation.

Close Rows promptly and check `Rows.Err()` after iteration. For multiple results, loop over `NextResultSet()` as well. Under the standard-library contract, `sql.RawBytes` remains valid only until the next row operation; copy it for longer retention.

## 4. Extensions and resource ownership

Extensions use the same public package. Resource APIs accept only pinned `*sql.Conn` or `*sql.Tx` values as a Session. A `*sql.DB` is not a pinned session and cannot open a cursor or locator.

| Capability | Entry point | Contract |
| --- | --- | --- |
| Batch execution | `ExecBatch(ctx, exec, query, rows, opts)` | exec may be DB/Conn/Tx; no automatic transaction start/commit; pass Tx for atomicity |
| OUT/INOUT | `sql.Out`, `Out` | Dest must be a non-nil pointer; set In:true for INOUT; use Out for explicit type/length |
| Multiple results and counts | `Execute`, `Rows()`, `Snapshot()` | May return a partial Execution with an error; always Close a non-nil object; interpret snapshots after complete consumption |
| Table BLOB | `OpenTableBlob`, `BlobTarget` | Supply the complete primary key; both modes; MySQL writes require the caller's writable Tx |
| Oracle native LOB | `OpenBlob`, `OpenClob` | Query must return one locator; SQL NULL returns nil,nil; never substitute a helper session for the transaction |
| Oracle complex types | `Object`, `Array`, `Attribute` | Use real type names and ordered attributes; descriptions come from the current session; Oracle types are unsupported in MySQL mode |
| Cursors | `OpenCursor`, `Fetch` | Consume and close each batch of Rows before fetching again; Forward/Static/Sensitive depend on target capabilities |
| Metadata | `Metadata` | Returns standard Rows that must be closed; column names/filtering follow architecture and source comments |
| XA | `XAStart`, `XAEnd`, `XAPrepare`, `XACommit`, `XARollback` | Pin one connection for the entire branch; do not mix sql.Tx or bare COMMIT; the application owns the durable coordinator |

`ExecBatch` preserves input order, actual Strategy and each item's Status/Started. BatchFailed does not imply no side effects; BatchUnknown cannot be retried automatically. Explicit BatchRewrite combines independent DML requests in MySQL mode only when multi-statements are allowed and negotiated, execution stops on error, and the tenant merge parameter is confirmed False on every visible observer per chunk through oceanbase.GV$OB_PARAMETERS. Missing views/parameter values fall back; permissions and other query errors propagate. The driver never changes tenant parameters. Explicit BatchArrayBinding requires a known OceanBase >=4.2.1 Oracle writable transaction and ContinueOnError=true; it uses native column-oriented parameters and saved-exception responses, and multi-item successful affected-row counts remain unknown. Other conditions and bulk execution fall back to individual items. See root-package ExampleExecBatch_rewrite and ExampleExecBatch_arrayBinding, and architecture §13.4 for gates and Started=true/NotExecuted semantics. A successful batch does not commit the caller's transaction.

Explicit `BatchMultiValues` combines simple MySQL `INSERT INTO t (a,b) VALUES (?,?)` scalar batches without multi-statement permission. `BatchResult.Chunks` records each SQL chunk's [Start,End) input range and actual aggregate result. Multi-item chunks neither distribute affected counts nor infer generated keys. Per-item results and single-item chunks preserve confirmed full uint64 decimal IDs through InsertID/InsertIDKnown. Unsupported SQL shapes fall back to individual execution; use the returned strategy as the actual result.

Simple MySQL `CALL schema.p(?,?)` supports `sql.Named` binding by declared parameter name for IN/OUT/INOUT. All placeholders must be named; matching is case-insensitive, and literal arguments retain their declaration positions. Ordinary MySQL SQL remains positional; complex CALL expressions/session variables do not support inferred named binding. Parameter metadata is read on the original connection. Older/unknown versions use a safely quoted SHOW CREATE fallback; insufficient privileges propagate as an error without executing CALL.

`MetadataColumns` appends nullable numeric auto_increment/generated flags (1/0/NULL) after existing columns; an unknown Oracle auto-increment flag is NULL. `MetadataTypeInfo` supplies ten-column descriptions of the current mode's built-in SQL types, separate from `MetadataTypes` for Oracle user types. Unknown server/session-dependent limits are NULL; this is not live capability probing. Older MySQL routine-parameter metadata accepts only exact schema/routine patterns; LIKE wildcards are unsupported.

Set query limits with the context passed to each ExecContext/QueryContext. PrepareContext bounds preparation only; Rows reads remain constrained by the query context. A timeout after sending invalidates the physical connection but does not establish that the database stopped or rolled back; do not replay writes automatically. ReadTimeout bounds each underlying read, not the total query duration. Initialization is acknowledged sequentially, and client parameters automatically retain safe separating spaces; Java usePipelineAuth, enableQueryTimeouts and encloseParamInParentheses switches need not be migrated. Affinity address lists use Routing.Endpoints/Weight/Strategy. Ordered is the default; explicitly choose BalanceAffinity for weighted selection.

Execute command results are not disguised as row sets. Use a ResultInfo count only when its corresponding Known flag is true. Oracle anonymous blocks do not synthesize independent counts for each internal DML. Handle read errors, close errors and Snapshot.Err.

The two standard sql.Result methods independently handle availability and int64 range. RowsAffected() may successfully return 0. LastInsertId() returns oceanbase.ErrUnsupported when the server reports no nonzero insert ID; it does not guess primary keys or query sequences. A nonzero ID beyond int64 returns an error. For full unsigned IDs, use Execute's decimal InsertID and check InsertIDKnown. Use explicit RETURNING for Oracle returned columns. An unavailable result field does not make an already-successful write a failure and is not a reason to replay it.

Standard Rows.Columns() / ColumnTypes().Name() return result labels. For original-column/table provenance, select Kind == oceanbase.ResultRows from e.Snapshot().Results and read Columns in projection order. Name is the label; OriginalName is the source column; Table/OriginalTable retain protocol table/original-table names; Schema/Catalog are separate. Empty fields mean the server did not provide them; do not infer them from labels. Snapshots and column slices are independent copies and retain observed provenance after execution closes. RowSetIndex follows standard Rows row-set order; commands/Failure have no Columns. Provenance does not establish updatability or access rights. See [architecture §16.1.2](go-driver-architecture-design.md#1612-d32-result-origins-and-product-identity) for cumulative limits and Java differences.

`MetadataServerInfo` reports OceanBase version and compatibility mode separately. MySQL/Oracle modes are not separate database products; Java metadata switches such as useCompatibleMetadata are not exposed.

Close resources in this order: each batch's Rows → Cursor/Blob/Clob/Execution → commit or roll back Tx → close pinned Conn → close DB at application shutdown. Blob offsets are bytes; Clob offsets are Unicode code points; both are zero-based. End-of-file may return io.EOF. Handle SQL NULL before dereferencing a nil LOB.

LOB/complex values from ordinary QueryContext have different ownership from explicit locators. Configure bounds such as Limits.MaxLOBBytes for materialization; do not read large objects without limits. CursorStatic needs a bounded cache. CursorSensitive requires a simple Oracle base-table SELECT: native cursor positions retain the original membership/order, while each Fetch refreshes columns by hidden ROWID under current isolation. It preserves aliases and ordinal ORDER BY, including repeated read-only columns. A deleted row returns ErrCursor and sql.ErrNoRows, invalidates the cursor and preserves the synchronized session. Joins, DISTINCT, aggregates and derived projections reject. See the compatibility matrix for version gates and the [current live evidence](live-validation.md).

## 5. Runnable examples

The [comprehensive program](../examples/usage/main.go) separates [standard operations](../examples/usage/basic.go) from [resource operations](../examples/usage/resources.go). It depends only on the public root package and standard library, with no new runtime wrapper. The separate [XA program](../examples/xa/main.go) demonstrates XA lifecycle.

The comprehensive example requires:

- A dedicated test database/schema with CREATE/DROP TABLE and PROCEDURE privileges. Oracle also needs CREATE/DROP TYPE and type-description query privileges.
- OB_DSN with an explicit mode=mysql or mode=oracle matching the target. Auto is rejected so that SQL is never selected using an unknown mode.
- Server-side preparation, enforced by the example. The MySQL branch enables AllowMultiStatements for fixed SQL; this is not a reason to enable it globally in an application.
- Random uppercase object names. The program creates tables/procedures/types, writes sample data, checks results and attempts cleanup in reverse dependency order. DDL may commit implicitly and cannot be undone by rollback.

After setting OB_DSN, run from the repository root. Both programs passed against the supplied direct MySQL/Oracle 4.4.2.2 tenants on 2026-10-20:

```sh
go run ./examples/usage
go run ./examples/xa
```

Do not put real passwords into command history; inject them through the application environment or secure configuration. Escape usernames containing @ under the URL rules above. Example errors go to stderr; server errors may contain SQL/values. Do not copy this interactive troubleshooting behavior directly into production logs.

| Example step | MySQL compatibility mode | Oracle compatibility mode |
| --- | --- | --- |
| `basic` | Prepared statements and batch execution in a transaction, then a query after commit | Same public API |
| `call` | CALL with OUT/INOUT | Anonymous block, named binding and OUT/INOUT |
| `results` | UPDATE/SELECT/UPDATE with command and row results | Anonymous block as one server-observable result |
| `tableBlob` | Read/write BLOB by complete primary key in a Tx | Same public API |
| `cursor` | Forward cursor, batch reads and prompt Rows closure | Same public API |
| `nativeBlob` | Not executed; prints the mode limitation | Read a native BLOB locator on a pinned connection |
| `complexTypes` | Not executed; prints the mode limitation | Create OBJECT/ARRAY types, bind/Scan exact Number values, roll back sample data |
| Separate XA program | Start/end/roll back a random branch | Same public API; not a durable 2PC coordinator |

The examples have not run against a database. They do not replace integration tests across modes, direct/OBProxy connections, versions, authentication and protocol combinations. Interruptions, disconnects or process termination may prevent cleanup; errors retain object names. Reconnect to the same test schema, confirm ownership and clean up manually. Do not delete objects of unknown ownership.

## 6. Offline verification and current boundaries

For complete database workflows, use the [standalone acceptance tool](go-driver-test-tool.md): `go run ./cmd/obtest -config docs/obtest.example.json -report obtest-run-001.json`. Using configured DSNs, it creates isolated objects/data, runs CRUD/transaction/procedure/LOB/cursor/XA scenarios and cleans up, independently of go test. The -list and -check entry points do not connect.

```sh
go test ./... -count=1
go vet ./...
golangci-lint run
go test -tags=integration -run '^$' ./...
go test -run '^Example' .
```

The full suite compiles both main examples without running main. Example functions run only when they have an Output comment; the others are compiled only. ExampleNewConnector_proxyUser and ExampleSessionOptions_collation demonstrate configuration without networking. The integration -run '^$' command also compiles only. Live integration tests reside beside their concrete owners; connect, control and batch use `internal/{connect,control,batch}/integration` subpackages, with public smoke/query/pool scenarios at the repository root. Shared profile helpers live in `internal/integrationtest` and open no database during package initialization.

Local performance sampling uses offline protocol peers; see [benchmarks](go-driver-benchmarks.md). Ordinary unit tests check the same operations and results.

Outstanding work includes native VC platform execution; live-service acceptance of Shared Memory and other platform features; behavioral/default differences for all Java settings; full live database benchmarks and long-term resource curves; live database integration; actual Linux execution; remote CI; and race evidence on targets other than the local Windows host (the full suite passed under `-race` there on 2026-11-04 with `CGO_ENABLED=1`; CI still runs zero-cgo without race). D28/D32 and their dependencies retain the complete scope. This guide and its examples add no acceptance conclusions.

The two MySQL profiles for manual TestCreateDatabaseSessions additionally require OB_MYSQL_CREATE_DATABASE_DSN / OBPROXY_MYSQL_CREATE_DATABASE_DSN credentials allowed to create/drop temporary databases, query information_schema.SCHEMATA and read/write their tables. The test generates a random name, verifies absence, creates it, checks new/existing database behavior and InitSQL, closes application sessions and attempts deletion. Insufficient privileges and cleanup failures must be reported. Missing fixtures fail when OB_TEST_REQUIRE=1. This test was compiled only, without a database connection.

The four TestReadTimeoutSessions profiles require OB_MYSQL_READ_TIMEOUT_SQL, OB_ORACLE_READ_TIMEOUT_SQL, OBPROXY_MYSQL_READ_TIMEOUT_SQL and OBPROXY_ORACLE_READ_TIMEOUT_SQL respectively. Supply dedicated read-only SQL that waits at least three seconds before the first response. A one-second read budget checks network timeout, eviction and replacement by a new physical connection; initialization/ordinary Ping must complete within that budget. Missing fixtures fail in require mode. This stage compiled the test only. Closing a connection does not guarantee the server's slow query has stopped; do not supply SQL with application side effects.


2026-11-02 G05a：命名 OBJECT（含嵌套）/collection 的 BLOB 成员可用 `[]byte`/`Raw` 内容，CLOB 成员用严格 UTF-8 `string` 内容输入；受 MaxLOBBytes/MaxMessageBytes 和复杂值节点预算约束。命名 LOB collection 保留目录身份；匿名 LOB collection、NCLOB、locator 回绑继续拒绝。仅实现 PL 读取内容的已知子集，指定目标将 BLOB OBJECT 成员写入持久列的 B06 限制仍单列。2026-11-03 普通/combined、对象/嵌套/collection 的 Oracle 4.4.2.2 直连实库测试通过；代理及其他部署另验。

2026-11-02 G07a：named OBJECT 文本结果现在可扫描为 `Object`，支持 NUMBER、普通/national 字符和递归 named OBJECT；按目录身份、字段顺序、UTF-8、NULL 及有界 parser 解码。业务响应全部结束后才查询目录，兼容 XMLTYPE/普通列混合及后续结果集。RAW/DATE/TIMESTAMP/LOB/cursor/collection 描述符仍拒绝，普通 VARCHAR2 结果仍为 string；不根据全 NULL 属性猜测根对象 NULL。本地回归与走查完成；2026-11-03 Go Oracle 4.4.2.2 直连实库用例通过；代理另验，B07/B08 仍保留。本段更新旧的“所有复杂 text 结果拒绝”边界。

2026-11-02 G07b：设置 `PrepareMode: PrepareClient` 后，命名 `Object`/`Array` 输入可按当前连接的目录描述生成 SQL 构造器；多语句另外启用 `AllowMultiStatements`。字段名称/顺序和可选 Type 必须匹配目录，嵌套值使用确切目录类型名称；支持 NUMBER、普通/national 字符、RAW、DATE/TIMESTAMP 叶子。类型身份双引号转义，内容复用已有字面量编码，受编码字节及复杂值预算限制。匿名类型、LOB/locator/cursor、集合内部 NULL OBJECT 子树及同命令中的 DDL/动态 EXECUTE 均拒绝。默认 server prepare 不变，也没有自动回退或 SQL 重放。2026-11-03 Oracle 4.4.2.2 直连存储、重复命名参数、多语句和 PL collection 实库测试通过；代理另验。
2026-11-02 C02：新增 `EmulateUnsupportedPstmts`/`emulateUnsupportedPstmts`，默认关闭。开启后，独立 server `PREPARE` 返回 1295 且参数可走现有 client 插值时才回退；不覆盖 combined、输出、流参数、其他错误或已执行响应，也不重放业务命令。本地协议回归通过，真实服务端 1295 仍待 N 验证。
2026-11-03 C02 实库：4.4.2.2 MySQL 上 `CREATE/DROP PROCEDURE` 的独立 PREPARE 返回 1295；开启后显式 `sql.Prepare` 回退为 client 插值并真实建/删过程，关闭时保留 1295。带占位符的同名变体为 1064，因此有参数的自动 prepare 回退在本目标不可达；两条回退入口现均有实库证据。
2026-11-02 C08：`PrepareClient` 与 `AllowMultiStatements=true` 的 MySQL 顶层多语句现在可将 `LongData` reader 在 `MaxBytes`/`MaxLOBBytes` 内一次性展开，并复用现有字面量编码；单语句、Oracle PL、未启用多语句及超限输入仍拒绝，reader 不回放。本地协议回归及 `TestMySQLMultiStatementLongData` 在 MySQL 4.4.2.2 直连通过（长文本含 NUL/中文/引号/反斜杠及第二结果）。

```go
// Oracle 中已存在 APP.T，按目录顺序包含 N NUMBER 字段。
value := oceanbase.Object{Schema: "APP", Type: "T", Fields: []oceanbase.Attribute{
    {Name: "N", Value: oceanbase.Number("123.45")},
}}
// db 使用 PrepareClient；此脚本还需 AllowMultiStatements=true。
_, err := db.ExecContext(ctx,
    "INSERT INTO items VALUES (:v); INSERT INTO items VALUES (:v)",
    sql.Named("v", value))
```

2026-11-03 follow-up: MySQL JSON/BIT/YEAR/GEOMETRY, VECTOR(3), ARRAY(INT) and dimensionless SPARSEVECTOR passed the specified server/client, text/parameter and NULL checks. RoaringBitmap function/bytes rebinding, empty/NULL/max-uint64 values passed; direct bytes column binding still returns 1235 (B11). Oracle SDO_GEOMETRY generic Object/Array polygon input and explicitly prepared whole-column reads passed; ownerless metadata falls back to SYS only after local types and synonyms are absent. Ordinary RECORD PL calls/field metadata and ANYDATA PL numeric construction/readback passed; native Go RECORD/ANYDATA parameters remain open. U06 strict charset mappings are now implemented; see below and the [classified matrix](go-driver-completeness.md).

### Additional charsets and Oracle NUL parameters (2026-11-03)

`Session.Charset` also accepts `cp850`, `hp8`, `macroman`, `swe7`, `ujis`, `cp932` and `eucjpms`, with strict encoding and result decoding. Undefined bytes and unrepresentable/lossy Unicode input fail instead of substituting characters. SWE7 replaces ASCII punctuation, including `@` and backslash; the driver therefore rejects a tenant-qualified `user@tenant` identity when SWE7 cannot represent it. Use a UTF8 connection for that identity and SWE7 columns/results. Do not assume SWE7 preserves arbitrary ASCII SQL or identifiers.

For raw bytes already encoded in a target MySQL charset, explicit SQL such as `CONVERT(CAST(? AS BINARY) USING cp850)` establishes binary-to-text conversion. On the tested target, `CONVERT(? USING cp850)` with a native bytes parameter on a UTF8 connection still applies the parameter's UTF8 collation: byte `82` becomes `3F`, although `HEX(?)` returns `82`. The driver preserves bytes and does not rewrite SQL automatically.

Oracle client preparation renders NUL-containing string parameters using collision-free `REPLACE(...,marker,CHR(0))`, avoiding the long VARCHAR concatenation failure of the earlier literal. Encoded SQL and message budgets still apply. Single/double SELECT scripts and independent CLOB storage checks passed at 12003, 98304 and 196608 bytes; larger values and other targets remain unverified. No retry, truncation or automatic CLOB conversion is introduced. See [test evidence](live-validation.md).

Native Oracle TIMESTAMP/NCHAR/NVARCHAR2 collection parameters support `Array`. For direct TIMESTAMP INOUT requiring nine fractional digits, set the named collection `Type`: anonymous element metadata carries no precision and the tested target rounds this call shape to six digits. Complex national input uses `Session.Charset`; `Session.NCharset` still controls national output and ordinary `NString` parameters. See [verified shapes and remaining limits](live-validation.md).
