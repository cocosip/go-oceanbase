# Release validation — 2026-10-20

The supplied direct OceanBase 4.4.2.2 MySQL and Oracle tenants have no remaining failing exercised acceptance scenarios. At the user's direction, unavailable deployments are assessed against official Connector/J 2.4.14 source and offline protocol regressions; no additional server versions, services, users or grants are required for this release review. The compression default/threshold gaps identified in the follow-up below are corrected; no reviewed release-blocking code gap remains. This supports package release within the documented scope, without claiming complete deployment acceptance. The repository's 0/32 multi-target acceptance total is unchanged. No version tag or package has been published.

## Changes

- Fixed Oracle sensitive-cursor freshness. Native scrollable fetch returns the opening snapshot; the driver must refresh projected values by ROWID, as Connector/J does. The hidden ROWID is appended to preserve positional ORDER BY and SELECT hints, and never appears in public result metadata. Simple base-table projections, aliases, wildcards, repeated read-only columns and LOBs are covered. Updatable cursors still require unique projected columns and complete primary keys.
- Native membership/order are retained. Refresh uses current isolation and does not reapply the original WHERE. Deleted rows return both ErrCursor and sql.ErrNoRows and invalidate the cursor; a complete response preserves the caller's usable session. Transaction-ending refresh responses reject before reading stale LOB locators, with session reuse/explicit rollback verified on plain, compressed and OB20 protocol peers.
- Corrected the Oracle usage example: OBJECT values round-trip through a table; collection IN/OUT round-trips through PL/SQL, avoiding unsupported nested-table storage on this target. Object/collection NUMBER precision and all returned elements are checked.
- Added a four-session concurrent transaction integration test and included it and sensitive refresh in manual CI selection. Added the user-selected MIT license.
- Follow-up: fixed Oracle LOCAL import transaction safety. Both modes now reject imports in local/manual transactions, with autocommit disabled, or after XA use before sending SQL or reading input. Both live runners share the autocommit/content/count and preceding-DML rollback assertions; Oracle no longer requires custom import SQL.

The earlier [Oracle report](oracle-live-validation-2026-10-19.md) remains historical. Its JDBC comparison hit cached rows and did not establish that refresh was unsupported. A corrected comparison with official Connector/J 2.4.14 observed `combined cached=initial refreshed=changed`; both comparison fixtures were cleaned up. The original failing Go report is retained locally alongside passing reruns.

## Live evidence

Standalone command: `go run ./cmd/obtest -config <local-config> -report <local-report>`. Both DSNs were supplied through environment variables. The final local report is `obtest-run-dual-20261020-03.json`, ignored by Git; earlier reports are retained.

| Direct mode | Pass | Fail | Skip | Not applicable | Selected |
| --- | ---: | ---: | ---: | ---: | ---: |
| MySQL | 35 | 0 | 2 | 8 | 45 |
| Oracle | 38 | 0 | 2 | 5 | 45 |

`obtest` exits 3 when selected scenarios are skipped; `go run` reports that as shell exit 1 with `exit status 3`. This run is functionally green for exercised scenarios, but not strict all-scenarios acceptance. MySQL's extra not-applicable entry versus the older report is the separately selected native-NCLOB case.

Full `go test -json -tags=integration ./tests/integration -count=1 -timeout=8m` results use `OB_TEST_REQUIRE=0`:

| Direct mode | Passing test/subtest events | Failed | Skipped |
| --- | ---: | ---: | ---: |
| MySQL | 212 | 0 | 6 |
| Oracle | 200 | 0 | 9 |

These are Go test/subtest events, not independent top-level scenario counts. Focused post-review runs also verify repeated projection aliases, positional ordering, hidden-column exclusion, sensitive/updatable Fetch and mutations, cross-connection changes, BLOB materialization, deletion and session reuse. Both `examples/usage` and `examples/xa` passed in both modes.

Positive Oracle warnings were exercised using a uniquely named invalid stored procedure: CREATE completed with protocol warning count 1; warning details passed. The procedure was dropped and USER_OBJECTS independently returned count 0. MySQL distinct-identity ChangeUser and CREATE DATABASE used a disposable generated account/database, subsequently removed and independently verified absent.

