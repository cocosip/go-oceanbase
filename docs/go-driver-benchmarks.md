# Go driver local performance benchmarks

The current minimum Go version is 1.26. Historical samples below retain the Go 1.25/1.26 versions and raw metrics recorded at collection time; Go 1.25 is no longer supported. Reproduction commands use Go 1.26.

## 1. Scope and interpretation

`BenchmarkDriver` runs both OceanBase compatibility modes over one physical connection to an in-process `net.Pipe` protocol peer. It includes 18 scenarios: the original 16 plus MySQL batch rewriting and Oracle native array binding. See [driver_benchmark_test.go](../driver_benchmark_test.go). It connects to no database, executes no real SQL and excludes network round trips and server execution, storage and lock overhead.

Timing and allocation statistics include database/sql, the driver, peer response construction, protocol I/O, scheduling and result assertions. B/op and allocs/op are therefore **not driver-only allocations**, and ns/op is not database latency. The current fixtures use standard packet framing and one connection; they do not cover TLS, compression, OB20, multi-connection concurrency or real proxy performance. Short samples support later comparison using the same fixtures; they define no performance pass threshold and do not establish relative toolchain performance.

## 2. Scenarios and timing boundaries

Each scenario completes handshake, initialization, Ping and one warmup cycle before timing. These steps and final cleanup are excluded. It then runs N cycles, checking protocol commands, parameters, results and resource closure. A second physical dial fails immediately so that automatic reconnection cannot hide fixture errors.

| Scenario | Operations and assertions per op | Timing boundary |
| --- | --- | --- |
| crud | One INSERT, UPDATE, SELECT and DELETE; each write affects one row; query returns 42 | Four complete calls and assertions |
| prepare-per-call | Parameterized INSERT; verify parameters and one affected row | Prepare/execute/close on every cycle |
| prepare-reuse | INSERT using an explicit sql.Stmt; verify one affected row | Execute only; prepare/final close excluded |
| cache-hit | Implicit prepared-statement cache hit, StatementCacheSize=1, ReuseParameterTypes=true | Reuse statement/types after warmup; verify omitted type header |
| batch-sequential | Eight INSERT items per cycle; verify strategy, per-item status and affected counts | Per-item prepare/execute/close, result allocation and assertions |
| batch-multisend | Eight INSERT items per cycle; verify actual MultiSend strategy and per-item results | Shared prepare per batch, chunked I/O and close |
| batch-rewrite (MySQL) | Eight independent INSERTs per cycle using multi-statement rewriting; verify actual strategy, guard query and eight complete OKs | Per-batch server merge-setting query, interpolation and multi-result consumption |
| batch-array-binding (Oracle) | Eight native bindings in an existing writable sql.Tx; verify complete flag24 packet, statement closure and per-item success with unknown counts | Prepare/execute/close per batch; transaction start/final rollback excluded |
| lob | Open, read and close a 16 KiB BLOB; compare every byte | MySQL table-BLOB dictionary/length/chunk queries; Oracle inline-locator materialization |
| fetch | Return 32 int64 values per cycle on the same cursor; verify 0–31 and Rows closure | Cursor prepare/execute/final close excluded; Oracle includes offset extension, metadata and trailing OK |

The LOB backends differ between modes, so their difference does not establish relative mode performance. Remote-locator streaming reads/writes are not covered. The fetch peer resends the same batch each time, measuring decoding/scanning only, not cross-batch cursor position or consistency. Batch rows/op=8 and fetch rows/op=32 are fixed workloads, not independently measured throughput. LOB MB/s uses a 16 KiB useful payload; fetch uses 32×8 bytes of useful values. Neither represents actual protocol bandwidth.

## 3. Reproduction and correctness checks

```sh
GOTOOLCHAIN=go1.26.0 go test . -run '^TestDriverBenchmarkFixtures$' -count=30
GOTOOLCHAIN=go1.26.0 go test . -run '^$' -bench '^BenchmarkDriver$' -benchmem -benchtime=100x -count=3
```

