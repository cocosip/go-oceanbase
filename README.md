# go-oceanbase

A Go `database/sql` driver for OceanBase in MySQL and Oracle compatibility modes. Use the standard library for queries, prepared statements and transactions, and the same `oceanbase` package for batch execution, LOBs, cursors and XA.

**Status: release candidate.** Direct-target acceptance is complete for OceanBase 4.4.2.2 in both compatibility modes: the 72-scenario acceptance tool reached **114 pass / 0 fail / 3 skip / 27 not applicable**, the full integration suite passed, and both-mode `ChangeUser` passed with second identities. TLS, compression, OBProxy/multi-node, plugin/KDC and Linux-client combinations require deployments the current endpoints cannot provide and are recorded as unverified boundaries, not client gaps. See the [release-readiness assessment](docs/go-driver-completeness.md#发布就绪评估release-readiness) and the [compatibility matrix](docs/go-driver-compatibility-matrix.md).

The [2026-10-20 release validation](docs/live-validation.md) records passing direct MySQL/Oracle 4.4.2.2 scenarios, official Connector/J 2.4.14 source alignment, and deployment validation boundaries. Licensed under [MIT](LICENSE).

## Requirements

- Go **1.26 or later**.
- An OceanBase endpoint in MySQL or Oracle compatibility mode, reached directly or through OBProxy.
- The core driver builds without cgo. Optional system authentication and platform transports have additional requirements described in the [architecture](docs/go-driver-architecture-design.md).

## Installation

```sh
go get github.com/cocosip/go-oceanbase
```

## Quick start

Set `OB_ADDR` to your endpoint, such as `127.0.0.1:2881`, and supply `OB_USER`, `OB_PASSWORD` and `OB_DATABASE` through your application environment. This example uses MySQL compatibility mode:

```go
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"time"

	"github.com/cocosip/go-oceanbase"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	connector, err := oceanbase.NewConnector(oceanbase.Config{
		Addr:     os.Getenv("OB_ADDR"),
		User:     os.Getenv("OB_USER"),
		Password: os.Getenv("OB_PASSWORD"),
		Database: os.Getenv("OB_DATABASE"),
		Mode:     oceanbase.ModeMySQL,
		Logger:   logger,
	})
	if err != nil {
		panic(err)
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		panic(err)
	}
	var answer int
	if err := db.QueryRowContext(ctx, "SELECT ?", 42).Scan(&answer); err != nil {
		panic(err)
	}
	logger.Info("query completed", "answer", answer)
}
```

`NewConnector` validates and snapshots configuration; `PingContext` establishes a connection. A named import registers the `oceanbase` driver, so no additional blank import is needed. Reuse the `*sql.DB` pool and configure its connection limits for your deployment. A nil `Logger` keeps the driver silent.

Applications can also import [`config`](config), [`types`](types), [`dberrors`](dberrors), [`metadata`](metadata), and [`obproxy`](obproxy) directly for their public models and helpers. The root package retains aliases for those contracts, so existing `oceanbase.Config`, value, error, and metadata names remain usable. Driver registration and connection-bound operations stay in the root package; `internal` packages are implementation details.

For Oracle compatibility mode, use `ModeOracle` and `SELECT ? FROM dual`. `ModeAuto` detects and pins the mode on each authenticated physical connection; it does not translate SQL dialects.

### DSN configuration

`sql.Open("oceanbase", dsn)` also supports URL-style connection strings:

```text
oceanbase://app%40tenant:password@127.0.0.1:2881/demo?mode=mysql
oceanbase://app%40tenant:password@127.0.0.1:2881/?mode=oracle
```

These are placeholders. URL-escape credentials (`%40` represents `@`), or use `Config` to avoid constructing credential-bearing URLs. Supported DSN option names ignore case; duplicate names after case folding are rejected, while values keep their existing semantics. DSNs reject unknown options and option names borrowed from other drivers. See the [usage guide](docs/go-driver-usage.md) for supported configuration.

Configure `TLSConfig` with trusted CAs and a server name when encryption is required; leaving it nil does not enable TLS. Use a context for each operation. Network timeouts or an uncertain commit outcome do not make writes safe to replay.

## Capabilities

| Area | APIs and behavior |
| --- | --- |
| Standard SQL | `database/sql` queries, prepared statements, transactions and multiple result sets |
| Parameters and types | Positional parameters in both modes, Oracle named parameters, OUT/INOUT, exact numeric and temporal types |
| Batch execution | `ExecBatch` with per-input status and the actual execution strategy |
| Large and complex values | Table BLOB access in both modes; Oracle native LOBs, objects and collections |
| Cursors and metadata | Explicit cursor resources and metadata queries through the public root package |
| Distributed transactions | XA branch operations on a pinned session; the application supplies the durable coordinator |
| Connectivity | TLS, caller-supplied credentials and logging, routing/discovery and platform-specific transports |

Availability depends on compatibility mode, server version, negotiated capabilities and platform. The [compatibility matrix](docs/go-driver-compatibility-matrix.md) records those boundaries. Resource APIs require a pinned `*sql.Conn` or `*sql.Tx`; close rows and resources before releasing their session.

## Server-side unavailable combinations

These are deployment boundaries, not missing Go features. The driver reports them explicitly instead of simulating success. The [compatibility matrix §0](docs/go-driver-compatibility-matrix.md#0-server-side-unavailable-verification-release-boundary) records the exact evidence.

| Combination | Why it is unverified | Driver behavior |
| --- | --- | --- |
| TLS / mTLS | The endpoint reports `have_ssl=YES` but has no certificates configured | Plaintext unless `TLSConfig` is set; certificate and mTLS logic covered by local peer tests |
| Compression | The direct endpoint does not advertise `CLIENT_COMPRESS` | `compression=required` fails negotiation with an explicit unsupported-capability error |
| OBProxy, multi-node, OCP | Only a direct single-endpoint 2881 is available (2883 is not a usable protocol endpoint) | Direct-target behavior verified; proxy and topology paths unverified |
| Plugin / KDC | No vendor plugin runtime or KDC is deployed | No per-plugin support is claimed |
| B21 Oracle proxy reauthentication | 4.4.2.2 does not apply the proxy target on `COM_CHANGE_USER` | The driver rejects the proxy target on this version instead of reporting a false success |
| Linux client transports | The local client host is Windows without WSL or Docker | Core driver builds without cgo; Unix socket/shared memory/VC/GSSAPI paths unverified |

## Benchmarks

`BenchmarkDriver` covers 18 scenarios over one physical connection to an in-process `net.Pipe` protocol peer. It connects to no database, so **ns/op is not database latency**; timings include `database/sql`, the driver, peer response construction, protocol I/O and result assertions. The full methodology, all samples and resource checks are in the [benchmark guide](docs/go-driver-benchmarks.md).

Go 1.26.0, Windows/amd64, Intel Core i5-9400 @ 2.90GHz, GOMAXPROCS=6, 100 operations × 3 samples, median:

| Scenario | MySQL ns/op | Oracle ns/op |
| --- | --- | --- |
| crud | 140,431 | 133,644 |
| prepare-per-call | 81,428 | 91,360 |
| prepare-reuse | 19,949 | 27,104 |
| cache-hit | 26,670 | 29,375 |
| batch-multisend (8 rows) | 208,289 | 216,202 |
| batch-rewrite / array-binding (8 rows) | 157,059 | 96,715 |
| lob (16 KiB BLOB) | 352,196 | 102,149 |
| fetch (32 values) | 278,709 | 319,728 |

Reproduce with:

```sh
GOTOOLCHAIN=go1.26.0 go test . -run '^$' -bench '^BenchmarkDriver$' -benchmem -benchtime=100x -count=3
```

Performance pass/fail thresholds are intentionally not defined. Live database throughput, TLS/compression and multi-connection load are not covered by these samples.

## Documentation

| Document | Contents |
| --- | --- |
| [Usage guide](docs/go-driver-usage.md) | Configuration, TLS, queries, ownership, errors and examples |
| [Capability matrix](docs/go-driver-capabilities.md) | Current Go driver capabilities and support boundaries |
| [Feature gaps and limitations](docs/go-driver-completeness.md) | Driver gaps separated from server, permission, environment and verification limits |
| [Compatibility matrix](docs/go-driver-compatibility-matrix.md) | Feature support, conditions and verification status |
| [Architecture](docs/go-driver-architecture-design.md) | Public contracts, protocol design and package boundaries |
| [Development plan](docs/go-driver-development-plan.md) | D01–D32 scope and delivery stages |
| [Development progress](docs/go-driver-development-progress.md) | Implementation history and verification evidence |
| [Benchmarks](docs/go-driver-benchmarks.md) | Local protocol-peer scenarios, commands, samples and limitations |
| [Live validation record](docs/live-validation.md) | Merged live-database evidence, current baseline and dated results |
| [Acceptance tool](docs/go-driver-test-tool.md) | Configure and run the standalone `obtest` database suite |

## Examples and database acceptance

- [Comprehensive example](examples/usage/main.go): queries, transactions, prepared statements, batch execution, procedures, multiple results, LOBs, complex types and cursors.
- [XA example](examples/xa/main.go): start, end and roll back a branch on a pinned session.
- [Standalone acceptance tool](docs/go-driver-test-tool.md): 72 configured scenarios with fixture creation, cleanup and JSON reporting. Build with `go build -o bin/obtest ./cmd/obtest` (use `bin/obtest.exe` on Windows).

The examples and acceptance suite require an explicitly configured test database. The comprehensive example creates, modifies and drops temporary tables, procedures and types. Read the usage guide before running it. Compilation and offline tests do not establish live database compatibility.

## Development checks

```sh
go build ./...
go test ./... -count=1
go vet ./...
golangci-lint run
go test -tags=integration ./... -run '^$' -timeout 1m
go run ./cmd/obtest -list
```

The integration command compiles the database tests without running them. `obtest -list` lists scenarios without connecting. See the [acceptance tool guide](docs/go-driver-test-tool.md) for live runs and the [benchmark guide](docs/go-driver-benchmarks.md) for offline performance measurements.

### GitHub Actions

[CI](.github/workflows/ci.yml) runs on pushes to `master`, `main` and `develop`, on `v*` tag pushes, on pull requests and on manual dispatch:

- Go 1.26 unit tests on Linux, Windows and macOS with `CGO_ENABLED=0`. Linux also builds all packages and examples, compiles integration tests without a database, and collects coverage.
- One Linux lint pass covering ordinary and integration-tagged sources, including vet through the existing linter configuration. Formatting, module files and workflow syntax are checked in the same job.
- Protocol-peer benchmarks on pull requests, saved for comparison without a shared-runner performance threshold.

Coverage artifacts are retained for 14 days; benchmark artifacts for 30 days. Superseded automatic runs are canceled, while manual CI runs are not. [CodeQL](.github/workflows/codeql.yml) scans on push/PR, weekly and on manual dispatch; [Dependabot](.github/dependabot.yml) checks Go modules and Actions weekly. Dependency updates must retain the Go 1.26 minimum.

On a `v*` tag push, CI calls the reusable [release workflow](.github/workflows/release.yml) only after all platform tests and lint succeed. Publishing generates release notes and creates a source release without rerunning builds or tests. CodeQL remains an independent workflow and is not a release dependency.

Dedicated fuzz campaigns, native GSSAPI/OBProxy ABI fixtures, Linux/386 checks, extended stability runs and live database acceptance run outside routine CI. Their tests and local commands remain available; ordinary unit tests still run fuzz seeds and the default resource checks. Live integration requires explicit database configuration and `OB_TEST_REQUIRE=1`; routine CI does not read database credentials. Race detection requires a separate cgo-enabled run.

Workflow definitions and local results are not evidence of successful remote execution. Live OceanBase/OBProxy acceptance, platform coverage and race results remain subject to the evidence recorded in development progress.
