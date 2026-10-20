# Release validation — 2026-10-20

The supplied direct OceanBase 4.4.2.2 MySQL and Oracle tenants have no remaining failing exercised acceptance scenarios. This supports a release candidate with the boundaries below, not a claim of complete deployment acceptance. The repository's 0/32 multi-target acceptance total is unchanged. No version tag or package has been published.

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

| Area | Observed result or missing prerequisite |
| --- | --- |
| MySQL XA suspend/join | Server rejects reassociation with code 1398; both standalone cases skip, ordinary XA workflows pass |
| Oracle NCLOB | Target rejects with OBE-00902; BLOB/CLOB pass separately |
| Oracle nested-table storage / child CURSOR expressions | Nested storage fails with 600/-4007 or 900; selected prepared CURSOR expression fails with 902. Text CURSOR result decoding is unsupported in Go and failed in official JDBC; do not generalize to all server forms. OBJECT/collection PL/SQL and procedure REF CURSOR resources pass |
| Oracle view metadata | Supplied identity lacks CREATE VIEW privilege; supported metadata tests pass |
| Oracle distinct ChangeUser / proxy identity | No second identity/proxy grant supplied; CREATE USER probe returns 1031. No existing users or grants were changed |
| Oracle LOCAL import | Passed with built-in LOAD DATA SQL, exact content/counts and transaction safety; no external SQL fixture needed |
| Compression | Both endpoints do not offer it; required-mode refusal is exercised, positive compressed protocol peers pass locally |
| TLS-positive | SSL is disabled on supplied endpoints; required-TLS refusal passes, an encrypted endpoint and trusted certificate are still needed |
| Deployed OBProxy / OCP / failover | Only direct single-endpoint tenants supplied; need genuine service/proxy topology and multiple observers for these scenarios |
| Platform-specific authentication / Linux VC | Ordinary hosted baseline unit coverage does not establish these external runtime integrations |

Sensitive refresh adds one prepared query per fetched row. The finite correctness workload does not measure the resulting large-batch/LOB cost. Other server versions and transaction isolation configurations were not established by this run.

## Concrete remaining prerequisites

The existing direct tenants suffice for ordinary MySQL/Oracle functionality and negative capability checks. The user does not need to supply import SQL, test code or another direct environment for those checks.

For the two small Oracle privilege/identity checks, a tenant administrator can execute the following **in kayi_oracle** (replace the password placeholder). No administrator credentials need to be shared with the driver tester:

```sql
GRANT CREATE VIEW TO RISDB;
CREATE USER OBG_RELEASE_ALT IDENTIFIED BY "<DBA sets a temporary password>";
GRANT CREATE SESSION TO OBG_RELEASE_ALT;
```

Supply the new login and temporary password privately. ChangeUser only needs `SELECT USER FROM dual` and `SELECT 42 FROM dual`; it does not need DBA, RESOURCE or access to application tables. Tests create/drop their own view. The administrator owns cleanup of the new user and any newly added grant after testing.

| Check | Exact additional input | Test to run |
| --- | --- | --- |
| Oracle proxy identity | Two distinct Oracle identities and server authorization permitting the authenticating identity to open sessions as the effective identity; supply login, effective username and expected identities | `TestProxyUserSessions`: initial identity, ChangeUser, clearing proxy state, old-pool isolation, SYS_CONTEXT current/proxy/session identities. Authorization syntax must be confirmed for the deployed OceanBase version; stock Oracle syntax is not established here |
| Real compression | SQL endpoint that advertises and negotiates CLIENT_COMPRESS, its version and existing tenant login format | `TestPacketModes` with compression required, exact round-trip results; current endpoints refuse negotiation |
| Deployed OBProxy | Existing OBProxy SQL address:port, version and tenant login format | Full MySQL/Oracle integration and standalone targets through proxy; no source SQL or DSNs need to be written by the user |
| Multiple-endpoint failover | Two distinct reachable Observer SQL addresses serving the same tenant/mode | `TestConnectionFailover`: inject first dial failure, connect to second, keep active transaction pinned, recover first for new connections. Does not shut down a node |
| TNS discovery | Existing node list and actual tenant/service role information | Generate the local TNS description ourselves and run `TestDiscovery/TNS`; no TNS server is required |
| OCP discovery | Actual discovery URL/service and tenant identifiers, HTTP authentication if required, expected service role | `TestDiscovery/OCP` and same-session tenant/role proof; a fabricated local HTTP server does not count as real OCP |
| Real TLS | Deferred while no encrypted endpoint exists; when available, address, certificate DNS name and trusted CA; client certificate/key only if mTLS required | Required TLS connection, certificate/hostname rejection and SQL round trips; local TLS peers already pass |
| Server-limited SQL features | An OceanBase version/entry documented to support the exact XA/NCLOB/nested/CURSOR form | Repeat the same independent SQL/JDBC probe, then the Go scenario; extra account privileges cannot fix the observed unsupported datatype/storage/flags errors |

OBProxy/OCP/TLS/cluster topology are needed only for release claims covering those deployments. Real node shutdown, failover and server configuration changes require a separate agreed test window; they were not performed. The release revision still needs hosted CI/CodeQL before packaging as a broadly supported stable release; baseline CI is not evidence for the new commit.

## Cleanup and release decision

Independent inventories in both tenants found no test-prefixed objects or routines; MySQL's disposable identity/database were absent, and XARecover returned zero branches in both modes. Warning/probe/JDBC/example fixtures were also checked after cleanup. Credentials remain outside tracked files and the public evidence document.

A first release candidate such as `v0.1.0-rc.1` can declare the tested direct-mode scope and these limitations. Before a general stable compatibility claim, run hosted checks on the actual release revision and close the deployment/privilege scenarios included in that claim. Publishing/tagging remains a separate user action.