These commands use POSIX shell syntax. In PowerShell, set `$env:GOTOOLCHAIN='go1.26.0'` first, then run the corresponding go commands. No DSN, credentials or database is required. Ordinary `go test ./...` runs warmup and two operations per fixture as well, so CI checks return values and cleanup by default. Ordinary pushes do not collect performance results; PR CI runs a separate benchmark job and saves artifacts.

Each fixture has a two-minute total budget starting at connection setup. The peer remains alive through DB.Close; transport closure and waiting for its goroutine follow, with context cancellation last. Because testing.TB.Context is canceled before Cleanup, fixtures use WithoutCancel and impose their own budget. Long benchtime settings must fit this budget. See §6 for separate resource checks; extending this short single-connection benchmark does not establish acceptance.

Outside timing and before DB.Close, every fixture inspects the original physical session directly: no pool leases, released transaction/response/LOB/cursor resources, consistent statement-cache indexes, valid capacity/leases and type-description bytes within budget. It does not borrow a connection again, preventing ResetSession from hiding leaked state. After closure it checks zero pool counts, physical connection closure and empty cache/resource registries, and waits for each protocol peer to exit. Ordinary tests, benchmarks and the extended runs below share these checks.

## 4. Local samples

The original 16-scenario samples below were collected on 2026-10-17; the two additions are kept separate in §4.1. Environment: Windows/amd64, Intel Core i5-9400 CPU @ 2.90GHz, default GOMAXPROCS=6, Go 1.25.0 and Go 1.26.0. Each scenario ran 100 operations with three samples in one process. Each metric below is the median of its three samples; no significance analysis was performed. Operation/assertion definitions follow this document and the fixtures in the corresponding commit.

| Mode/scenario | Go | ns/op | B/op | allocs/op |
| --- | --- | --- | --- | --- |
| mysql-compatible/crud | 1.25.0 | 136,698 | 20,794 | 377 |
| mysql-compatible/prepare-per-call | 1.25.0 | 85,686 | 11,815 | 218 |
| mysql-compatible/prepare-reuse | 1.25.0 | 27,962 | 4,231 | 76 |
| mysql-compatible/cache-hit | 1.25.0 | 25,425 | 4,959 | 85 |
| mysql-compatible/batch-sequential | 1.25.0 | 683,746 | 93,005 | 1,733 |
| mysql-compatible/batch-multisend | 1.25.0 | 206,191 | 31,889 | 634 |
| mysql-compatible/lob | 1.25.0 | 326,405 | 108,900 | 841 |
| mysql-compatible/fetch | 1.25.0 | 278,536 | 42,609 | 842 |
| oracle-compatible/crud | 1.25.0 | 129,897 | 20,748 | 376 |
| oracle-compatible/prepare-per-call | 1.25.0 | 85,814 | 12,688 | 229 |
| oracle-compatible/prepare-reuse | 1.25.0 | 25,351 | 4,231 | 76 |
| oracle-compatible/cache-hit | 1.25.0 | 29,112 | 5,431 | 92 |
| oracle-compatible/batch-sequential | 1.25.0 | 695,274 | 100,046 | 1,829 |
| oracle-compatible/batch-multisend | 1.25.0 | 210,359 | 36,520 | 701 |
| oracle-compatible/lob | 1.25.0 | 95,775 | 99,273 | 191 |
| oracle-compatible/fetch | 1.25.0 | 320,905 | 47,359 | 939 |
| mysql-compatible/crud | 1.26.0 | 140,431 | 20,875 | 377 |
| mysql-compatible/prepare-per-call | 1.26.0 | 81,428 | 11,816 | 218 |
| mysql-compatible/prepare-reuse | 1.26.0 | 19,949 | 4,231 | 76 |
| mysql-compatible/cache-hit | 1.26.0 | 26,670 | 4,968 | 86 |
| mysql-compatible/batch-sequential | 1.26.0 | 696,520 | 93,030 | 1,733 |
| mysql-compatible/batch-multisend | 1.26.0 | 208,289 | 31,845 | 634 |
| mysql-compatible/lob | 1.26.0 | 352,196 | 108,741 | 838 |
| mysql-compatible/fetch | 1.26.0 | 278,709 | 42,582 | 842 |
| oracle-compatible/crud | 1.26.0 | 133,644 | 20,769 | 376 |
| oracle-compatible/prepare-per-call | 1.26.0 | 91,360 | 12,690 | 229 |
| oracle-compatible/prepare-reuse | 1.26.0 | 27,104 | 4,231 | 76 |
| oracle-compatible/cache-hit | 1.26.0 | 29,375 | 5,430 | 92 |
| oracle-compatible/batch-sequential | 1.26.0 | 713,457 | 100,065 | 1,829 |
| oracle-compatible/batch-multisend | 1.26.0 | 216,202 | 36,561 | 701 |
| oracle-compatible/lob | 1.26.0 | 102,149 | 99,270 | 190 |
| oracle-compatible/fetch | 1.26.0 | 319,728 | 47,362 | 939 |

