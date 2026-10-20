# OceanBase Go Driver Compatibility Conditions and Verification Matrix

This matrix separates Java source conditions, Go implementation, and real-environment verification. Java baseline: obconnector-j 2.4.14, commit `6adc311ba5db8b2f19f98f3e6e4734667fdfdda1`. Source conditions are not server-support promises; the MySQL handshake version string is not directly treated as an OceanBase version.

Current live evidence: [2026-10-20 release validation](release-validation-2026-10-20.md) on OceanBase 4.4.2.2 returned **MySQL 35 pass / 0 fail / 2 skip / 8 not applicable** and **Oracle 38 pass / 0 fail / 2 skip / 5 not applicable**, 45 scenarios per mode. Full direct integration tests passed in both modes. The report distinguishes exercised features from target rejections, privileges and missing deployment fixtures. Historical pending entries below are qualified only for the exercised direct combinations; complete multi-target acceptance remains 0/32.

Oracle sensitive-cursor freshness is fixed and verified across connections, with ROWID refresh, aliases, wildcards, repeated columns, ordinal ordering, LOBs and deletion. The earlier [2026-10-19 report](oracle-live-validation-2026-10-19.md) is historical: its JDBC comparison exercised cached rows and did not prove refresh unsupported. The corrected Connector/J comparison and the Go fix are recorded in the current release report. Oracle positive warnings and LOCAL import now pass; import requires autocommit in both modes to prevent implicit commits of caller transactions. NCLOB, nested-table storage and selected child CURSOR forms remain target limits; Oracle view/alternate-identity/proxy and unavailable TLS/compression/OBProxy/OCP/failover deployments were reviewed against the pinned official Java source and local protocol peers. See the release report for exact source paths, intentional Go differences and unverified deployment boundaries. Additional infrastructure/grants are not requirements for this package release; source alignment does not count as a live pass.

## 1. Handshake, Authentication, and Modes

