# MySQL compatibility live validation, 2026-10-19

The user supplied a test database and authorized real-database testing, fixes and repeated validation. This supersedes the earlier temporary restriction on database integration testing for this target. Baseline results below are retained; the final retest follows them.

## Environment and scope

- OceanBase MySQL compatibility mode, `ob_version() = 4.4.2.2`.
- Windows amd64, Go 1.27.2; TCP, utf8mb4, TLS disabled as supplied.
- One endpoint and one identity. This does not establish an OBProxy, Oracle, TLS, HA or other-platform result.
- Existing application tables were not used. Tests created their own objects and XA identifiers.
- Credentials and DSNs are absent from this document and tracked configuration.

## Initial results before fixes

The complete standalone `obtest` selection ran: **30 passed, 6 failed, 1 skipped, 7 not applicable**. Exit code: 1. The local, ignored report is `obtest-run-mysql-20261019-01.json`, run ID `668917c2fba2`.

Passed scenarios:

```text
connect crud counts-matched counts-changed generated-keys
prepare-server prepare-client scalars mysql-scalars transactions
metadata-parameters execution batch optimized-execution reset-full
multi-results trace long-data table-blob routines routines-client
cursor cursor-update xa-one-phase xa-two-phase xa-rollback
xa-recovery warnings cancel-context cancel-query
```

The ordinary `batch` result includes negotiated fallback: its success is not proof that every preferred native strategy ran. Likewise, `optimized-execution` does not prove every optional optimization was negotiated. `trace` verifies client diagnostics, not server trace visibility.

`change-user` skipped because a distinct second identity was not supplied. The seven Oracle-only scenarios were not applicable. No skip or mode rejection counts as a successful real-feature test.

## Failures and focused reproductions

| Scenario | Confirmed evidence | Interpretation and remaining work |
| --- | --- | --- |
| `metadata` | MetadataColumns returns 15 columns, including auto_increment/generated; both tool and integration consumers still Scan 13 destinations. A direct reproduction returns `expected 15 destination arguments in Scan, not 13`. | Stale test consumers. Synchronize consumers and fixtures with the existing public result contract, then repeat metadata acceptance; the current failure does not establish incorrect driver metadata values. |
| `batch-rewrite` | Connection initialization fails with 1193. `SHOW VARIABLES LIKE 'ob_enable_batched_multi_statement'` returns no rows. | The test configuration depends on a variable absent on this target. Actual rewrite execution was not reached; inspect the version-specific initialization requirement before changing it. |
| `xa-suspend` | `XA END ... SUSPEND` returns 1398 / XAE05, invalid arguments. | Reproduced on a separate generated branch. Basic XA succeeds, but suspend/resume is not accepted on this combination. |
| `xa-join` | `XA START ... JOIN` returns 1398 / XAE05, invalid arguments. | Reproduced on a separate generated branch. JOIN is not accepted on this combination. |
| `session` | SetSessionVariable passes wait_timeout as a quoted string; server returns 1232 / 42000, incorrect argument type. Sending the same value as an integer succeeds and preserves 28800. | Numeric system-variable assignment is incompatible with the helper's current string-only SQL representation on this target. Do not generalize this result to all session variables. |
| `local-import` | Import succeeds and content matches, but Rollback leaves both imported rows. A focused transaction inserted ID=9 before importing IDs=1/2; after Rollback all three rows remained. | The observed LOAD DATA LOCAL operation has implicit-commit behavior on this combination. The promised rollback boundary is not satisfied, including earlier DML. Keep the failure visible; do not assume ordinary transaction rollback protects imports. Other clients/server configurations were not compared. |

The existing integration package was also run with `OB_TEST_PROFILE=mysql`, `OB_TEST_REQUIRE=0`, `-count=1`, `-timeout=10m` and JSON output. It exited 1 and independently reproduced the six scenario failures above (the two XA variants share one parent test). Its packet-mode test additionally failed when requiring compression. Separate connection probes returned ErrUnsupported for required compression and required OB20. These are capability rejections on this endpoint, not successful compressed/OB20 execution.

Integration top-level PASS entries can contain skipped child tests or mode-boundary checks; they are not an independent feature-acceptance count. Missing fixtures included second identity, multi-address failover, discovery, database creation and deployment-specific queries. The standalone tool supplied its own warning and cancellation fixtures, so those tool scenarios did execute and pass.

## Cleanup and offline checks

- The standalone report records 32 dropped objects, four resolved XA branches and two XA branches rolled back by cleanup. Its single create_unconfirmed table was independently confirmed absent: initialization failed before its CREATE reached execution.
- The integration metadata failure left its own view and two related tables: its error path did not close Rows before attempting cleanup. Their unique timestamp names matched this run; those exact three objects were dropped in view/child/parent order and verified absent. No unrelated objects were dropped.
- Integration XA failure cleanup tried to borrow another connection while holding the only permitted connection, producing two 30-second cleanup timeouts. The sessions subsequently closed; no prepared branch was created by either failing operation. This failure path needs correction before repeated acceptance runs.
- Focused diagnostic tables were dropped and their generated XA branches rolled back successfully.
- Final read-only inventory found no OBT_/OBGM_ objects in the test database, and XARecover returned zero recoverable branches.
- `go build -o bin/obtest.exe ./cmd/obtest` passed. `go test ./...` passed after rerunning outside the sandbox, which had denied Windows Named Pipe access. Workspace-local Go build/module caches avoided default cache write restrictions.
- Local diagnostic source/output and integration JSONL are under ignored `.cache/`; the standalone JSON report is also ignored. These local artifacts must be retained separately if evidence is needed on another machine.