Concurrent workload: the initial four-session/256-round run passed. Follow-up used four pinned sessions, 2,000 rounds each, repeated three times: **24,000 transactions per mode**, 12,000 commits and 12,000 rollbacks. Every transaction read its own inserted value and verified its committed/rolled-back outcome; each run ended with 4,000 rows, pool open=4/in-use=0. Statement cache was enabled. This finite workload is not long-duration leak freedom or a performance benchmark.

Independent official Connector/J 2.4.14 comparison reproduced MySQL XA SUSPEND/JOIN code 1398, Oracle NCLOB code 902, nested-table column code 600/-4007 and explicit nested storage clause code 900. For `SELECT CURSOR(SELECT 42 AS N FROM dual) AS C FROM dual`, the prepared path returned 902; the text path reached JDBC result decoding and threw NullPointerException, while Go explicitly returned ErrUnsupported. This does not establish that all CURSOR expressions are rejected by the server. Procedure REF CURSOR outputs pass separately.

Oracle LOCAL import succeeded with explicit tab/newline delimiters and exact content assertions. The earlier 1722 probe used mismatched input delimiters and was not evidence of absent import support. JDBC also showed both preceding DML and uploaded rows surviving rollback, proving implicit commits. The original Go transactional test failed; the shared guard fix now preserves earlier DML for rollback and passes real autocommit import in both modes. Failed autocommit imports can still leave partial effects.

## Local and hosted checks

