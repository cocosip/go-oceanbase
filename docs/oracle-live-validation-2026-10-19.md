# Oracle-mode live validation — 2026-10-19

## Scope and outcome

The user-provided direct Oracle tenant ran OceanBase **4.4.2.2**. Tests used the shared Go driver, `tests/integration` and the standalone `cmd/obtest`, on Windows amd64 / Go 1.27.2. Credentials and connection strings are intentionally absent. No grants, tenant configuration changes or application-table writes were performed.

The latest complete standalone selection (`obtest-run-oracle-20261019-09.json`) returned **35 pass / 1 fail / 4 skip / 5 not applicable**, across 45 scenarios. The full Oracle integration selection also has one failing test: sensitive-cursor visibility. This is incomplete acceptance, not an all-green Oracle compatibility claim. Complete multi-environment acceptance remains **0/32**.

The initial standalone run had 15 pass / 21 fail / 3 skip / 5 not applicable (44 scenarios before splitting NCLOB from BLOB/CLOB). Initial and subsequent reports remain private local artifacts; the checked-in description records conclusions without deployment secrets.

## Demonstrated defects fixed in the common driver

| Trigger | Root cause and correction | Evidence |
| --- | --- | --- |
| Oracle combined execution / PL/SQL OUT / XA start | COM_STMT_PREPARE_EXECUTE omitted the mandatory SQL checksum when the optional ordinary-execute checksum was disabled. Always append the combined-command checksum. | Independent JDBC checksum golden, unit tests and live combined execution/OUT/XA |
| Combined no-rowset response with FLT metadata split across frames | Inner and compressed counters restart for the tail, while OB20 packet/request identities continue. Track OB20 and compressed counters separately. | Independent framing fixture with one/two metadata frames, live optimized execution with FLT |
| Rebinding real ROWID | Real OceanBase ROWIDs have an optional leading `*`; validation rejected it. Accept one leading marker while retaining the payload whitelist. | Captured ROWID golden, malformed-input tests, text/server/client live scalar paths |
| Client-interpolated TIMESTAMP WITH LOCAL TIME ZONE | An implicit precision of six rounded nanoseconds. Cast explicitly with precision nine. | Literal goldens and live nanosecond scalar round trips |
| Reading native BLOB/CLOB | 4.4 locators use common header `0x00018401`, instead of only the legacy magic. Accept this observed header, retaining length/version checks. | Full locator golden independently read by Connector/J; native/table LOB, long upload, nested LOB and cursor reads |
| Closing a V2 locator | Its temporary-looking flag can refer to persistent data. Guard FREETEMPORARY with DBMS_LOB.ISTEMPORARY, matching the existing V1 policy. | Temporary/retained-locator unit checks and live cleanup/session reuse |
| Table BLOB targeting / updatable cursors | ALL_TAB_COLUMNS lacks VIRTUAL_COLUMN. Read visible columns from ALL_TAB_COLS. | Shared dictionary fixtures, live BLOB and cursor mutation/rollback |
| Dictionary column ordinal | Physical COLUMN_ID includes internal columns and can start at 16. Compute logical ordinals across visible columns before applying a column-name filter. | Metadata contract regression and real table/column/key metadata |
| Setting numeric session variables | Quoted numeric wait_timeout was rejected in Oracle. Use current result metadata to choose numeric literals in either mode; Oracle probes include FROM dual. | Initialization/error-boundary unit tests and live session operations |
| XA recovery metadata | SYS.DBMS_XA_XID_ARRAY reports a NULL type-version byte. Interpret this version as unversioned (zero) without accepting malformed encodings elsewhere. | Independent column-metadata golden and six live XA workflows |
| Preparing during a managed Oracle transaction | Oracle PREPARE metadata reports static transaction/autocommit bits `3`. Preserve prior bits at that specific metadata boundary so preparing does not invalidate active resources. | Transaction-generation regression; live cursor updates and XA association/recovery |