### 4.1 Added native batch scenarios

Two supported-mode-specific scenarios were added on 2026-10-18; per-item fallback is not counted as an optimized path. Batch rewriting advertises multi-statement capability and confirms the server aggregation setting is disabled per batch. Array binding uses a known 4.3.5.1 peer, the original writable transaction and eight signed scalar inputs; final aggregate counts are not distributed across items. Both reuse original-session cleanup assertions and participate in ordinary fixtures and eight-round resource checks. Final rollback verifies lifecycle only, not actual database undo behavior.

On the same date, Windows/amd64, the same CPU and GOMAXPROCS=6, only these two scenarios ran with 100 operations and three samples each; each metric is its median. Each batch has eight rows. These are whole-batch costs including the in-process peer/assertions, not database throughput or evidence of toolchain performance differences.

| Mode/scenario | Go | ns/op | B/op | allocs/op |
| --- | --- | --- | --- | --- |
| mysql-compatible/batch-rewrite | 1.25.0 | 153,689 | 25,936 | 472 |
| oracle-compatible/batch-array-binding | 1.25.0 | 99,189 | 21,482 | 349 |
| mysql-compatible/batch-rewrite | 1.26.0 | 157,059 | 25,937 | 472 |
| oracle-compatible/batch-array-binding | 1.26.0 | 96,715 | 21,483 | 349 |

Reproduce with `go test . -run '^$' -bench '^BenchmarkDriver$/.*/batch-(rewrite|array-binding)$' -benchmem -benchtime=100x -count=3 -timeout 2m`; see §3 for toolchain selection. Both fixtures use 42 for all eight inputs and do not independently verify input order or differing values. The expected array packet reuses the production encoder; independent byte-format assertions are in `TestStmtArrayGolden`.

## 5. Outstanding verification

Live CRUD, caching, native batch optimization, streaming LOB/cursor performance in both modes over direct/OBProxy connections, network and multi-connection load, and long-term memory/handle/goroutine curves have not run. Actual Linux execution, remote CI and successful race evidence remain missing. D32 is not complete; implementation remains 28/32 and acceptance 0/32. See [development progress](go-driver-development-progress.md).

## 6. Offline resource stability checks

`TestDriverResourceStability` reuses the 18 scenarios, repeatedly creating, using and closing sessions over eight fixed rounds. Each scenario in each round warms up once, then executes N operations; N defaults to 2 in ordinary tests and CI. Set `OB_TEST_STABILITY_ITERATIONS` to N in 1–10000 to increase load without changing the sample/test-tree size or accessing DSNs/databases. Each scenario retains an independent two-minute budget; give the whole command a sufficient timeout.

```sh
OB_TEST_STABILITY_ITERATIONS=4096 GOTOOLCHAIN=go1.26.0 go test . -run '^TestDriverResourceStability$' -count=1 -v -timeout 10m
```