| Capability | Confirmed basis | Current Go behavior | Verification status |
| --- | --- | --- | --- |
| Protocol v10 / 4.1 | [Initial handshake parser](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/ReadInitialHandShakePacket.java#L85), [client response](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L120) | Strict v10 parsing, requires Protocol41/SecureConnection; declares only implemented protocol paths | Synthetic-stream unit tests; actual versions pending integration |
| Oracle compatibility mode | [Status bit 4](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/util/constant/ServerStatus.java#L57), [authentication OK check](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823) | Detect from authentication OK status; close explicit mode mismatches; reject ChangedRows in Oracle; Connector atomically pins mode after successful initialization | Synthetic positive/negative cases; actual versions pending integration |
| CLIENT_SUPPORT_ORACLE | [Client declaration](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L303) | Always declared by client; greeting need not echo it | Synthetic handshakes; actual versions pending integration |
| native / caching SHA2 / SHA256 / clear | [Authentication orchestration](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L720) | Shared gates for initial/switch/multistage authentication; clear requires driver-verified TLS; non-TLS SHA uses only externally pinned RSA keys, never automatic retrieval | Algorithm vectors, peer decryption, switch/rejection paths pass; actual server plugin combinations unconfirmed |
| TLS | [SSL capability bit](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/OceanBaseCapabilityFlag.java#L67) | Explicit TLSConfig enables upgrade after SSLRequest; verify certificates/hostnames without downgrade; custom-Dial security state never authorizes credential sending | Local positive TLS/invalid certificates/hostnames; real certificates pending integration |

Authentication extensions use per-authentication factories in `AuthOptions.Plugins`. `Security` runs before `Respond` obtains credentials and is reevaluated every stage; stages declaring TLS never invoke Respond before verification. Extensions are trusted caller code and must declare security levels correctly/respect context; no raw socket exposure. Credentials callbacks run for every new authentication, never during NewConnector or refresh within transactions.

Handshake charset IDs are implemented for utf8mb4, utf8/utf8mb3, latin1, GBK, GB18030, HKSCS/HKSCS31, GB18030_2022, ASCII, and binary; other explicit client charsets return capability errors. Authentication retains the original server-version text; D05 retains OceanBase's raw version through mode-specific ob_version() queries. Actual versions remain pending integration; D04 connects session initialization.

Standard text and COM_STMT_PREPARE/EXECUTE/CLOSE are implemented using [Java prepare protocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComStmtPrepare.java) metadata/EOF boundaries. MySQL parameter counts follow server metadata; Oracle verifies physical counts after named mapping. Binary NUMBER protocol type 3 reads fixed four-byte integers; 246 preserves exact decimal text rather than guessing byte layouts from Java type names. Local tests cover synthetic peers in both modes, both EOF paths, mixed results, errors, and closure; real targets remain unverified. D12 implements extended scalars; D18 supplies local complex-type protocol support with §9 boundaries; D10 implements client preparation.

D11 implements Execute summaries over standard Rows, per-item counts, and bounded finalization; full uint64 IDs retain text. Snapshot only observes state; Close coordinates standard-library cancellation errors after iteration stops. MySQL MatchedRows/ChangedRows use CLIENT_FOUND_ROWS; Oracle rejects ChangedRows. Synthetic streams pass; real mixed results/no-op UPDATE/upsert remain pending integration.

D32 credential-source/pinned-key supplement: application callback/PEM examples and both-mode initial/switch/ChangeUser peer decryption, source refresh on new connections within one Connector, no failure fallback, and public-key snapshots are added. Missing keys, unsolicited server keys, oversized passwords, and Java DSN parameters explicitly reject. RSA does not prove TLS or subsequent SQL confidentiality; actual plugins/privileges/key sources/rotation remain unverified. See [architecture §17.2.2](go-driver-architecture-design.md#1722-d32-credential-sources-and-pinned-rsa-keys); D32 stays in progress.

### 1.1 D32 Initial Connection Declarations

| Capability | Current Go behavior | Local evidence / real-verification boundary |
| --- | --- | --- |
| Interactive | Config.Interactive/DSN interactive, default false; CLIENT_INTERACTIVE is a client request bit | Exact handshakes pass in both modes with/without the greeting bit; actual interactive_timeout effects pending integration |
| Application connection attributes | Explicit Config.ConnectionAttributes map, snapshots, reserved keys, strict Session.Charset, cumulative limits including length prefixes | UTF-8/GBK/GB18030, length thresholds/contraction/expansion/TLS pass locally; server attribute-table visibility pending integration |
| ChangeUser retransmission | Same physical session/original negotiated capabilities, shared attribute encoder | Both modes × UTF-8/GBK × standard/compressed/OB20 pass locally; actual services/proxies pending integration |
| Default-attribute exclusion | No automatic PID/thread/host collection; nil/empty map retains only internal protocol declarations | No global switch disabling protocol declarations; custom attributes cannot replace useProxyUser authentication adaptation |

Source basis: pinned DefaultOptions, AbstractConnectProtocol.initializeClientCapabilities, SendHandshakeResponsePacket.writeConnectAttributes. See [configuration inventory](go-driver-options-mapping.md) for options/default differences and architecture §6.1 for Go contracts. D32 remains in progress with remaining options/platform/full-delivery scope unchanged.

### 1.2 D32 Collation

| Capability | Current Go behavior | Local evidence / pending verification |
| --- | --- | --- |
| MySQL connectionCollation | Session.Collation/DSN collation; SET NAMES Charset COLLATE Collation, empty keeps defaults | Local configuration/strict identifiers, UTF-8/GBK/utf8mb3, standard/compressed/OB20 initialization recovery/ChangeUser/Full-reset replacement pass; actual sorting/comparison/argument round trips pending integration |
| Failure and encoding | Server validates charset/name pairing; no fallback/driver-encoding changes; failure closes; columns decode by actual IDs | Local unknown/mismatched server errors/resource closure covered; actual supported target list unconfirmed |
| Oracle mode | Explicit ModeOracle rejects during configuration; ModeAuto with actual Oracle returns ErrUnsupported before initialization | Both rejection paths covered locally; no Java silent-ignore behavior or changes to default Oracle flow |

Four-target TestSessionCollation joins manual CI but only compiles. MySQL direct/proxy require real positive verification; Oracle provides this feature's negative contract, not overall compatibility evidence. See architecture §6.2 for defaults/lifecycle.

### 1.3 D32 Proxy Identity

| Capability | Current Go behavior | Local evidence / pending verification |
| --- | --- | --- |
| useProxyUser | Config.ProxyUser / Credentials.ProxyUser / DSN proxyUser; empty disables; no bracketed-username parsing | Static/callback coverage, clearing, encoding/budgets, dedicated attributes/TLS pass locally; actual authorization/target identity pending integration |
| Modes and capabilities | Oracle only; explicit MySQL rejects in preflight, actual MySQL under Auto after authentication/before initialization; reject missing CONNECT_ATTRS | No ordinary-identity fallback and preserved server errors pass locally; old-version/proxy support unconfirmed |
| Lifecycle | ChangeUser explicitly replaces target/empty clears; original pool restores original configuration; CancelQuery compares authenticating user/proxy target | Standard/compressed/OB20, UTF-8/GBK, no KILL for mismatched targets covered locally; actual KILL privileges/proxy paths pending integration |

TestProxyUserSessions joins manual CI but only compiles. Oracle targets use PROXY_DSN, PROXY_AUTH_USER, PROXY_SESSION_USER fixtures under OB_ORACLE / OBPROXY_ORACLE prefixes; DSN must explicitly include proxyUser, and expected usernames must be distinct actual server-returned values. Tests read SYS_CONTEXT CURRENT_USER/PROXY_USER/SESSION_USER for login, ChangeUser retransmission/clearing, and new-pool restoration; missing fixtures fail with OB_TEST_REQUIRE. MySQL targets verify only configuration rejection, not full compatibility. See architecture §6.3.

### 1.4 D32 Initial Database Creation

| Capability | Current Go behavior | Local evidence / pending verification |
| --- | --- | --- |
| Explicit MySQL creation | Config.CreateDatabaseIfMissing/DSN createDatabaseIfMissing, default false; no database during authentication, then create/select with COM_INIT_DB before InitSQL | Handshake bits/names, UTF-8/GBK, backtick escaping, standard/compressed/OB20, close-on-failure/total budget covered locally; actual CREATE privileges/default properties pending integration |
| Lifecycle isolation | New physical connections may repeat IF NOT EXISTS; ChangeUser/cancellation auxiliaries clear the creation option in derived configuration without changing original Connector | Both unintended-creation regressions have failure→success evidence; creation/selection failures never retry or automatically delete possibly created databases |
| Oracle/empty target | Explicit Oracle/empty Database reject during configuration; actual Oracle under Auto rejects before initialization SQL | Negative contracts covered locally; rejection is not positive MySQL acceptance |

TestCreateDatabaseSessions joins manual CI. MySQL direct/proxy require dedicated CREATE/DROP DATABASE fixtures to verify missing/existing databases, InitSQL, and physical replacement; Oracle checks configuration rejection only. Only compilation this phase; no real DDL. See architecture §6.4 for contracts/Java comparison.

### 1.5 D32 Network Read Timeout

| Capability | Current Go behavior | Local evidence / pending verification |
| --- | --- | --- |
| Both-mode read budgets | Config.ReadTimeout / DSN readTimeout; default 0/disabled; each underlying Read uses the earlier deadline/context, writes retain original budgets | Handshake/initialization/TLS/standard/compressed/OB20/response waits/Rows covered locally; real service delay/proxy buffering pending integration |
| Lifecycle/pipeline | Restart each read budget; idle time does not consume the next budget; concurrent deadline updates cannot extend blocked reads; timeout evicts without replay | Context precedence/old deadlines/sequential and pipeline unknown outcomes/network-error classification covered locally; concurrent-update regression failed before passing |

Four-target TestReadTimeoutSessions requires explicit slow read-only SQL fixtures for independent network timeouts/physical replacement; only compiled, not run. It cannot guarantee server queries stopped; use call contexts for complete command deadlines. See architecture §12.5.1.

### 1.6 D32 Native TCP Configuration

Seven Java options (tcpNoDelay, tcpAbortiveClose, localSocketAddress, tcpKeepAlive, tcpRcvBuf, tcpSndBuf, socketFactory) map to existing DialContext/standard net.Dialer/net.TCPConn without duplicate driver configuration. Architecture §9.0.2 defines defaults/error/resource ownership. Compilable ExampleConfig_tcpOptions/TestTCPDialContext* cover local native TCP, source addresses, selected endpoints, protocol I/O, authentication gates, cancellation, close, and Java DSN rejection. Explicit abortive close guarantees neither QUIT delivery nor server SQL rollback; built-in defaults disable it.

This proves only that applications can use standard TCP capabilities through the same driver entry point; remote OS tuning, actual probe/buffer values, network middleware, and database deployment remain unaccepted. D28 VC gaps/Shared Memory real-service acceptance remain unchanged; API mappings do not complete D32.

### 1.7 D32 Native TLS Configuration

Twelve Java TLS options map to Config.TLSConfig, crypto/tls, crypto/x509; see architecture §17.2.1. No store/password/factory fields are added. File-loading examples are verified with local temporary certificates; local TLS peers cover both modes, TLS1.2/1.3, static/dynamic client certificates, extra policy after standard verification, snapshots, and cancellation. Version/suite/callback rejection never falls back to plaintext. Same-name Java DSNs, historical aliases, and weakened-verification switches strictly reject.

Applications/deployments convert JKS/PKCS12/encrypted private keys; no claim that standard Go loaders read them directly. Full TLS-engine replacement is outside tls.Config callbacks. Real database/proxy mutual-identity mapping, platform system trust, and certificate deployment remain unaccepted; CipherSuites cannot restrict TLS1.3 suites. D32/D28 platform gaps remain in progress.

### 1.8 D32 Autocommit and Read-Only Policy

Four Java options map per architecture §12.3.1 to standard sql.TxOptions, pinned sql.Conn, and strict Routing.Role without new driver switches. TestTransactionNative* verifies both-mode read-only commands/original server errors, read/write restoration after transaction end, no reset while manual autocommit is disabled, rollback-before-restoration on pool reuse, rejection of initialization disabling autocommit, and no unknown/opposite-role selection after failures. Oracle LevelRepeatableRead and ReadOnly+ReadCommitted remain explicitly rejected; Java read-only reinterpretation is not copied.

Prearranged peer errors/state prove client commands/lifecycles only, not real DML rejection/read-only snapshot isolation/HA. External standard-API examples only compile, without network runs. Four same-name Java DSNs strictly reject; implementation/acceptance totals remain unchanged.

### 1.9 D32 Result-Column Origins

After complete metadata, ResultInfo.Columns supplies six fields including labels/original columns/tables, strictly decoded by session charset; standard Rows still returns labels. Local tests cover both-mode text/binary, GBK, combined execution, OUT auxiliary-result isolation, multiple results/error prefixes, malformed metadata, independent concurrent snapshots, and cumulative budgets; no new packages/configuration/dependencies. See architecture §16.1.2 for useOldAliasMetadataBehavior/blankTableNameMeta/useCompatibleMetadata adaptations; express product identity separately from compatibility mode.

TestMetadataCatalog actual-origin source is added but only compiled; missing origins may be empty. Local peers do not establish actual fields/case/view/proxy behavior. D32 remains in progress, with 28/32 implemented and 0/32 accepted unchanged.

### 1.10 D32 SOCKS5

Existing DialContext integrates directly with x/net/proxy.ContextDialer, retaining standard pooling/TLS/authentication gates. Actual local TCP SOCKS peers cover both modes/three streams, remote domains, anonymous/username-password authentication, rejection/malformed responses, cancellation/total budget/close, candidate switching, TLS target identity, and zero database credentials to unverified targets. Examples only compile; ordinary tests use no external proxies/databases. Proxy authentication provides neither encryption nor database identity; see architecture §9.0.3.

Actual proxy DNS/identity/network policies/load/database combinations remain unaccepted. Both Java options are mapped; see §1.11 for connectProxy initialization policy and §19 for local Shared Memory/VC implementations/platform boundaries. Totals remain 28/32 implemented, 0/32 accepted.

### 1.11 D32 Version-Probe Policy

Session.SkipVersionProbe (DSN skipVersionProbe) defaults false and explicitly skips initialization ob_version(), retaining actual mode, charset/autocommit/timezone, and custom initialization. This maps only the probe actually present in Go corresponding to Java connectProxy, not skipped authentication/arbitrary initialization errors; see architecture §9.0.4.

TestVersionProbe* / TestDSNSkipVersionProbe cover both modes/three streams, snapshots/pool reuse, ChangeUser, cancellation auxiliaries, no retries after default-probe/required-baseline failures, and preserved custom initialization. Unknown-version standard-preparation fallback, disabled SQL checksums, and sensitive-cursor/Oracle-OUT/long-parameter/XA rejection have local evidence. Explicit queries are neither suppressed nor used to upgrade internal version knowledge. Four-target TestVersionProbeSessions covers connection/read-only transactions/queries/rollback/capability gates and only compiles this phase. Actual proxy baseline SQL/version combinations remain unverified; totals stay 28/32 implemented, 0/32 accepted.

### 1.12 D32 Standard Connection Pool

See [architecture §5.1](go-driver-architecture-design.md#51-d32-standard-connection-pool-and-lifecycle) for semantics/default differences of pool/poolName/maxPoolSize/minPoolSize/maxIdleTime/poolValidMinDelay/staticGlobal/registerJmxPool. Go uses sql.DB setters, business contexts, external slog, and Stats without a second driver pool/JMX. No standard-library minimum equivalent to minPoolSize exists; examples perform one-time multi-session readiness checks. staticGlobal server-global caching does not become a boolean: every new session runs necessary baselines.

TestPool* uses actual database/sql/driver/local peers for both-mode lazy connections, limit waits/cancellation, initialization/reuse/QUIT, warmup failure/PING-rejection cleanup, lifetime/idle eviction, default/enabled reuse validation, independent Auto-mode pools, and Java DSN rejection. Examples only compile; real idle timeouts/capacities/long-term resources/monitoring/platform runs remain unaccepted. D32 remains in progress: 28/32 implemented, 0/32 accepted.

### 1.13 D32 Initialization, Argument Boundaries, and Query Deadlines

| Java option | Current Go equivalent | Local evidence / limitations |
| --- | --- | --- |
| usePipelineAuth | Sequential initialization after final authentication OK; independent ConnectTimeout/context/ReadTimeout; no same-name switch | TestInitializationErrorBoundary covers both modes/three streams, close after stage rejection, no later commands/retries/pool entry; TestVersionProbeInitialization covers successful restoration. Pinned Java also calls getResult individually; no pipeline-performance claim |
| encloseParamInParentheses | Fixed spaces around client arguments; standard preparation remains binary binding | TestClientParameterBoundaries uses public APIs in both modes/three streams for negative numbers/numeric aliases/repeated names/quoted-semicolon strings. Current pinned Java only prepends a space; real SQL interpretation pending integration |
| enableQueryTimeouts | Per-call contexts; no global disable switch/SQL changes/implicit KILL | TestStatementContextDeadline covers both modes/three streams/both preparations, connection availability before/after expiry, deadlines earlier than ReadTimeout, no replay; existing Rows/transaction tests reused. No guarantee of server stop/rollback |
| serverAffinityOrder | Endpoints replaces Addr; Strategy/Weight/Group explicitly express ordering | Pinned Java lists also follow strategy, so Go does not map only to Ordered. Existing TestFailoverBudget/routing/selector tests cover candidate replacement/order/snapshots/bounded failover. TestAddressAffinityWeights adds weight preference/no-duplicate checks, not probability/throughput acceptance; real HA pending integration |

See architecture §9.0.4, §10.2, §12.5, §17.1.1 for pinned source/default differences. All four same-name Java DSNs reject; local evidence does not fabricate server/platform acceptance. Totals stay 28/32 implemented, 0/32 accepted. FuzzStmtArray joins Go1.26 CI's 30-second fuzz target/386 boundary checks; remote CI has not run.

## 2. Further Advanced Protocol Conditions

| Capability | Pinned Java conditions | Go owning task/status |
| --- | --- | --- |
| INFORMATION_SCHEMA.PARAMETERS | [OceanBase version >= 4.1](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1774) | D13 implemented; known >=4.1 uses parameter dictionaries, unknown/older versions use SHOW CREATE fallback for exact schema/routine requests; real dictionaries unverified |
| PrepareExecute | [Oracle mode + useOraclePrepareExecute + OceanBase >= 2.2.76](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778) | D14 required OUT/INOUT/RETURNING path implemented with field flags/trailing responses; D24 explicit optional general strategy implemented, see §15; actual versions pending integration |
| Offset fetch | [Oracle mode + server preparation + useCursorOffset + OceanBase >= 2.2.5](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1783) | D19 offset/trailing OK implemented for known Oracle >=2.2.5; old/unknown versions use forward fetch; actual versions pending integration |
| LOB locator V2 / array binding / OB20 / reset | Declare LOB 1<<29 and locator-v2 mask 1 after CONNECT_ATTRS negotiation; D23 new-metadata mask 2 negotiates separately; establish other capabilities independently without extrapolating versions | D16 negotiation/parsing implemented, target versions unverified; D09 Full evicts old sessions, native reset unimplemented; D23 OB20 locally implemented, see §14; D22 native array binding locally implemented under §13 version/transaction/argument gates |

## 3. Real Targets

| Target | Mode / access | Exact OceanBase / OBProxy versions | Current status |
| --- | --- | --- | --- |
| P-MD | Direct MySQL compatibility mode | Unconfirmed / inapplicable | Integration deferred, unverified |
| P-OD | Direct Oracle compatibility mode | Unconfirmed / inapplicable | Integration deferred, unverified |
| P-MP | MySQL compatibility mode via OBProxy | Unconfirmed / unconfirmed | Integration deferred, unverified |
| P-OP | Oracle compatibility mode via OBProxy | Unconfirmed / unconfirmed | Integration deferred, unverified |

Before future real-environment runs, pin target versions, authentication plugins, transports, and declared positive/rejection paths. [Development progress](go-driver-development-progress.md) V01–V14 governs full capability acceptance/evidence. Local simulation cannot replace real compatibility acceptance.

## 4. Scalars, Character Sets, and Time (D12)

| Capability | Go behavior | Verification status |
| --- | --- | --- |
| BIGINT UNSIGNED result metadata | ScanType uint64 covers full unsigned range; signed BIGINT stays int64; row values remain int64/exact strings | TestUnsignedBigIntScanType uses actual database/sql and reflected ScanType targets across three execution paths/streams for 0, MaxInt64, MaxInt64+1, MaxUint64, signed controls, NULL; TestAllScalarTypes integration only compiles |
| Exact NUMBER / DECIMAL | Number retains text; Oracle protocol 3 uses four-byte integers, 206/246 exact decimal text; inputs use 246, never float64 | Both-mode boundaries/three execution paths pass locally; actual precision pending integration |
| NCHAR/NVARCHAR2, RAW, ROWID | NString/Raw/RowID retain input identity; NCHAR decodes with NCharset, RAW returns copied bytes, ROWID text | Native field bodies/NULL/empty/parameter identity pass locally; actual server empty-value semantics pending integration |
| Oracle temporal/interval types | Types 200/201/202/204/205 use native Java layouts; both row protocols retain nanoseconds/offsets/regions, distinguishing LTZ session wall time from instants | Native fixtures, DST, zero times, year 1 CE, invalid fields, local end-to-end pass |
| parseTime / Location | Text by default; explicit session timezone parses timezone-free dates/timestamps when enabled; reject zero dates/DST ambiguity; TIME/YEAR semantics unchanged | Initialization order/DSN/column ScanType/positive and negative cases pass locally; named server timezones require target support |
| UTF8MB4 / UTF8MB3 | UTF8MB3 rejects supplementary characters; both strictly validate UTF-8 | SQL/arguments/column names/results/fuzzing pass |
| latin1 / ASCII / binary | latin1 uses MySQL Windows-1252 and rejects undefined bytes; ASCII rejects non-ASCII; binary fields preserve bytes | Positive/negative cases/fuzzing pass |
| GBK / GB18030 | Pinned x/text mappings; reject unrepresentable/invalid encodings, accept decodable legacy byte aliases | Known vectors/SQL/arguments/column names/results/fuzzing pass |
| UTF16 | Big endian/no BOM, strict surrogate validation; NCHAR/results only, explicitly rejected as SQL client charset | BMP/supplementary characters/invalid surrogates pass locally |
| HKSCS / HKSCS31 | OceanBase IDs 152/153; UTF-8 payloads per Java contract, preserving SET NAMES names | Local configuration/authentication/SQL/argument/result peers pass |
| GB18030_2022 | Separate 2022 mapping/default ID 217; old GB18030 unchanged; unknown collations still reject | Independent full-BMP OpenJDK digest/vectors, strict decoding/protocol peers pass; actual targets unaccepted |

Existing collations resolve using pinned mappings; do not guess charsets for new unevidenced IDs. `TestAllScalarTypes` provides four-target real-test source/explicit CI for text/server/client paths, compiled only; no database connections this phase. Local peers verify conversions/byte layouts, not target SQL/timezone-table/charset compatibility.

D13 completes standard RowsColumnType length/precision interfaces/unified Metadata: separate MySQL information_schema/Oracle ALL_*; empty Oracle catalog results when absent; ErrUnsupported for MySQL UDT/synonym dictionaries; preserve privilege error codes. StatementParameters always server-prepares/closes, retaining logical/physical mapping/raw protocol direction codes. Public/internal dictionary execution share plans; internal execution retains the original gate/physical connection at full-response boundaries only. Local-peer verification does not establish real dictionary fields/OBProxy compatibility.

MySQL BINARY/VARBINARY result/parameter type names now use charset 63 without misreporting text binary collations; Oracle VARCHAR2/CHAR names unchanged. Both modes/three streams/three local result paths, bytes/empty/NULL/metadata covered; TestAllScalarTypes real-column comparisons only compile. See architecture §16.1.1.

## 5. Scalar OUT, INOUT, RETURNING (D14)

| Capability | Go behavior | Verification status |
| --- | --- | --- |
| Scalar OUT / INOUT | sql.Out or Out; nil/unsupported destinations reject before sending; nullable/full unsigned/extended scalars retain input identity | Both-mode protocol peers/prepared reuse pass; actual routines pending integration |
| Oracle outputs/RETURNING | Actual ob_version >=2.2.76; COM_STMT_PREPARE_EXECUTE; RETURNING flags 1/3 and PL OUT accepted; trailing OK/ERR always read; publish after temporary statement closes | Encoding, EOF/deprecated-EOF, errors/tail boundaries pass locally; SQL/OBProxy behavior unverified |
| MySQL outputs | Random private variables on the same session; initialize/main command/output SELECT/clear; auxiliary results excluded from summaries; cleanup failure evicts | Main-command/SELECT/cleanup faults, type sizes/connection reuse pass locally; real SQL pending integration |
| Output publication | Ordinary results stream; write back after final response/cleanup and cancellation check; stage built-in conversions, report custom Scanner partial side effects through OutError.Partial | Overflow/NULL/duplicate conflicts/unexpected row counts/close failure/cancellation during finalization regressions pass |

`TestScalarCall` / `TestReturning` join manual integration entry points and only compile. All four real targets remain unverified; LOB and REF CURSOR/handle outputs are implemented under D16/D20 respectively.

## 6. Pinned Resource Lifecycles (D15)

Internal resource records, physical/session/transaction generations, parent-statement leases, and private close dispatch through standard Session are implemented. No public ResourceManager or driverConn retained from Raw. Close does not end caller transactions; invalid generations return ErrResource, though the standard library may first report ended Conn/Tx. CleanupTimeout bounds transaction/reset cleanup; pre-dispatch cancellation permits close retries. Synthetic peers verify identity/leases/unpublished records/cancellation/timeouts/pool reuse. D16 adds concrete temporary-LOB release; cursor protocol belongs to D19/D20. No real-server resource-acceptance conclusion exists.

## 7. Native LOBs (D16)

| Capability | Go behavior | Verification status |
| --- | --- | --- |
| BLOB/CLOB/NCLOB | Oracle OpenBlob/OpenClob on pinned Conn/Tx only; NULL returns nil, empty retains valid handles | V1/V2 locators, inline/out-of-line, NULL/empty, invalidation/cardinality-failure cleanup pass locally |
| Native chunk operations | Actual ob_version >=2.2.76; same-session DBMS_LOB; random byte reads/writes, character reads/writes/length, shrinking/temporary release | Short reads, NUMBER counts, prefix-write failure, locator updates, CLOB/NCLOB offsets pass locally; actual SQL/versions pending integration |
| Ordinary queries / scalar OUT | Bounded materialized content; internal commands follow complete main response/tail; cumulative remaining responses bounded | QueryContext/Rows.NextResultSet/Oracle hidden OUT/multiple results/transaction contexts pass locally; real original-transaction visibility pending integration |
| Large objects / Reader/Writer | Per-chunk limits separate from ordinary 16 MiB materialization; adapters use explicit contexts | Explicit Reader >16 MiB and independent reads after open-context completion pass locally |
| Character boundaries | Unicode-code-point API/UTF16 offset conversion; overwriting with changed existing surrogate width returns ErrUnsupported before writes | BMP/supplementary/equal-width overwrite/truncation/unequal-width rejection pass locally; actual NCLOB units unverified |
| Resource release | V2 temporary locators release server objects; V1 uses ISTEMPORARY; persistent objects release local records; no implicit transaction end | Idempotent Close/multiple temporary-object cleanup after cardinality failures/transaction-reset generations covered; actual resources/locks pending integration |
| MySQL mode | Native OpenBlob/OpenClob return ErrUnsupported; OpenTableBlob table chunks implemented in D17 | Native rejection verified; BLOB content never interpreted as locators |

`TestNativeLOB` source covers original-transaction uncommitted I/O, ordinary materialization, NCLOB, cancellation, temporary release, reuse after transaction end/Full reset; only compiled this phase. No database/Linux/remote-CI/successful-race execution evidence.

## 8. Table BLOBs (D17)

| Capability | Go behavior | Verification status |
| --- | --- | --- |
| Explicit target | Standard Conn/Tx, resolved/pinned schema, base table/non-generated BLOB column/full primary key; copy Key/byte values, evaluate custom Valuer once | Composite keys/special identifiers/NULL/missing/empty rows/invalid targets/arguments pass locally |
| MySQL reads | OCTET_LENGTH/SUBSTRING, bounded chunks; content may exceed materialization limit; Conn promises no cross-chunk snapshot | database/sql peers cover cross-chunk reads, keyed length rereads, cancellation/invalidation; actual SQL pending integration |
| MySQL mutation | Caller writable Tx, ENGINE=InnoDB; FOR UPDATE checks row/non-NULL/length each time; no sparse expansion; ChangedRows=0 may succeed | Append/overwrite/shrink/empty writes, read-only/unknown-engine rejection, missing/NULL rows/lock errors pass locally; real locking/isolation pending integration |
| Capacity/failures | Precheck four BLOB capacities before first UPDATE; warning UPDATE returns ErrLOB; failed chunks excluded from completed prefix, caller rolls back | Capacity/warning-OK/disconnect/cancellation regressions pass; real non-strict mode/max_allowed_packet unverified |
| Oracle backend | Same target queries reuse native Blob; writable Tx opens with FOR UPDATE, reads without locks | Native capture, CHAR/DATE/NUMBER keys, transaction-lock SQL pass locally; actual locators/privileges pending integration |

Four-target `TestTableBlob` source/manual CI added, compiled only this phase. No database connections; local protocol tests do not count as real SQL/lock/transaction acceptance.

## 9. OBJECT/ARRAY and Descriptions (D18)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Values/scanning | Root Object/Array/Attribute, format=1 JSON; exact NUMBER/base64/wall time/nesting/NULL/empty collections; failed Scan preserves original | Lossless/boundary/depth/fuzz verified locally; API in architecture §11.2.2 |
| Native arguments | COMPLEX160; dictionary field plans; separate object/anonymous-collection element headers | database/sql server preparation, named/anonymous arrays, complex OUT peers pass; real targets unverified |
| Descriptions/cache | Same physical session/one command, isolated by mode/schema/name/protocol version; entities precede private/PUBLIC synonyms; mismatches refresh descriptions/reparse buffered bytes only | Internal queries after complete Query/Next responses, version changes/synonyms/cache budgets pass locally; DDL/privilege/version integration incomplete |
| Nested LOBs / result cleanup | Materialize after full remaining response under shared root-value budgets; same path for ordinary/later complex row sets/hidden OUT; final decode on close inherits cleanup context | Nested BLOB/CLOB/total limits/OUT column mismatch/tail errors/Close-Exec drain/cleanup timeout regressions pass |
| Current rejection boundaries | MySQL native complex input, text complex results/client interpolation; non-NULL TIMESTAMP/LOB field inputs; built-in NCHAR/NVARCHAR2/TIMESTAMP/LOB collection-element headers | Explicit errors, invalid headers reject before sending including empty/all-NULL collections; awaiting server write-protocol evidence |

Protocol basis from the same Java baseline: [object parameters](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBStructParameter.java), [collection parameters](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBArrayParameter.java), [binary-row parsing](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/BinaryRowProtocol.java). Java text complex reading has no usable representation; Go explicitly rejects it. Current implementation proves neither all versions nor every field type compatible.

`TestComplexTypes` only compiles under integration tags without database connections. Four-target objects/collections/nested LOBs/transactions/complex OUT, synonym privileges/actual versions remain pending integration, without acceptance credit.

## 10. Cursors, Scrolling, and Updatable Results (D19)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Forward batches | Pinned Conn/Tx; COM_STMT_EXECUTE flag1, COM_STMT_FETCH; complete batches return standard sql.Rows; active batch returns ErrCursorBusy | Both-mode native packets/early close/independent contexts/empty cursors/failure invalidation pass locally; actual servers pending integration |
| Static | Native fetch builds bounded snapshots with absolute/relative positions; 0 precedes first row, negative/overflow reject; mutation leaves original snapshot unchanged | Scrolling/budget/boundary tests/position fuzzing; actual large results unverified |
| Sensitive | Known Oracle >=2.2.76, command161 + scrollable mode8 + absolute-offset fetch; simple single-base-table projection, hidden ROWID and per-row requery; original membership/order with current-isolation refresh | Cross-session updates, LOBs, aliases/wildcards/repeated columns, ordinal ordering and deletion pass on direct Oracle 4.4.2.2; epoch invalidation/session reuse covered on three protocol streams; other combinations explicitly reject |
| Updates/refresh | Writable Tx, single base table/full projected PK; MySQL InnoDB; safe identifiers/native bound DML; Update/Delete first FOR UPDATE in same transaction; Refresh reads base projection by PK | Both-mode SQL/binding/no-op0/missing rows/warnings/NULL PK/prepare-and-lock generation rechecks pass; actual locks/privileges/concurrency pending integration |
| Batch values/lifetimes | Decode complex values/materialize LOBs under original gate after complete response; MaxMessageBytes/MaxLOBBytes bound batches; Close/transaction/reset release parent-statement leases | Oracle LOB fetch/both EOF paths/old 9-byte/new 19-byte protocols/transaction-reset lifetimes pass locally |

See architecture §15 for public contracts/limits. No ROWID-only updatable backend exists; unreliable full PK rejects. Refresh requires Updatable and returns current base rows without rerunning original filters. Callers roll back mutation warnings/unknown outcomes; missing rows allow safe cursor closure while retaining transactions. Direct/proxy both-mode `TestCursorModes`, `TestCursorUpdates` source/manual CI added, compiled only this phase; no database connections.

## 11. REF CURSOR and Streaming Outputs (D20)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Explicit outputs | Execute + CursorOut / Out to `**Blob`/`**Clob`; known Oracle >=2.2.76; initially nil/non-INOUT | Positive/NULL/multiple-mixed resources/ordinary-entry/old-version/mode rejection pass locally; actual targets unverified |
| Protocol/leases | CURSOR163 fixed LE4 ID; separate nine-byte FETCH/COM_STMT_CLOSE; multiple resources share parent-statement retention | Peers check commands; final-resource parent release, reading/new contexts after Execution.Close, transaction invalidation pass |
| Publication/failures | Publish resources together after complete responses/all built-in-Scanner conversions; recheck context/generations; clean failures | Tail errors/corrupt responses/Scanner errors/cancellation during conversion/no output leaks pass locally |
| Ordinary child Rows | Binary REF CURSOR columns cache after full parent response under shared message budget; standard parent-child lifetimes, depth 16/cycle gates | Original session/transactions/nesting/cancellation/metadata budgets pass locally; static-child cross-batch accounting/independent lookback pass |

Source basis from the same Java baseline: [type IDs](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java), [REF CURSOR](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/RefCursor.java), [binary reading](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/BinaryRowProtocol.java), [release commands](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java). `TestProcedureResources`/manual CI added, compiled only this phase without database connections; actual procedure syntax/temporary LOBs/versions/privileges/OBProxy behavior remain unverified.

## 12. Long Parameters and Chunked Uploads (D21)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Single-use input | *LongData; borrowed Reader, exact Length/-1, MaxBytes; one upload per object, named repeated positions receive each chunk | Empty/length/boundary/limits/no whole-stream buffering/n+err/no-rewind peer tests |
| Both-mode protocol | MySQL24 without per-packet response; Oracle162 per-packet OK, binary hex/text session encoding, STRING254; execute omits streamed bodies | Native packets/source checked; known Oracle >=2.2.76, old/unknown reject; four targets unverified |
| Text/entry points | Incremental strict UTF-8 conversion; ordinary preparation/explicit cursors/Oracle OUT/MySQL OUT upload on the same session | GB18030/UTF-8 across chunks, OUT publication, cursors/repeated names covered locally; actual NCLOB conversion pending integration |
| Failures/cancellation | Driver never closes borrowed Readers; upload failure/cancellation evicts, including cancellation before execute after upload; no automatic replay | Partial writes/server chunk errors-corruption/Reader errors/cancellation regressions; blocked Readers require caller cooperation |

See architecture §13.5. Ordinary PrepareClient rejects streams; OUT retains necessary native protocols. `TestLongParameters`/four-target manual CI added, compiled only this phase without database connections; max_allowed_packet, empty-stream storage, real LOB/transactions/OBProxy remain pending integration.

## 13. Batch Execution and Strategies (D22)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Public interface | ExecBatch; DB pins Conn for entire batch, Conn/Tx use one private dispatch without creating/ending transactions | Both-mode/autocommit partial-success/original-transaction rollback peers verified; actual targets unverified |
| States/counts | Input order, NotExecuted/Succeeded/Failed/Unknown, Started, known/unknown row counts; failure promises no zero-side-effect guarantee | Intermediate failures/later items/uint64 overflow/cleanup errors after confirmed success pass locally |
| multi-send | Auto/MultiSend + ContinueOnError + server prepare + simple DML/scalars; concurrent command writes/ordered reads, chunks by rows/physical slots/total bytes | Writes ahead/bidirectional backpressure/16MiB continuation/errors/transactions pass; server/proxy buffering pending integration |
| Conservative fallback | Default stop-on-error, OUT, single-use long parameters, complex values/custom Valuer use sequential execution with existing protocol/output rules | Single-consumption streams/scalar OUT/all six strategy preferences pass locally; handle outputs remain Execute-only |
| Rewrite/bulk/array binding | Restricted MySQL BatchRewrite: explicit multi-statement permission, stop-on-error, confirm combined optimization disabled per chunk. Native Oracle BatchArrayBinding: known >=4.2.1, writable transaction, explicit continuation/scalar gates. Explicit BatchMultiValues: simple MySQL placeholder INSERTs, Chunks reports aggregates. Others/bulk sequential; architecture §13.4 | Rewrite/native array/rejection/fallback verified locally; four-target TestBatchRewriteExecution / TestBatchArrayBindingExecution only compile; actual semantics/performance pending acceptance |
| Uncertainty/cancellation | No replay; retain confirmed items, Unknown only for possibly sent/unconfirmed items; evict corrupt connections/wait for writer; retain SQL/context/cleanup error chains | Single dial/partial sends/corrupt-extra results/Tx-operation cancellation/cancellation after final response pass locally |

See architecture §13.4 for interfaces/defaults/Java evidence. `TestBatchExecution`/four-target manual CI added, compiled only this phase without database connections or acceptance credit.

## 14. Packet Streams, Checksums, and New Metadata (D23)

| Capability | Current Go behavior | Verification status |
| --- | --- | --- |
| Negotiation/policy | Six independent ProtocolOptions tristates including D25 FLT; pin stream after standard authentication OK; reject missing required/enabled off | Both modes/three streams/invalid combinations/capability echoes pass locally; actual versions unverified |
| Compression | Default off; explicit auto negotiates CLIENT_COMPRESS with fallback; Java >100-byte / <90% selection, seven-byte zlib/raw framing, per-message budgets | Local boundary/sequence/expansion guards pass. Official 2.4.14 codec: 8 Go messages decoded; 5 Java messages decoded, 3 known malformed Java outputs rejected. [Evidence](release-validation-2026-10-20.md#compression-alignment-follow-up); deployed compression unverified |
| OB20 | 7+24-byte header/four-byte tail; identity/sequence/flags/CRC checks; skip new TLV/old extra within boundaries | Independent CRC vectors/corruption-length/ID wraparound/partial writes/cancellation/unread reuse rejection pass locally |
| Two checksum classes | Checksum means OB20 CRC; ProxyChecksum means Java useObChecksum capability bits 3/6, separately requested/confirmed | Combined policies verified locally; actual proxy-backend checksums not proven locally |
| New metadata | Reuse column layouts; Oracle national-character Length directly, RAW/ROWID Precision, unknown LOB lengths | Decode flags/length tests pass locally; actual column descriptions unverified |
| Batches/errors | Framed multi-send falls back sequentially before sending; protocol errors never retry business operations, preserve actual network byte counts | Both modes/two framing classes, batch preparation/cleanup/cancellation/corruption tests pass |

See architecture §9.1 for complete policies/bits/source. Four-target `TestPacketModes` source/CI added, compiled only. Standard/auto/compression are registered positive on all four targets; OB20/OB20 CRC/proxy checksum positive on both proxies; new metadata positive on both Oracle targets. Other targets may record explicit capability rejection. Pin supporting server/proxy versions before execution; universal rejection cannot establish compatibility. Every real target remains unverified; FLT business fields belong to D25.

## 15. D24 Execution-Strategy Conditions and Verification Boundaries

| Capability | Pre-send conditions | Current evidence |
| --- | --- | --- |
| General PrepareExecute | Oracle + Execution.PrepareExecute + actual ob_version >=2.2.76; unknown/older use standard paths | Combined queries/explicit-statement DML/repeated IDs-types/no replay on errors/Blob-Clob capture pass locally; actual servers pending integration |
| Standard SQL checksum | Oracle + Execution.StatementChecksum + actual ob_version >=4.1.2 | Threshold gates/independent CRC vectors/standard-packet fields pass locally; batches/cursors share gates |
| Combined SQL checksum | Selected Oracle combined path + Execution.StatementChecksum | Tail fields verified locally; iteration unchanged, distinct from OB20 CRC |
| Type-header reuse | Execution.ReuseParameterTypes + last fully successful execution on same statement + matching types/descriptions | NULL/unsigned/complex-description changes/resend after SQL failure verified locally; retain type metadata only |
| Bounded cache | StatementCacheSize >0, SQL UTF-8 bytes <=StatementCacheSQLLimit, cacheable ordinary SQL | Both-mode LRU/error eviction/SQL lengths/session-SQL invalidation/Full-reset physical eviction pass locally; explicit statements/procedures/connection resources follow architecture §10.1.1 |

`TestExecuteStrategies` defines ordinary/optimized configurations for mysql/oracle/proxy-mysql/proxy-oracle, comparing values, NULL/type changes, OUT, duplicate-key errors, counts, and resource closure. Oracle optimized targets require actual >=4.1.2 to cover both gates; MySQL retains standard commands/caching/type-header reuse. Integration source only compiles; no database integration or server-throughput conclusion.

## 16. D25 Diagnostics and Full Link Trace

| Capability | Local implementation/evidence | Real-acceptance boundary |
| --- | --- | --- |
| Injected logging/metadata callbacks | Silent nil, Debug/Warn, redaction, unlocked callback/handler reentry, request/message limits/drop counts | No raw SQL/packet collection; callers manage handler concurrency/pool reentry |
| FLT negotiation/extra | off/auto/required, OB20/FLT capability combinations, new TLV/old varchar, CRC/16MiB fragmentation/malformed fields | Target versions/proxy configuration unconfirmed; direct off/proxy required registered positive |
| Request isolation/control | Clear app each command, 16-byte IDs, server sampling/printing/slow requests/ShowTrace, clear query time; works without logger | TestFullLinkTrace only compiles; compare server app clearing/trace-span IDs; client events do not prove server tracing |
| Boundaries/attribution | Rows includes consumption time; each fetch/long-data piece/LOB SQL/batch item is independent, multi-send aggregates actual I/O; malformed OK records failure | Total connection duration is a local event; final completed span may have no later command to upload; no extra SQL/replay |

See architecture §18.3.1 for detailed APIs/limits/deferred-upload policy. Database/OBProxy/Linux/remote CI/successful race remain unverified and unaccepted.

## 17. D26 Static Selection, Roles, and Connection Failover

| Capability | Local implementation/evidence | Real-acceptance boundary |
| --- | --- | --- |
| Addresses/strategies | DNS/IPv4/IPv6/port parsing, group priority/within-group ordered-random-roundrobin-weighted affinity; Connector snapshots/concurrent health isolation | Actual same-tenant endpoints/logical roles/network conditions unverified; TNS/OCP/topology refresh belong to D27 |
| Budgets/cooldown | Per-attempt dialing through initialization; all attempts/fixed backoff-cooldown waits share total budget; attempt/cancellation/expiry/independent-Connector tests | Defaults/constraints in architecture §17.1.1; custom Dial/credential callbacks must respect context |
| Rejection/security | No retries for auth/mode/malformed handshake/initialization; TLS uses selected host; TLS1.2/1.3 server certificate rejection alerts are not transport errors | Only received recognizable protocol rejection can be classified; unknown disconnects treated as transport faults; actual certificate/proxy conditions pending integration |
| Active sessions | RoleReadOnly rejects write transactions, pinned roles never downgrade; no replay after lost business responses or socket switching in transactions; optional idle-PING failure evicts | Server owns ordinary write permissions, no automatic SELECT routing; caller guarantees actual role labels |

Four-target TestConnectionFailover source/manual CI added; each profile DSN addresses must contain at least two same-tenant/mode endpoints. Test injects failure on first dial, then actually connects to a fallback, verifies no redial during active read-only transactions, and selects the recovered endpoint in a new session after close. It does not simulate actual server outages or establish database-side unknown outcomes. Only compiled this phase, no database connections; real HA/Linux/remote CI/race not passed.

## 18. D27 Discovery Sources and Connector Lifecycle

| Adapter | Local implementation/evidence | Real-acceptance boundary |
| --- | --- | --- |
| DESCRIPTION/TNS | Bounded nested/alias/comment parsing, file-type/size checks before/after reads; map roles/groups/weights/source-strategy hints; no implicit environment search/disk cache | Actual TNS changes/permissions/filesystem blocking unverified; callers manage source files |
| OCP | Explicit URL/injected HTTP Client; Java data.NET_SERVICE_NAME/DESCRIPTION envelopes/direct descriptions; reject duplicate keys/size-depth limits/failure status/timeouts/redirects | Actual OCP versions/response variants/TLS/authentication/service permissions/changes pending integration; Java in-memory examples are not HTTP contracts |
| Shared refresh | Lazy serialized refresh, service-identity/role gates, last-success snapshot/hard expiry/new-connection generations/same-address health inheritance/idempotent close | Source labels do not prove actual tenant/primary-replica roles; static/dynamic sources need server-identity evidence |
| replication/Aurora | Explicit role snapshots/TopologySource adapt through the same path; two standard sql.DB instances; no guessed external-product SQL against OceanBase | Custom replication control planes/actual Aurora callbacks/permissions/role changes require per-service verification; no native OceanBase Aurora support promise |
| Close/legacy entry | sql.DB.Close stops refresh; driver.Open cleans private Connectors; acquired connections survive discovery-context cancellation | Custom sources must respect context; timeout limits waiting but cannot kill user callbacks/filesystem syscalls |

Four-target TNS/OCP TestDiscovery source/manual CI added, compiled only without database connections. For each base DSN variable prefix (remove `_DSN`), configure `_TNS_DSN`, `_OCP_DSN`, `_DISCOVERY_IDENTITY_SQL`, `_DISCOVERY_TENANT`, `_DISCOVERY_ROLE`. Deployment supplies actual-version/privilege-specific identity SQL returning tenant/readwrite-or-readonly columns; tests explicitly assign that role only to the entire unlabeled logical service and verify on the same session. Query must read actual server identity; constants are not acceptance evidence. Role/address changes after controlled refresh and external replication/Aurora discovery require independent real-service evidence, not initial connections/static callbacks. Linux/remote CI/race remain unpassed.

## 19. D28 Per-Capability Authentication and Transport

This table registers support conditions first; exact OceanBase/OBProxy versions/plugin deployments remain unconfirmed. Missing services affect acceptance without removing development scope. Local protocol evidence below does not prove servers install/support same-name MariaDB plugins.

| Capability | Local implementation/target conditions | Actual acceptance status |
| --- | --- | --- |
| old password | Go-supported platforms; explicit AllowOldPassword; eight-byte response/legacy switch, byte-based charsets, empty response for empty password | Algorithm vectors/initial-switch/default rejection; server plugins/both modes/proxies unverified |
| Ed25519 | Go-supported platforms; client_ed25519 strict 32-byte challenge/64-byte signature; pure-Go edwards25519 dependency | Interoperability vectors/initial-switch/trailing-zero local tests; real plugin targets unverified |
| PAM dialog | Target supporting the plugin; all answers require driver-verified TLS; independent per-round callbacks, concurrency-safe/cancellation-aware | Actual local TLS/two-round dialog/default single-password/error-cancellation gates; PAM services/platforms unverified |
| Plugin lifecycle/encoding | Independent AuthFactory instance per use, optional io.Closer; strict built-in PasswordCharset, default UTF-8 | Success/rejection/switch/error cleanup/charsets tested locally; public ChangeUser path belongs to D30 |
| Windows GSSAPI/SSPI | Current Windows identity; Kerberos mutual authentication, NTLM/Negotiate require verified TLS; optional pinned SPN, close native contexts per use | Local native NTLM initial/TLS peers and full database/sql/consecutive ChangeUser/final rejection/original closure in both-mode standard streams tested via TestAuthPluginGSSNativeSessions; no proof of Windows identity switching; domain Kerberos/Negotiate/real-service combinations unverified; context cannot forcibly interrupt system ticket queries |
| Unix GSSAPI | -tags=gssapi; Linux amd64/arm64/386, macOS amd64/arm64; system MIT/Heimdal/Apple GSSAPI, Kerberos/default cache/mutual; purego dynamic calls, no build-time cgo, runtime needs dynamic loader/C libraries | Provider/state-resource cleanup/Windows-amd64 C ABI cases implemented/run; Linux/macOS system libraries/real ticket exchange await execution; no tag/other targets explicitly unsupported |
| TCP | Standard net.Dialer, multi-address/discovery, existing TLS/mode/command flow | Windows local tests; real both-mode/proxy/Linux verification pending |
| Unix socket | Config.Network=unix, Go/OS AF_UNIX; pinned Addr, explicit ServerName for TLS | Local Windows socket protocol I/O/close/timeouts/gates tested; Linux/actual service sockets unverified |
| Named Pipe | Config.Network=pipe; Windows go-winio overlapped I/O; configured retries for temporary absence, no permission retries; non-Windows built-in dialing unsupported | Local Windows protocol I/O/close/retries/gates; remote synchronous CreateFile has no strict context interruption guarantee, architecture §9.0.1; remote permissions/services unverified |
| Shared Memory | Config.Network=sharedmemory / Addr base name; native Windows events/mappings; soft cancellation budget after allocation request; no batch pipeline | Both modes/three streams/TLS-plaintext gates/native boundaries/cleanup/concurrency tested locally; permanently silent servers can block allocation; actual services/ACLs/mixed clients unverified; architecture §9.0.5 |
| OBProxy VC | NewOBProxyDialer; Linux amd64/arm64 -tags=obproxy; standalone C host calls official init/close, isolates native signals/locale/allocator; Go Unix channel reuses deadlines/TLS/authentication; pinned host/library/configuration/working directory | Concurrency/cancellation/failure caching/fixed endpoint/both modes/three streams/TLS gates and Windows-amd64 standalone C host-ABI fixtures/readiness/parent-EOF exit tested; real Linux proxy startup/signals/exit/services await execution, architecture §9.0.6; D28 platform evidence incomplete |

## 20. D29 LOCAL INFILE

`Import`/single-use `LocalInfile` authorize exact-name Reader/Open callbacks for only the current command. Ordinary commands cannot upload; protocol capability bits do not grant local-file permissions. Decode request names by session charset, send content unchanged; reuse standard/compressed/OB20 and existing cleanup. Both modes require autocommit outside transactions/XA-used sessions. See architecture §17.3.1 for size/timeouts/Reader ownership/failures.

| Target/capability | Current evidence | Conditions pending verification |
| --- | --- | --- |
| MySQL compatibility direct/proxy | Local request/rejection/failure/cancellation/all three streams; direct 4.4.2.2 autocommit content/counts and transaction refusal pass | Deployed proxy version/capability/privilege combinations and failed-autocommit partial effects remain deployment-specific |
| Oracle compatibility direct/proxy | Shared both-mode implementation/byte streams; direct 4.4.2.2 autocommit content/counts and transaction refusal pass | Deployed proxy version/capability/privilege combinations remain unverified; optional LOCAL_IMPORT_SQL override must contain {table} and request input |
| Security/resources | No implicit local-file reads; local one-use authorization/name rejection/source limits/closure/no-replay regressions | Synchronous Open/Read/Close must cooperate with context; local peers cannot prove partial database effects |

Four-target manual CI runs `TestLocalImport` with optional `OB_MYSQL_LOCAL_IMPORT_SQL`, `OB_ORACLE_LOCAL_IMPORT_SQL`, `OBPROXY_MYSQL_LOCAL_IMPORT_SQL`, `OBPROXY_ORACLE_LOCAL_IMPORT_SQL` overrides. Both modes use default LOAD DATA SQL when absent. Direct 4.4.2.2 modes pass; deployed proxy acceptance remains outstanding. Historical stage-only evidence below does not override the current release report.

## 21. D30 Session Helpers and Warnings (Initial Batch)

| Capability | Local implementation | Pending verification/incomplete |
| --- | --- | --- |
| schema/catalog | Pinned sql.Conn; Oracle current schema/MySQL current database-COM_INIT_DB; Oracle catalog unsupported; strict name encoding/escaping | Actual service versions/privileges/proxies/explicit prepared binding semantics pending integration |
| Client information/session variables | Three ClientInfo fields map to session user variables; system-variable identifier validation/value escaping/SQL NULL; encoding-timezone setters reserved for Config.Session | Both-mode/proxy variable SQL/writability/value domains pending integration |
| Pool isolation after mutation | Reject active transactions/disabled autocommit; sent mutations prevent return to original pool while pinned connections remain usable; all setters clear automatic preparation/type-description caches before sending | Real-target physical-disconnect/new-borrower state-isolation verification unexecuted |
| Warning counts/details/clearing | OK/EOF WarningSummary, per-Execute-item ResultInfo.Warnings; MySQL ReadWarnings reads current SHOW WARNINGS levels/codes/messages; ClearWarnings suppresses locally until next ordinary SQL; logger-independent | Oracle counts only, details ErrUnsupported following Java generic guidance; SHOW WARNINGS has no SQLSTATE, never inferred. Current diagnostics are not historical snapshots; nonzero-warning real targets unverified |
| ChangeUser | Same-connection COM_CHANGE_USER; reauthentication/baseline/same mode; identity generations invalidate server-client statements/pinned resources; failure closes, successful switched sessions leave original pool on close | Both modes/plugin switches-gates/cancellation/actual local TLS/compressed-OB20 tested; four-target TestChangeUserSessions only compiles; real identity/privilege/proxy verification pending |
| Explicit cancellation | QueryTarget/CancelTarget/CancelQuery; single-use first SQL/first pipeline chunk, original endpoint/private auxiliary channel/same user-tenant/verified TLS; completion-reset-identity changes invalidate; coordinate in-flight confirmation | Both modes/TLS/fixed endpoint/privileges/unknown outcomes/local races tested; actual interruption codes/proxy behavior/remote-ID reuse windows unverified; no unsupported atomic guarantees |

Four-target TestSessionOperations/TestChangeUserSessions source/manual CI added; only compilation this phase without database connections. D30 remains in progress; totals 28/32 implemented, 0/32 accepted. See architecture §16.3.1 for APIs/state lifecycles.

ChangeUser integration inputs: OB_MYSQL_CHANGE_USER_DSN, OB_ORACLE_CHANGE_USER_DSN, OBPROXY_MYSQL_CHANGE_USER_DSN, OBPROXY_ORACLE_CHANGE_USER_DSN; each must specify another identity on its original service. Missing values fail mandatory acceptance; ordinary skips do not pass. See architecture §16.3.2.

Four-target TestWarningDetails source/manual CI added; OB_MYSQL_WARNING_SQL, OB_ORACLE_WARNING_SQL, OBPROXY_MYSQL_WARNING_SQL, OBPROXY_ORACLE_WARNING_SQL must provide successful SQL yielding nonzero warnings (MySQL must retain actual details). Missing values fail mandatory acceptance; skips do not pass. Only compilation this phase, no SQL execution. See architecture §16.3.3 for current diagnostic areas, limits/failing connections, and local clearing.

Four-target TestCancelQuerySessions source/manual CI added; each DSN prefix supplies CANCEL_SQL/CANCEL_CODE, for example OB_MYSQL_CANCEL_SQL/OB_MYSQL_CANCEL_CODE; other prefixes OB_ORACLE, OBPROXY_MYSQL, OBPROXY_ORACLE. SQL must be a sufficiently long blocking read-only query; CODE a confirmed nonzero interruption code on the actual target. Arbitrary syntax/privilege errors cannot count as successful cancellation. Source only compiles without database connections; local tests cannot verify real ID allocation/proxy routing/permissions. This sub-item is implemented but D28 dependencies remain incomplete, so D30 stays in progress with 28/32 implemented, 0/32 accepted; see architecture §16.3.4.

## 22. D31 Local XA Contracts

| Capability | Current Go behavior | Evidence/boundaries |
| --- | --- | --- |
| XID/flags | Copy binary XIDs, validate flags by stage/mode; explicitly expose Oracle extensions | Native bytes/negative FormatID/empty BranchID/invalid lengths-stages pass locally; actual versions unverified |
| Both-mode execution | MySQL XA SQL; Oracle DBMS_XA native XID/OUT, preserve prepare votes/XA errors | 1PC/2PC/rollback-only/suspend-resume/native outputs-return codes pass locally; Oracle native calls require >=2.2.76 |
| Recovery/isolation | Parse actual four-column/collection results, bounded complete lists; dedicated sessions leave original pool; no local rollback of prepared branches | NULL/lengths/extra results/limits/prepared Close/transaction-identity-session guards pass locally |
| Faults | Post-write unknown outcomes evict without replay; commit additionally ErrCommitUnknown | Partial writes/lost confirmations/tail errors after native outputs pass locally; no real fault-recovery evidence |
| Error classification | Preserve standard XA codes for MySQL duplicate XID/timeout-deadlock rollback; Oracle SQL errors never use MySQL numbering, DBMS_XA codes unchanged | [TestXAMySQLErrorCodes, TestXAOracleServerErrorNamespace](../xa_errors_test.go) verify chains/complete rejection not unknown/same-connection Ping-branch state/pool exclusion; actual server triggers pending integration |
| Explicitly unsupported | Forget/RM timeout never fabricate success; application owns coordinator logs/recovery decisions | Server contracts Java itself does not implement; context is not RM timeout |

Four-target TestXATransactions/TestXARecoveryAfterDisconnect source/manual CI added for DML, 1PC/2PC/rollback, join/resume/suspend, disconnect after preparation/new-connection recovery/commit visibility; only compiled this phase. Both-mode standard/compressed/OB20 local coverage does not replace server SQL/privilege/version/proxy/RM recovery semantics. D31 remains in progress because D30/D28 platform dependencies are incomplete; 28/32 implemented, 0/32 accepted. See architecture §16.2.1.

## 23. D32 Local Performance Fixtures

Sixteen baseline scenarios cover both-mode CRUD, per-call/reused preparation, cache hits, sequential/multi-send batches, 16KiB BLOBs, and 32-row fetch; MySQL batch rewriting/Oracle native array binding bring the total to 18, with local peers/ordinary correctness tests. See [Local benchmarks](go-driver-benchmarks.md) for Go1.25.0/1.26.0 Windows-amd64 short samples/reproduction. In-process peers/assertions/scheduling count in measurements; no real SQL/network/server work, nor every transport/optimization combination. Real-database performance, remote streaming LOBs, and long-term resource stability remain unverified; no combination has performance acceptance, and D32 remains in progress.

`TestDriverResourceStability` reuses these scenarios for eight fixed rounds, checking session/statement/LOB/cursor cleanup/cache/pool bounds each round and recording process heap/goroutines after GC; manual `stability` CI is independent of database integration. Serial net.Pipe basic paths cannot prove real handles/full concurrency-fault paths/long-term production stability; see local benchmarks §6 for samples/execution.

`TestDriverSessionStability` adds sustained mixed workloads on the same physical session across both modes/three streams: caching/type reuse, early Rows close, transaction commit/rollback, statement release/Ping after explicit SQL errors. One dial per combination, direct original-session/pool checks each round; process samples retain active idle connections, as defined in local benchmarks §7. Bounded offline checks cannot replace real-platform/full advanced-capability-fault/long-term production acceptance.

`TestDriverConcurrentSessionStability` pins three physical sessions in one pool and concurrently runs the mixed workflow with independent IDs/arguments and a first-round protocol barrier per sampling segment, checking original sessions, release/reborrow, pool counts, and actual peer closure. See [Local benchmarks §8](go-driver-benchmarks.md#8-concurrent-mixed-load-on-three-pooled-connections) for both-mode/three-stream samples of 18432 rounds per toolchain/scope. Limited three-connection samples do not replace full concurrency/fault/advanced-resource or real-platform long-term acceptance.

### 2026-10-19 Java Feature Difference Fixes

| Capability | Current contract | Verification boundary |
| --- | --- | --- |
| Batch generated keys | BatchItem.InsertID/InsertIDKnown retains actual nonzero uint64 IDs; never infer IDs from multi-item aggregates | Sequential/multi-send/multi-statement/single-item array peers covered |
| Named procedure parameters | Simple MySQL CALL maps declared IN/OUT/INOUT names, reading metadata per call on the original connection; ordinary SQL remains positional | Server/client/explicit prepare-direct call/Exec-Query/outputs/failures covered by peers |
| Column/built-in type metadata | MetadataColumns appends auto_increment/generated; MetadataTypeInfo separate from Oracle UDT MetadataTypes | Both-mode public Rows/NULL/types-column order/filters covered by peers |
| Multi-VALUES | Explicit BatchMultiValues, simple placeholder tuples only; Chunks retains aggregate counts/IDs, per-item values unknown | Three streams/budgets/continue-stop/cancellation/disconnect/safe fallback covered; server semantics/performance unaccepted |

No database connections or Linux deployment runs in this round; overall totals remain 28/32 implemented, 0/32 real acceptance.