No public API or dependency was added. Text-protocol complex values remain explicitly unsupported; native OBJECT reads must use server-prepared binary queries. The standalone complex scenario now exercises that documented route.

## Positive real-target coverage

The complete selection passed connection/version, CRUD/counts, server/client preparation, exact scalar/Unicode/NULL values, Oracle national text/TZ/LTZ/intervals/ROWID, transactions/savepoints/read-only enforcement, dictionary/parameter metadata, execution summaries, sequential/native-array batches, combined execution/caches, full reset, client trace/FLT, long uploads, transactional table BLOBs, procedures/functions with both preparation modes, forward/static/updatable cursors, RETURNING, BLOB/CLOB handles, OBJECT storage and PL/SQL collection IN/OUT, REF CURSOR/LOB procedure outputs, session isolation, context cancellation and explicit server cancellation.

All six XA scenarios passed: one-phase, two-phase, rollback, suspend/resume, JOIN, and recovery/commit on a fresh connection after disconnect. These Oracle results differ from the MySQL tenant's XA SUSPEND/JOIN rejection.

Additional opt-in integration checks passed:

- Read timeout independent of the caller deadline, eviction and replacement of the broken session.
- Explicit cancellation of a long read-only query, original-session reuse and one-shot target invalidation.
- Required-TLS refusal on this SSL-disabled endpoint before the credential callback runs.
- Required OB20, checksum, new metadata and proxy-checksum round trips. Compression is not offered and remains skipped.
- Complex values with nested BLOBs, procedure output failure atomicity and native resource lifetime/rollback.

OceanBase reports cancellation as code **600**, SQLSTATE **70100**, internal code **-5065**, message **Query execution was interrupted**. The standalone default accepts this complete error envelope through every error-chain branch, alongside Oracle 1013; unrelated internal errors or joined cleanup/deadline failures still fail. The opt-in integration cancellation fixture explicitly configures the observed code.

The metadata tool fixture now explicitly declares NUMBER(38,0): this target can report NULL DATA_PRECISION for an INTEGER declaration. The driver continues to preserve the dictionary's unknown precision; no precision is fabricated.

## Failure and unavailable coverage

| Capability | Observed result | Classification |
| --- | --- | --- |
| Sensitive cursor sees another session's committed change on reposition | Existing row remains the opening snapshot; acceptance assertion fails | Unresolved API/target behavior gap; not marked pass or skipped |
| NCLOB DDL | 902 invalid datatype | Target restriction; BLOB/CLOB pass independently |
| CURSOR(subquery) child column | 902 invalid datatype | Target restriction; OUT SYS_REFCURSOR passes independently |
| Nested-table collection column storage | 600 / 0A000 / -4007, nested table type not supported | Target restriction; OBJECT columns and native PL/SQL collection binding pass independently |
| CREATE VIEW | 1031 insufficient privileges | Current account restriction; table/column/key metadata checks continue |
| Positive nonzero Oracle warnings | No deployment fixture supplied | Skipped; no fabricated warnings |
| Distinct-identity ChangeUser / proxy user | No second identity supplied | Skipped; same-identity unit coverage does not prove this deployment |
| Oracle LOCAL import | No supported deployment-specific SQL fixture supplied | Skipped; no MySQL LOAD DATA syntax assumed |
| Encrypted TLS, deployed OCP/OBProxy, multi-endpoint failover / topology changes | No corresponding live endpoints/certificates supplied | Unverified; direct connection and TLS refusal cannot substitute |

Independent diagnostics used the official OceanBase Connector/J **2.4.14**, artifact SHA256 `3168e7e3f2f14f35738d7c695e05012bf351f623757c7baada29659d2afa2ad4`. It also rejected NCLOB, child CURSOR expressions and nested-table storage. Its requested/returned TYPE_SCROLL_SENSITIVE result still read the old value after another connection committed a change, under both ordinary and combined execution. This corroborates native target/JDBC behavior, but does not fulfill the Go API's promised sensitivity. No automatic SQL re-execution or server changes were introduced to disguise this failure.