In PowerShell, set `$env:OB_TEST_STABILITY_ITERATIONS='4096'` and `$env:GOTOOLCHAIN` before the go command. Extended stability checks run locally, outside routine CI, without integration or database secrets.

Each scenario completes Cleanup before the next begins. After every round, an explicit GC precedes resource_sample records for elapsed_ms, HeapAlloc, HeapObjects and NumGoroutine. Sample 0 is an unloaded baseline; there are nine points total. Resource ownership, cache capacity and pool closure are hard assertions. Heap/goroutine values are process observations including testing's fixed tree, output, runtime caches and peer costs; they have no arbitrary thresholds and do not prove absence of leaks. This run covers repeated connection creation and basic serial paths, not one long-lived session or every extension/error/concurrency/framing combination. net.Pipe provides no real OS socket/file-handle curves. Database load and long production runs remain pending.

See §7 for a separate continuous mixed-operation check on the same physical session; it changes neither this section's session-recreation workload nor its historical samples.


### 6.1 Extended local samples

2026-10-17, Windows/amd64, Intel Core i5-9400, default GOMAXPROCS=6. Toolchains ran sequentially with N=4096, eight rounds and 16 scenarios each, without concurrent Go checks. All resource assertions passed. Go 1.25 took 111.101 seconds and Go 1.26 took 114.024 seconds. Including one warmup per scenario/round, each toolchain executed 524416 scenario operations. See §2 for SQL/rows per scenario; these are not SQL-throughput figures.

| Go | Sample | Elapsed ms | Post-GC heap bytes | Heap objects | Goroutines |
| --- | --- | --- | --- | --- | --- |
| 1.25.0 | 0 | 1 | 229344 | 699 | 2 |
| 1.25.0 | 1 | 13735 | 373072 | 1289 | 2 |
| 1.25.0 | 2 | 27610 | 392352 | 1330 | 2 |
| 1.25.0 | 3 | 41373 | 391536 | 1308 | 2 |
| 1.25.0 | 4 | 55074 | 403360 | 1406 | 2 |
| 1.25.0 | 5 | 68852 | 410400 | 1446 | 2 |
| 1.25.0 | 6 | 82795 | 409952 | 1422 | 2 |
| 1.25.0 | 7 | 96534 | 413936 | 1467 | 2 |
| 1.25.0 | 8 | 110498 | 437520 | 1521 | 2 |
| 1.26.0 | 0 | 0 | 274928 | 725 | 2 |
| 1.26.0 | 1 | 14618 | 411384 | 1243 | 2 |
| 1.26.0 | 2 | 28593 | 439336 | 1365 | 2 |
| 1.26.0 | 3 | 42697 | 442968 | 1386 | 2 |
| 1.26.0 | 4 | 56979 | 485928 | 1518 | 2 |
| 1.26.0 | 5 | 71207 | 482648 | 1474 | 2 |
| 1.26.0 | 6 | 85237 | 488440 | 1507 | 2 |
| 1.26.0 | 7 | 99199 | 495400 | 1581 | 2 |
| 1.26.0 | 8 | 113095 | 502216 | 1594 | 2 |

Every quiescent sample had two goroutines. Heap/object counts still grew after warmup in both versions, so these results cannot be described as zero growth or proof of no leaks. Samples include fixed test-framework metadata and runtime caches. The resource assertions directly establish cleanup after each scenario. Production stability requires longer runs, long-lived sessions, multiple connections, faults and real socket/OS-handle/database environments.

### 6.2 Eighteen-scenario samples including native batches

2026-10-18, Windows/amd64, the same CPU and GOMAXPROCS=6. Toolchains ran sequentially without other concurrent Go checks. Each used N=4096, eight rounds and 18 scenarios for 589968 scenario operations including warmup. All resource assertions passed. Go 1.25 took 119.070 seconds and Go 1.26 took 121.222 seconds. Do not compare directly with the 16-scenario workload in §6.1.

