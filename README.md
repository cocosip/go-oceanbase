# go-oceanbase

A Go `database/sql` driver for OceanBase in MySQL and Oracle compatibility modes. Use the standard library for queries, prepared statements and transactions, and the same `oceanbase` package for batch execution, LOBs, cursors and XA.

**Status: under development.** Live database compatibility acceptance is not complete. See the [compatibility matrix](docs/go-driver-compatibility-matrix.md) and [development progress](docs/go-driver-development-progress.md) for implemented behavior, verification evidence and remaining work.

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

For Oracle compatibility mode, use `ModeOracle` and `SELECT ? FROM dual`. `ModeAuto` detects and pins the mode on each authenticated physical connection; it does not translate SQL dialects.

### DSN configuration

`sql.Open("oceanbase", dsn)` also supports URL-style connection strings:

```text
oceanbase://app%40tenant:password@127.0.0.1:2881/demo?mode=mysql
oceanbase://app%40tenant:password@127.0.0.1:2881/?mode=oracle
```

These are placeholders. URL-escape credentials (`%40` represents `@`), or use `Config` to avoid constructing credential-bearing URLs. DSNs reject unknown options; JDBC settings are not interchangeable with Go settings. See the [usage guide](docs/go-driver-usage.md) and [option mapping](docs/go-driver-options-mapping.md) for supported configuration.

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

## Documentation

| Document | Contents |
| --- | --- |
| [Usage guide](docs/go-driver-usage.md) | Configuration, TLS, queries, ownership, errors and examples |
| [Compatibility matrix](docs/go-driver-compatibility-matrix.md) | Feature support, conditions and verification status |
| [Option mapping](docs/go-driver-options-mapping.md) | Java-to-Go configuration mapping and migration differences |
| [Architecture](docs/go-driver-architecture-design.md) | Public contracts, protocol design and package boundaries |
| [Connector/J outline](docs/obconnector-j-architecture-outline.md) | Reference Java driver structure |
| [Development plan](docs/go-driver-development-plan.md) | D01–D32 scope and delivery stages |
| [Development progress](docs/go-driver-development-progress.md) | Implementation history and verification evidence |
| [Benchmarks](docs/go-driver-benchmarks.md) | Local protocol-peer scenarios, commands, samples and limitations |
| [Acceptance tool](docs/go-driver-test-tool.md) | Configure and run the standalone `obtest` database suite |

## Examples and database acceptance

- [Comprehensive example](examples/usage/main.go): queries, transactions, prepared statements, batch execution, procedures, multiple results, LOBs, complex types and cursors.
- [XA example](examples/xa/main.go): start, end and roll back a branch on a pinned session.
- [Standalone acceptance tool](docs/go-driver-test-tool.md): 44 configured scenarios with fixture creation, cleanup and JSON reporting. Build with `go build -o bin/obtest ./cmd/obtest` (use `bin/obtest.exe` on Windows).

The examples and acceptance suite require an explicitly configured test database. The comprehensive example creates, modifies and drops temporary tables, procedures and types. Read the usage guide before running it. Compilation and offline tests do not establish live database compatibility.

## Development checks

```sh
go build ./...
go test ./... -count=1
go vet ./...
golangci-lint run
go test -tags=integration -run '^$' ./tests/integration
go run ./cmd/obtest -list
```

The integration command compiles the database tests without running them. `obtest -list` lists scenarios without connecting. See the [acceptance tool guide](docs/go-driver-test-tool.md) for live runs and the [benchmark guide](docs/go-driver-benchmarks.md) for offline performance measurements.

### GitHub Actions

[CI](.github/workflows/ci.yml) runs on pushes to `master`, `main` and `develop`, on pull requests and on manual dispatch:

- Go 1.26 builds, vet, integration-test compilation and coverage tests on Linux, Windows and macOS. Tests use `CGO_ENABLED=0`; race detection requires a separate cgo-enabled run.
- Lint on Linux and Windows, including integration-tagged sources. Linux also checks formatting, module files and workflow syntax.
- Linux/386 tests, 22 fuzz targets in four parallel groups (30 seconds per target), and separate Linux GSSAPI/OBProxy system and ABI fixtures without a database.
- Protocol-peer benchmarks on pull requests and manual runs, saved for comparison without a shared-runner performance threshold.
- Optional manual `integration` and `stability` jobs, both disabled by default. Integration requires explicit database secrets and `OB_TEST_REQUIRE=1`; automatic push/PR checks do not read database credentials.

Coverage and fuzz artifacts are retained for 14 days; benchmark artifacts for 30 days. Superseded automatic runs are canceled, while manual CI runs are not. [CodeQL](.github/workflows/codeql.yml) scans on push/PR, weekly and on manual dispatch; [Dependabot](.github/dependabot.yml) checks Go modules and Actions weekly. Dependency updates must retain the Go 1.26 minimum.

Workflow definitions and local results are not evidence of successful remote execution. Live OceanBase/OBProxy acceptance, platform coverage and race results remain subject to the evidence recorded in development progress.