A NULL element in a collection of OBJECTs is returned by this target and Connector/J as an OBJECT whose attributes are all NULL; the PL/SQL IS NULL predicate is false. Tests verify the predicate, schema/type/attribute count and every NULL attribute together. The driver preserves the server representation rather than inventing an element-level NULL.

## Reproduction and retained evidence

Set `OB_ORACLE_DSN` privately, `OB_TEST_PROFILE=oracle`, and use the common entry points:

```powershell
go test -tags=integration ./tests/integration -count=1 -timeout=6m -v
go run ./cmd/obtest -config obtest.local.json -report obtest-run-oracle-new.json
```

The local configuration selects the target through `dsn_env`, without an inline DSN. Optional read timeout/cancellation use `OB_ORACLE_READ_TIMEOUT_SQL`, `OB_ORACLE_CANCEL_SQL` and `OB_ORACLE_CANCEL_CODE=600`; the exercised read-only fixture was `SELECT SUM(SIN(LEVEL)) FROM dual CONNECT BY LEVEL <= 100000000`, canceled or disconnected promptly. TLS refusal uses `OB_ORACLE_TLS_DISABLED=1`. Missing optional fixtures remain explicit skips. Reports refuse overwriting existing files.

Retained local evidence:

- Full tool: `obtest-run-oracle-20261019-09.json`, `.cache/oracle-tool-09.txt`.
- Full integration: `.cache/oracle-integration-full-14.txt`; later fixture-only cleanup/refactoring is covered by `.cache/oracle-final-focused-15.txt` (same sensitivity failure) and `.cache/oracle-final-complex-16.txt` (complex types pass).
- Initial red cases: `.cache/oracle-baseline-integration-01.txt`, initial tool report.
- Independent JDBC: `.cache/oracle-jdbc-capabilities-03.txt`, `.cache/oracle-jdbc-sensitive-04.txt`, `.cache/oracle-jdbc-array-05.txt`.
- MySQL full regression: `.cache/oracle-mysql-regression-01.txt` (passes, explicit optional/deployment skips remain).
- Offline quality: uncached full unit suite, build, integration-tag vet and ordinary/integration-tag golangci-lint; the final check results are recorded below.

## Cleanup and remaining risk

Early fatal protocol errors and timed-out recovery tests left owned objects and six XA branches. Ownership was established by joining global transaction IDs to locks and the exact test tables' DATA_OBJECT_ID, alongside report run IDs and creation times. Only these exact XIDs were rolled back; no other coordinator's branch was changed. The 23 recorded owned tables/procedures/functions were dropped. Final owned-object inventory was empty and XARecover returned zero prepared branches. Evidence is `.cache/oracle-xa-ownership-proof.txt` and `.cache/oracle-cleanup-result.txt`.

Sensitive-cursor freshness remains a failing acceptance condition. Cross-environment, platform, stress and positive TLS/HA acceptance have not been completed; successful local checks do not remove these limits.

Final quality checks passed: `go build ./...`, `go vet -tags=integration ./...`, uncached full `go test ./... -count=1 -timeout=3m` (Windows named pipes outside the sandbox), and `git diff --check`. Full unit evidence is `.cache/oracle-final-unit-07.txt`; subsequent tool assertion/NCLOB-boundary tests passed with `go test ./cmd/obtest -count=1`. Ordinary and integration-tag lint are both clean (`.cache/oracle-final-lint-default-04.txt`, `.cache/oracle-final-lint-integration-03.txt`). Final tool-only assertion extraction was rechecked against Oracle with the isolated complex scenario (`obtest-run-oracle-20261019-10.json`), which passed. Full Oracle integration remains failing on the explicitly retained sensitive-cursor assertion; it is not included among the passing checks.