| Go | Sample | Elapsed ms | Post-GC heap bytes | Heap objects | Goroutines |
| --- | --- | --- | --- | --- | --- |
| 1.25.0 | 0 | 2 | 224352 | 694 | 2 |
| 1.25.0 | 1 | 14916 | 537736 | 1374 | 2 |
| 1.25.0 | 2 | 29808 | 518392 | 1486 | 2 |
| 1.25.0 | 3 | 44903 | 557928 | 1496 | 2 |
| 1.25.0 | 4 | 59745 | 596744 | 1491 | 2 |
| 1.25.0 | 5 | 74518 | 518152 | 1417 | 2 |
| 1.25.0 | 6 | 89326 | 563944 | 1513 | 2 |
| 1.25.0 | 7 | 104141 | 609416 | 1550 | 2 |
| 1.25.0 | 8 | 118984 | 542296 | 1621 | 2 |
| 1.26.0 | 0 | 0 | 274592 | 722 | 2 |
| 1.26.0 | 1 | 15017 | 625216 | 1391 | 2 |
| 1.26.0 | 2 | 30276 | 590464 | 1346 | 2 |
| 1.26.0 | 3 | 45374 | 605632 | 1454 | 2 |
| 1.26.0 | 4 | 60453 | 608360 | 1416 | 2 |
| 1.26.0 | 5 | 75699 | 609400 | 1411 | 2 |
| 1.26.0 | 6 | 90744 | 660968 | 1551 | 2 |
| 1.26.0 | 7 | 105871 | 620440 | 1476 | 2 |
| 1.26.0 | 8 | 121129 | 515512 | 1519 | 2 |

Every sample had two goroutines; heap/object counts exceeded the unloaded baseline and fluctuated. This is evidence of finite load and per-scenario cleanup only, not proof of no leaks or a substitute for §5's live-platform, multi-connection, fault and long-term acceptance.

## 7. Mixed resource checks on one physical session

`TestDriverSessionStability` continuously reuses one physical connection in each of six combinations: two modes × standard/compression/OB20 framing. Each combination owns a sql.DB with MaxOpenConns=1 and allows only one dial. State checks do not reborrow on every round, preventing reset from hiding residual state.

Each round executes, in order:

1. Two cached prepared UPDATEs, with the current positive round number and its negative; exactly match the initial/reused type headers and each affected count.
2. A text SELECT returning three rows, alternately closed before reading or after one row; require draining the remaining response and closing the original cached statement.
3. A standard sql.Tx UPDATE, alternating commit/rollback; Oracle must also restore autocommit without relying on pool reset.
4. A newly prepared UPDATE returning explicit SQL error 1062/23000; close the failed handle, then successfully Ping the same session.

Each round directly checks the original physical session's transaction, response, pinned resources, statement cache and pool leases. Closure checks zero pool counts, physical-session closure and peer exit. Exact protocol ordering also constrains statement release and prevents replay after errors. These transaction/results are in-process peer responses and do not prove real SQL, rollback or locking effects.

The default is eight rounds per combination in ordinary tests/automatic CI. OB_TEST_STABILITY_ITERATIONS sets the total rounds per combination (1–10000), unlike §6's iterations per scenario per sampling round; 4096 means 24576 mixed cycles total. Each combination has a two-minute budget. Reduce iterations if load or a slow machine exceeds it; timeout is not performance acceptance.

```sh
OB_TEST_STABILITY_ITERATIONS=4096 GOTOOLCHAIN=go1.26.0 go test . -run '^TestDriverSessionStability$' -count=1 -v -timeout 10m
```

See §6 for PowerShell environment syntax. For extended local checks, run §6/§7 with 4096 iterations, then §8 with 1024 rounds per connection, without database secrets. Each combination creates only a fixed test tree and at most nine post-GC samples, recording cycles/iterations, elapsed_ms, HeapAlloc, HeapObjects, NumGoroutine and dials. Sample 0 already has an idle physical connection and a peer waiting for commands; it is not directly comparable with §6's unloaded, closed-session baseline.