This is the first partial real-environment validation, with explicit failures. It does not complete any task's mandatory multi-target acceptance matrix; the overall acceptance total remains 0/32. No production deployment, Linux execution, race/stress run, Oracle, TLS, real failover/discovery or second-identity test was performed.

## Fixes and complete retest

All validation used the existing public driver, `cmd/obtest`, and `tests/integration`. An ignored diagnostic program used the same public APIs solely to inspect individual failures and owned-object cleanup; it did not replace the common acceptance suites or implement another protocol.

- Metadata consumers and fixtures now scan the existing 15-column contract and check auto-increment/generated flags. Integration Rows close on failure before DDL cleanup. The tool also accepts the two known MySQL INT dictionary precisions, 10 and the target's actual 11, while retaining negative precision/scale tests.
- Shared initialization and runtime system-variable setters encode MySQL decimal int64/uint64 strings as normalized integer literals. Other values remain escaped strings; Oracle encoding is unchanged. Regressions cover signs, leading zeros, overflow, injection, initialization rejection and session isolation.
- MySQL Import now refuses local/manual transactions and XA-used sessions before SQL/upload, preserving earlier DML for rollback. Autocommit import verifies exact content and counts. This is a documented safety restriction: transactional MySQL import has not become supported, and failed autocommit uploads may leave partial effects. Oracle transaction behavior retains offline regression coverage.
- Batch acceptance no longer sets a variable absent on this target. The driver falls back only for unknown-variable 1193; syntax/permission errors propagate without sending DML. Native rewrite remains unavailable when its safety gate cannot be confirmed; sequential content/error/rollback checks still execute, and fallback is never counted as native rewrite success.
- XA integration cleanup resolves the owned branch on its original pinned session before releasing the sole pool lease, then uses fresh recovery only when necessary. Only exact MySQL flag rejection 1398 is classified as unavailable for SUSPEND/JOIN; timeout, cleanup, unrelated and mixed errors still fail.
- Packet capability rejection is explicitly skipped rather than logged as successful framed execution. Strict acceptance (`OB_TEST_REQUIRE=1`, or tool `require_all=true`) still fails required missing capabilities.

The complete 44-case tool retest produced **33 pass / 0 fail / 4 skip / 7 not applicable** in `obtest-run-mysql-20261019-03.json`. Exit 3 means incomplete coverage due to skips, not full acceptance. The four skips are native batch rewrite, XA suspend/resume, XA join, and change-user without a second identity. Seven scenarios remain Oracle-only. All registered cleanup actions succeeded.

The full MySQL integration package also exited 0 with no failing tests (`.cache/integration-mysql-20261019-03.jsonl`). Besides default fixtures, this run supplied read-only SLEEP statements for explicit cancellation and independent read timeout, a CAST warning statement, and the existing test identity for temporary auto-create-database validation; all four additional workflows passed. Created databases were uniquely named and dropped by the suite.

Integration still skips 12 child tests: native rewrite; Oracle RETURNING; discovery; two-address failover; five packet modes (compression, OB20, checksum, new metadata, proxy checksum); second-identity change-user; and two XA flag variants. Parent PASS entries do not establish execution of skipped children. Standard/auto packets, basic XA, two-phase recovery, CRUD, binding, transaction, cursor, BLOB and the other passing common scenarios executed their assertions on the real server.

Remaining unavailable environments are Oracle, OBProxy, TLS, failover/discovery, a second identity and other operating systems. This single combination still does not complete the mandatory multi-target acceptance matrix (0/32 overall).

Final verification: `go build ./...`, full `go test ./... -count=1 -timeout=5m`, `go vet ./...`, golangci-lint 2.14.0 (`0 issues`) and `git diff --check` passed. Windows Named Pipe tests required execution outside the restrictive sandbox. Workspace-local caches were used. Final read-only inventory found no test-prefixed tables/views, routines or temporary `obgdb_` databases; XARecover returned zero branches. The final tool report records 33 dropped objects, four resolved XA branches and two branches rolled back by cleanup, with no cleanup errors. No commit or push was performed.

### Follow-up review of build, tests and lint

A fresh review repeated the full build and uncached unit suite. All test-bearing packages passed; the symbolic-link test `TestTNSFileFollowsRegularTarget` skipped because symbolic links were unavailable. The previously reported default lint command excludes integration-tagged files. Running `golangci-lint run --build-tags integration ./...` exposed seven additional findings: four repeated constants, two test-function complexity findings, and one Boolean expression suggestion. These were corrected in the integration tests by naming repeated values and separating batch-result assertions and XA failure policy, retaining the existing checks and skip conditions.

After those corrections, both default and integration-tagged golangci-lint returned `0 issues`. `go build ./...`, integration-test compilation and `go vet -tags=integration ./...` passed. The complete MySQL integration suite was rerun with the same extra cancellation/read-timeout/warning/database-creation fixtures; it passed with the same 12 explicitly skipped children. Fresh logs are under ignored `.cache/review-*-20261019*`; this does not add evidence for unavailable Oracle/OBProxy/TLS/platform combinations.
