# Release validation — 2026-10-20

The supplied direct OceanBase 4.4.2.2 MySQL and Oracle tenants have no remaining failing exercised acceptance scenarios. This supports a release candidate with the boundaries below, not a claim of complete deployment acceptance. The repository's 0/32 multi-target acceptance total is unchanged. No version tag or package has been published.

## Changes

- Fixed Oracle sensitive-cursor freshness. Native scrollable fetch returns the opening snapshot; the driver must refresh projected values by ROWID, as Connector/J does. The hidden ROWID is appended to preserve positional ORDER BY and SELECT hints, and never appears in public result metadata. Simple base-table projections, aliases, wildcards, repeated read-only columns and LOBs are covered. Updatable cursors still require unique projected columns and complete primary keys.
- Native membership/order are retained. Refresh uses current isolation and does not reapply the original WHERE. Deleted rows return both ErrCursor and sql.ErrNoRows and invalidate the cursor; a complete response preserves the caller's usable session. Transaction-ending refresh responses reject before reading stale LOB locators, with session reuse/explicit rollback verified on plain, compressed and OB20 protocol peers.
- Corrected the Oracle usage example: OBJECT values round-trip through a table; collection IN/OUT round-trips through PL/SQL, avoiding unsupported nested-table storage on this target. Object/collection NUMBER precision and all returned elements are checked.
- Added a four-session concurrent transaction integration test and included it and sensitive refresh in manual CI selection. Added the user-selected MIT license.

The earlier [Oracle report](oracle-live-validation-2026-10-19.md) remains historical. Its JDBC comparison hit cached rows and did not establish that refresh was unsupported. A corrected comparison with official Connector/J 2.4.14 observed `combined cached=initial refreshed=changed`; both comparison fixtures were cleaned up. The original failing Go report is retained locally alongside passing reruns.

## Live evidence

Standalone command: `go run ./cmd/obtest -config <local-config> -report <local-report>`. Both DSNs were supplied through environment variables. The final local report is `obtest-run-dual-20261020-02.json`, ignored by Git.

| Direct mode | Pass | Fail | Skip | Not applicable | Selected |
| --- | ---: | ---: | ---: | ---: | ---: |
| MySQL | 35 | 0 | 2 | 8 | 45 |
| Oracle | 37 | 0 | 3 | 5 | 45 |

`obtest` exits 3 when selected scenarios are skipped; `go run` reports that as shell exit 1 with `exit status 3`. This run is functionally green for exercised scenarios, but not strict all-scenarios acceptance. MySQL's extra not-applicable entry versus the older report is the separately selected native-NCLOB case.

Full `go test -json -tags=integration ./tests/integration -count=1 -timeout=8m` results use `OB_TEST_REQUIRE=0`:

| Direct mode | Passing test/subtest events | Failed | Skipped |
| --- | ---: | ---: | ---: |
| MySQL | 212 | 0 | 6 |
| Oracle | 199 | 0 | 10 |

These are Go test/subtest events, not independent top-level scenario counts. Focused post-review runs also verify repeated projection aliases, positional ordering, hidden-column exclusion, sensitive/updatable Fetch and mutations, cross-connection changes, BLOB materialization, deletion and session reuse. Both `examples/usage` and `examples/xa` passed in both modes.

Positive Oracle warnings were exercised using a uniquely named invalid stored procedure: CREATE completed with protocol warning count 1; warning details passed. The procedure was dropped and USER_OBJECTS independently returned count 0. MySQL distinct-identity ChangeUser and CREATE DATABASE used a disposable generated account/database, subsequently removed and independently verified absent.

Concurrent workload: four pinned sessions per mode, 256 rounds each, **1,024 transactions per mode**, 512 commits and 512 rollbacks. Every transaction read its own inserted value and verified its committed/rolled-back outcome; final row count was 512, pool open=4/in-use=0. Statement cache was enabled. This finite workload is not long-duration leak freedom or a performance benchmark.

## Local and hosted checks

- Windows amd64, Go 1.27.2: full `go test -race ./... -v -count=1 -timeout=3m` passed with MinGW cgo enabled; all seven tested packages passed. One earlier run timed out during Unix Socket peer shutdown. The isolated test and the full rerun passed; retain the original log as an intermittent test failure rather than erasing it.
- Minimum Go 1.26.0, `CGO_ENABLED=0`: full uncached `go test ./... -count=1 -timeout=3m` passed.
- `go build ./...`, `go vet -tags=integration ./...`, normal and integration-tag golangci-lint 2.14.0, `go mod verify` and `git diff --check` passed. No runtime dependencies were added.
- Independent read-only review identified ordering, repeated-projection and refresh-epoch defects, all corrected and re-reviewed without remaining concrete findings.
- Hosted baseline commit `48b7a1a`: [CI](https://github.com/cocosip/go-oceanbase/actions/runs/39232918655) and [CodeQL](https://github.com/cocosip/go-oceanbase/actions/runs/39232918223) were verified successful. CI covered Windows/Linux/macOS Go 1.26, Windows/Linux lint, Linux native ABI, Linux 386 and four fuzz jobs. Manual integration/benchmark/stability jobs were skipped. **These results predate this working-tree patch; the release revision still needs its own hosted checks.** No local Linux distribution is installed.

## Remaining boundaries

| Area | Observed result or missing prerequisite |
| --- | --- |
| MySQL XA suspend/join | Server rejects reassociation with code 1398; both standalone cases skip, ordinary XA workflows pass |
| Oracle NCLOB | Target rejects with OBE-00902; BLOB/CLOB pass separately |
| Oracle nested-table storage / child CURSOR expressions | Target rejects these forms; OBJECT/collection PL/SQL and supported procedure resources pass |
| Oracle view metadata | Supplied identity lacks CREATE VIEW privilege; supported metadata tests pass |
| Oracle distinct ChangeUser / proxy identity | No second identity/proxy grant supplied; CREATE USER probe returns 1031. No existing users or grants were changed |
| Oracle LOCAL import | No supported positive SQL fixture supplied; tested MySQL-style syntax returns 1722/42000. This does not establish that every Oracle import dialect is unsupported |
| Compression | Both endpoints do not offer it; required-mode refusal is exercised, positive compressed protocol peers pass locally |
| TLS-positive | SSL is disabled on supplied endpoints; required-TLS refusal passes, an encrypted endpoint and trusted certificate are still needed |
| Deployed OBProxy / OCP / failover | Only direct single-endpoint tenants supplied; need genuine service/proxy topology and multiple observers for these scenarios |
| Platform-specific authentication / Linux VC | Ordinary hosted baseline unit coverage does not establish these external runtime integrations |

Sensitive refresh adds one prepared query per fetched row. The finite correctness workload does not measure the resulting large-batch/LOB cost. Other server versions and transaction isolation configurations were not established by this run.

## Cleanup and release decision

Independent inventories in both tenants found no test-prefixed objects or routines; MySQL's disposable identity/database were absent, and XARecover returned zero branches in both modes. Warning/probe/JDBC/example fixtures were also checked after cleanup. Credentials remain outside tracked files and the public evidence document.

A first release candidate such as `v0.1.0-rc.1` can declare the tested direct-mode scope and these limitations. Before a general stable compatibility claim, run hosted checks on the actual release revision and close the deployment/privilege scenarios included in that claim. Publishing/tagging remains a separate user action.