These process samples impose no arbitrary memory threshold and cannot establish zero leaks. See §8 for three-connection load. Mixed concurrent complex types/LOBs/cursors, transport faults, real OS sockets/handles and long production curves remain pending. This check does not increase the 18-scenario benchmark count.

### 7.1 Extended local samples

2026-10-18, Windows/amd64, the same local environment. Toolchains ran sequentially without other concurrent Go checks: 4096 rounds per combination, 24576 per version, all assertions passed. Complete test duration was 85.434 seconds on Go 1.25 and 101.237 seconds on Go 1.26. The table records each combination's loop/sampling time, excluding initialization/final closure, and establishes no relative toolchain/framing performance.

Each combination has nine post-GC observations (0, 512, …, 4096 rounds). Sampled maximum heap is only the maximum of these nine points, not the process peak. Baseline/final values retain the same idle physical connection and waiting peer. Later combinations include accumulated framework metadata in the same process; cross-row comparisons do not establish allocation/leak differences.

| Go | Mode/framing | Loop ms | Initial/final heap bytes | Sampled max heap bytes | Initial/final objects | Goroutines | Dials |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1.25.0 | mysql-compatible/standard | 1582 | 242544 / 371648 | 371648 | 811 / 1364 | 5 | 1 |
| 1.25.0 | mysql-compatible/compression | 32499 | 373576 / 404432 | 414248 | 1391 / 1295 | 5 | 1 |
| 1.25.0 | mysql-compatible/ob20 | 2056 | 408720 / 414208 | 425160 | 1328 / 1406 | 5 | 1 |
| 1.25.0 | oracle-compatible/standard | 1767 | 417088 / 428528 | 428528 | 1414 / 1550 | 5 | 1 |
| 1.25.0 | oracle-compatible/compression | 44360 | 428592 / 417856 | 428592 | 1547 / 1417 | 5 | 1 |
| 1.25.0 | oracle-compatible/ob20 | 2298 | 423440 / 418672 | 437048 | 1464 / 1430 | 5 | 1 |
| 1.26.0 | mysql-compatible/standard | 1598 | 290208 / 432440 | 432752 | 857 / 1386 | 5 | 1 |
| 1.26.0 | mysql-compatible/compression | 42913 | 434376 / 452600 | 459856 | 1410 / 1356 | 5 | 1 |
| 1.26.0 | mysql-compatible/ob20 | 2085 | 456776 / 467160 | 474256 | 1374 / 1485 | 5 | 1 |
| 1.26.0 | oracle-compatible/standard | 1771 | 471400 / 471320 | 477104 | 1498 / 1514 | 5 | 1 |
| 1.26.0 | oracle-compatible/compression | 50424 | 474336 / 451984 | 474336 | 1552 / 1295 | 5 | 1 |
| 1.26.0 | oracle-compatible/ob20 | 2340 | 458400 / 467872 | 477032 | 1341 / 1445 | 5 | 1 |

Every sample had five goroutines and one dial. Heap/object counts grew and fluctuated. Per-round resource assertions and protocol teardown are direct evidence; finite samples still do not prove leak freedom or long-term production stability.

## 8. Concurrent mixed load on three pooled connections

`TestDriverConcurrentSessionStability` pins three physical connections in one Connector/sql.DB across MySQL/Oracle modes and standard/compression/OB20 framing. Each continuously runs §7's cached preparation/type reuse, early Rows closure, alternating commit/rollback, expected SQL errors and Ping, with distinct statement IDs and parameter values. In the first round of every sampling segment, peers wait until all three connections receive execute requests before returning the first OK to any connection, directly verifying concurrent progress. Clients use ordinary goroutines, return errors and wait collectively, independently of go test -parallel subtest limits.

Each round locks and inspects the original physical connections for transaction/response/resources, cache indexes/leases and descriptor budgets. Each segment checks pool counts open=3/inUse=3/idle=0. After all loops, release the leases and check idle=3, then reborrow all three simultaneously, verifying distinct original sessions and successful Pings. After pool closure, check open/inUse/idle=0 and all original connections closed. Peers must receive QUIT and transport closure (EOF or net.Pipe ErrClosedPipe). Before cleanup, the main test waits up to five seconds for every peer confirmation; timeout/cancellation does not count as successful closure. Checks precede reborrow/reset so pool reset cannot hide residual state.