- Windows amd64, Go 1.27.2: full `go test -race ./... -v -count=1 -timeout=3m` passed with MinGW cgo enabled; all seven tested packages passed. One earlier run timed out during Unix Socket peer shutdown. The isolated test and the full rerun passed; retain the original log as an intermittent test failure rather than erasing it.
- Minimum Go 1.26.0, `CGO_ENABLED=0`: full uncached `go test ./... -count=1 -timeout=3m` passed.
- After the Oracle import safety fix, full race tests and minimum-Go/no-cgo tests passed again, as did build, integration-tag vet, both lint variants and module verification. Additional local TLS/OBProxy protocol peers, certificate rejection/cancellation, packet-mode and concurrent-session tests passed under race with 1,024 stability iterations. They do not establish real encrypted/proxy deployment compatibility.
- `go build ./...`, `go vet -tags=integration ./...`, normal and integration-tag golangci-lint 2.14.0, `go mod verify` and `git diff --check` passed. No runtime dependencies were added.
- Independent read-only review identified ordering, repeated-projection and refresh-epoch defects, all corrected and re-reviewed without remaining concrete findings.
- Hosted baseline commit `48b7a1a`: [CI](https://github.com/cocosip/go-oceanbase/actions/runs/39232918655) and [CodeQL](https://github.com/cocosip/go-oceanbase/actions/runs/39232918223) were verified successful. CI covered Windows/Linux/macOS Go 1.26, Windows/Linux lint, Linux native ABI, Linux 386 and four fuzz jobs. Manual integration/benchmark/stability jobs were skipped. **These results predate this working-tree patch; the release revision still needs its own hosted checks.** No local Linux distribution is installed.

## Remaining boundaries

| Area | Observed result or validation boundary |
| --- | --- |
| MySQL XA suspend/join | Server rejects reassociation with code 1398; both standalone cases skip, ordinary XA workflows pass |
| Oracle NCLOB | Target rejects with OBE-00902; BLOB/CLOB pass separately |
| Oracle nested-table storage / child CURSOR expressions | Nested storage fails with 600/-4007 or 900; selected prepared CURSOR expression fails with 902. Text CURSOR result decoding is unsupported in Go and failed in official JDBC; do not generalize to all server forms. OBJECT/collection PL/SQL and procedure REF CURSOR resources pass |
| Oracle view metadata | Supplied identity lacks CREATE VIEW privilege; supported metadata tests pass |
| Oracle distinct ChangeUser / proxy identity | No second identity/proxy grant supplied; CREATE USER probe returns 1031. No existing users or grants were changed |
| Oracle LOCAL import | Passed with built-in LOAD DATA SQL, exact content/counts and transaction safety; no external SQL fixture needed |
| Compression | Both endpoints do not offer it; required-mode refusal is exercised, positive compressed protocol peers pass locally |
| TLS-positive | SSL is disabled on supplied endpoints; required-TLS refusal and local positive TLS peers pass. Real encrypted deployment remains unverified |
| Deployed OBProxy / OCP / failover | Only direct single-endpoint tenants supplied; official-source review and local protocol/routing peers provide implementation evidence, without certifying real proxy/discovery/cluster operation |
| Platform-specific authentication / Linux VC | Ordinary hosted baseline unit coverage does not establish these external runtime integrations |

Sensitive refresh adds one prepared query per fetched row. The finite correctness workload does not measure the resulting large-batch/LOB cost. Other server versions and transaction isolation configurations were not established by this run.

## Official Java source alignment review

The user cannot provision additional versions or deployments and selected official Java source alignment as the review method. The local reference checkout is clean, its HEAD is `6adc311ba5db8b2f19f98f3e6e4734667fdfdda1`, and its pom declares **2.4.14**. This is the pinned official baseline, not a claim to have reviewed every newer Java release. The earlier deployment/account prerequisite list is superseded; no new infrastructure or grants are requested.

| Area | Official Connector/J evidence | Go implementation, regressions and conclusion |
| --- | --- | --- |
| XA suspend/join | [OceanBaseXaResource](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseXaResource.java#L81) maps JOIN/RESUME/SUSPEND to MySQL SQL, and uses DBMS_XA plus native XID objects for Oracle | [xa_protocol.go](../xa_protocol.go), [xa_test.go](../xa_test.go): matching SQL flags, Oracle native calls, XID fields/flags/votes/error paths. Java's optional pinGlobalTxToPhysicalConnection rewrites JOIN to RESUME; Go preserves the explicit caller flag on a pinned connection and does not implement that Java option. Current target 1398 is not a missing SQL implementation |
| NCLOB / LOBs | [JDBC4ResultSet.getNClob](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ResultSet.java#L2372) wraps getString in JDBC4NClob; [Clob](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/Clob.java#L133) reads V1/V2 locators, and Lob uses DBMS_LOB for out-of-row data | [lob_oracle.go](../lob_oracle.go), [lob_chars.go](../lob_chars.go), [wire locator decoder](../internal/wire/lob.go), TestLOBLocator/TestClobOffsets/TestClobUTF16OverwriteBoundary: locator/NULL/Unicode/resource paths covered. Go's public offsets count Unicode code points and convert UTF-16 server units; Java String/Clob uses UTF-16 units. Java's NClob API does not prove native NCLOB DDL support; native NCLOB remains unverified on the rejecting target |
| Collections / nested storage | [OBArrayParameter](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBArrayParameter.java#L71) writes type metadata, element count, null bitmap and element values; BinaryRowProtocol recursively decodes complex values | [complex_codec.go](../complex_codec.go), [complex tests](../complex_test.go), [regressions](../complex_regression_test.go): corresponding binary metadata/count/NULL/nesting paths exist. Collection transport and PL/SQL support do not implement or guarantee server nested-table storage DDL |
| CURSOR expressions / REF CURSOR | [BinaryRowProtocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/BinaryRowProtocol.java#L2682) reads a four-byte cursor ID; [RefCursor](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/RefCursor.java#L70) fetches/closes it. [TextRowProtocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/TextRowProtocol.java#L1100) returns null for complex array/struct/cursor readers | [rows.go](../rows.go), [cursor_out.go](../cursor_out.go), [cursor_child_test.go](../cursor_child_test.go): binary type 163/four-byte ID, fetch/close, nested child rows and lifetime gates exist; text CURSOR/complex decoding returns ErrUnsupported. Java's null-return paths and observed text-reader exception are not behavior to copy. This does not add a claim that rejected expression SQL works |
| Compression / OB20 | [AbstractConnectProtocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L324) checks CLIENT_COMPRESS; compressionHandler selects OB20 before zlib. [CompressPacketOutputStream](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/io/output/CompressPacketOutputStream.java#L73) defines >100-byte / <90% decisions and seven-byte framing | [protocol.go](../protocol.go), [compress.go](../internal/wire/compress.go): default changed from auto to off; explicit auto retains Java fallback; thresholds now match Java. Required is a documented Go extension. [Direct official-JAR codec test](../internal/wire/compression_java_test.go) separates interoperability successes from rejection of known Java malformed outputs; details below |
| TLS | [sslWrapper](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L672) sends SSLRequest before upgrading the socket, applies protocol/cipher policy and verifies hostname unless weakening options disable it | [authentication.go](../authentication.go), [tls_options_test.go](../tls_options_test.go): matching upgrade order, verified certificate/hostname, TLS1.2/1.3, client certificates, cancellation and rejection. Go uses crypto/tls and refuses insecure verification, rather than copying Java's trust-all switches or Java keystore formats |
| ChangeUser / proxy identity | [sendChangeUser](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L237) writes command 0x11, identity/authentication/database/plugin/attributes; writeConnectAttributes writes __ob_client_proxy_user_name | [change_user.go](../change_user.go), [change_user_test.go](../change_user_test.go), [proxy_user_test.go](../proxy_user_test.go): packet fields, independent authenticating/proxy identities, clearing target, reauthentication and pool/resource isolation covered. Go requires explicit ProxyUser instead of Java's username parsing; actual Oracle authorization remains a server policy, not an omitted client grant command |
| View metadata | [OceanBaseOracleDatabaseMetadata.getTables](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseOracleDatabaseMetadata.java#L204) queries accessible objects with TABLE/SYNONYM/VIEW filters | [metadata_sql_oracle.go](../metadata_sql_oracle.go), [metadata tests](../metadata_test.go): accessible dictionary objects, bound schema/name filters and VIEW type mapping. The driver reads metadata; it does not need to create application views. CREATE VIEW is a test-fixture privilege, not a driver runtime requirement |
| TNS / OCP / failover | [ConfigParser](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/failover/utils/ConfigParser.java#L353) reads descriptions and OCP data.NET_SERVICE_NAME/DESCRIPTION; LoadBalanceDriver builds candidate and blacklist policies | [discovery.go](../discovery.go), [TNS parser](../internal/routing/tns.go), [OCP parser](../internal/routing/ocp.go), [routing regressions](../internal/routing/discovery_test.go), [failover tests](../failover_test.go): compatible description/envelope fields, refresh/health/candidate selection, failure/cancellation and active-session pinning covered. Go uses an explicit URL/source and bounded per-Connector state; it does not reproduce Java's implicit global files/caches or transaction replay policies. This is source/peer alignment, not deployed OCP or cluster-outage validation |

The initial source-review regression command was `go test -race . ./internal/wire ./internal/routing -count=1 -timeout=3m`, Windows amd64/Go 1.27.2 with cgo enabled. All three packages passed (32.121s / 4.451s / 1.321s). This evidence predates the compression follow-up below. No database was contacted for this source review.

The compression follow-up identified and corrected default/threshold discrepancies. The remaining differences above are explicit Go API/security/lifecycle choices, not complete Java API equivalence. Existing live successes, target rejections and skips retain their original evidence classification. Other OceanBase versions, real encrypted/compressed/proxy deployments, Oracle alternate/proxy authorization and cluster fault behavior remain unverified; that scope limitation does not require the user to provision those environments for this package release.

### Compression alignment follow-up

The user requested implementation alignment with the pinned official Connector/J 2.4.14. Go now defaults compression **off**. Explicit `FeatureAuto` / DSN `compression=auto` requests CLIENT_COMPRESS and falls back if absent. Java's size decision is native payload **>100 bytes**, excluding its four-byte header; the zlib input includes that header. The compressed result must be **< `int(0.9f * nativePayloadLength)`**, including Java float32 multiplication/truncation. Otherwise the outer frame carries raw bytes. Go uses the standard-library zlib codec; compressed byte streams need not be identical between implementations.

`FeatureDefault=0` distinguishes the unset policy from explicit auto. Other five protocol policies retain auto defaults. Off=1 and Required=2 retain their numeric values; explicit Auto moves from 0 to 3, while zero now means the per-option default. Applications that persisted numeric Auto=0 must migrate it to Auto=3 to keep explicit compression opt-in; named/string policies are preferred. `FeatureRequired` remains an opt-in Go extension, and OB20 retains priority over optional compression.

[TestCompressionJavaInterop](../internal/wire/compression_java_test.go) runs a small original Java harness against the **actual official JAR**, invoking CompressPacketOutputStream and DecompressPacketInputStream directly. It adds no runtime dependency and skips unless `OB_TEST_JDBC_JAR` is provided. Eight fixtures cover empty, 100/101 bytes, limited savings, noise, Unicode, exactly `0xffffff` bytes, and `0xffffff+1` bytes:

- Go encoding → official Java decoding: **8/8** payloads and their following sentinel message recovered. Java's reader needs lookahead for an empty native packet (`getNextCachePacket` uses a strict `>` header-length condition), so each input stream includes the sentinel.
- Official Java encoding → Go decoding: **5/8** payloads recovered exactly, with no unread data. The other **3/8** are explicit **known-invalid Java output rejection** cases, not successful interoperability.
- Limited-saving and noise fixtures: Java increments mysqlSeqNo while attempting zlib, then again in raw fallback ([lines 163 and 278](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/io/output/CompressPacketOutputStream.java#L163)). The first native sequence is 1, not 0; Go rejects ErrSequence.
- Exact-max fixture: the compressed `commandEnd` path emits its empty terminator and returns before draining four pending native payload bytes ([lines 246–251](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/io/output/CompressPacketOutputStream.java#L246)). Go rejects the truncated message with EOF. The over-max fixture passes because Java flushes the full fragment with `commandEnd=false` first.

Go preserves valid native sequence numbering, complete continuations, expansion/trailing-data checks and packet budgets. It does not copy these Java defects. [TestCompressionJavaThreshold](../internal/wire/framing_test.go) separately verifies the aligned sending decisions; negotiation tests verify default off, optional fallback and OB20 priority. The regression suite also retains malformed-sequence and large-message tests.

Reproduce without a database (Java 21 used for the recorded run):

```powershell
$env:OB_TEST_JDBC_JAR = 'C:\path\to\oceanbase-client-2.4.14.jar'
$env:OB_TEST_JAVA = 'C:\path\to\jdk-21\bin\java.exe'
go test ./internal/wire -run '^TestCompressionJavaInterop$' -v -count=1 -timeout=2m
```

This is direct codec validation against an independent official implementation, not a compressed server/proxy deployment test. Neither supplied endpoint advertises CLIENT_COMPRESS; no additional deployment is requested.

Final follow-up checks, Windows amd64:

- Go 1.27.2 `go test -race ./... -count=1 -timeout=3m`, with the official JAR enabled: all seven test-bearing packages passed; root 27.109s and wire 7.280s. The ordinary examples have no tests. Normal Windows permissions were needed for named-pipe tests after the restricted run reported Access is denied.
- `go build ./...`, `go vet -tags=integration ./...`, `go mod verify`: passed.
- golangci-lint 2.14.0, ordinary and integration-tag runs: **0 issues** each.
- Minimum Go 1.26.0, `CGO_ENABLED=0`, `go build ./...`: passed. Integration-tag tests compiled with `-run '^$'`; no live integration scenario was run in this follow-up.
- `git diff --check`: passed. Independent final code/document review found no remaining substantive issue after preserving the Off/Required enum values.

## Cleanup and release decision

Independent inventories in both tenants found no test-prefixed objects or routines; MySQL's disposable identity/database were absent, and XARecover returned zero branches in both modes. Warning/probe/JDBC/example fixtures were also checked after cleanup. Credentials remain outside tracked files and the public evidence document.

Package release can proceed within the documented scope on the basis of official-source alignment, passing offline regressions and the existing direct-mode live results. Missing deployment fixtures are recorded limitations, not additional provisioning requirements or passing live results. Hosted CI/CodeQL should cover the actual release revision; earlier baseline CI does not validate this revision. Publishing/tagging remains a separate user action.