The default is eight rounds per connection, 24 per combination. OB_TEST_STABILITY_ITERATIONS selects rounds per connection (1–10000). Runs split into at most eight segments, retaining at most nine post-GC process samples for completed per-connection rounds, connection count, elapsed time, heap/objects, goroutines and dials. Worker goroutines have exited at sampling time, while all three leases remain pinned. Each combination has a two-minute budget; extended local runs use 1024 rounds per connection rather than applying the single-connection 4096 budget to triple load.

```sh
OB_TEST_STABILITY_ITERATIONS=1024 GOTOOLCHAIN=go1.26.0 go test . -run '^TestDriverConcurrentSessionStability$' -count=1 -v -timeout 5m
```

This provides local protocol-peer evidence of three-connection concurrency and return to the pool. It does not replace real databases, successful race detection, other target platforms, arbitrary connection counts or advanced-resource/fault combinations, and does not increase the 18-scenario benchmark count. Process samples are finite observations without arbitrary heap thresholds and do not establish absence of leaks.

### 8.1 Extended local samples

2026-10-18, Windows/amd64. Toolchains ran sequentially without concurrent Go checks during sampling. Each used 1024 rounds per connection, 3072 per combination and 18432 mixed cycles per toolchain; all assertions passed. Complete tests took 31.429 seconds on Go 1.25 and 28.884 seconds on Go 1.26. The table records loop/sampling time excluding initialization/final closure, not comparative toolchain/framing performance.

Each combination has nine post-GC observations (0, 128, …, 1024 rounds per connection), retaining three leases and waiting peers. Sampled maximum heap is only the largest of these nine values, not the process peak. Combinations share a process; later ones include earlier framework metadata, so cross-row comparisons cannot establish allocation/leak differences.

| Go | Mode/framing | Loop ms | Initial/final heap bytes | Sampled max heap bytes | Initial/final objects | Goroutines | Dials |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1.25.0 | mysql/standard | 339 | 265376 / 380736 | 389280 | 968 / 1465 | 7 | 3 |
| 1.25.0 | mysql/compression | 13367 | 385744 / 411632 | 421520 | 1538 / 1543 | 7 | 3 |
| 1.25.0 | mysql/ob20 | 433 | 418528 / 420944 | 430032 | 1627 / 1652 | 7 | 3 |
| 1.25.0 | oracle/standard | 398 | 428512 / 424656 | 438800 | 1722 / 1671 | 7 | 3 |
| 1.25.0 | oracle/compression | 16271 | 429904 / 431200 | 439008 | 1747 / 1728 | 7 | 3 |
| 1.25.0 | oracle/ob20 | 488 | 437200 / 442064 | 444128 | 1802 / 1852 | 7 | 3 |
| 1.26.0 | mysql/standard | 336 | 304944 / 442200 | 442200 | 974 / 1553 | 7 | 3 |
| 1.26.0 | mysql/compression | 12410 | 445384 / 483760 | 483760 | 1598 / 1682 | 7 | 3 |
| 1.26.0 | mysql/ob20 | 411 | 483320 / 492624 | 493920 | 1714 / 1782 | 7 | 3 |
| 1.26.0 | oracle/standard | 376 | 498848 / 500864 | 505136 | 1820 / 1844 | 7 | 3 |
| 1.26.0 | oracle/compression | 14728 | 505448 / 488352 | 505448 | 1914 / 1720 | 7 | 3 |
| 1.26.0 | oracle/ob20 | 474 | 495016 / 505568 | 505568 | 1811 / 1901 | 7 | 3 |

All 108 samples observed seven goroutines and three dials. Per-round original-session assertions, reborrowing every original connection and peer-close confirmation before cleanup are direct evidence. These finite process samples still do not establish long-term production stability or leak freedom.
