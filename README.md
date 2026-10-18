# go-oceanbase

面向 OceanBase 的 Go `database/sql` 驱动，统一处理 MySQL、Oracle 两种兼容模式。普通查询、事务和预处理使用 Go 标准库；LOB、游标、批量及 XA 等扩展由同一个 `oceanbase` 包提供。

**开发中，尚未完成真实数据库兼容验收。** 当前实现与未闭环能力以[开发进度](docs/go-driver-development-progress.md)和[兼容矩阵](docs/go-driver-compatibility-matrix.md)为准，不能据此认定已具备完整生产可用性。

最低 Go **1.26**。核心协议使用 Go 实现；可选系统认证和平台传输的要求见[架构设计](docs/go-driver-architecture-design.md)。

## 接入

在应用的 Go module 中添加依赖：

```sh
go get github.com/cocosip/go-oceanbase
```

下面通过 `Config` 建立 MySQL 兼容模式连接。`NewConnector` 只校验、快照配置，`PingContext` 才实际建连。密码由应用提供，日志通过外部 `*slog.Logger` 注入；不设置 `Logger` 时驱动静默。

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
        Addr: os.Getenv("OB_ADDR"), User: os.Getenv("OB_USER"),
        Password: os.Getenv("OB_PASSWORD"), Database: os.Getenv("OB_DATABASE"),
        Mode: oceanbase.ModeMySQL, Logger: logger,
    })
    if err != nil { panic(err) }
    db := sql.OpenDB(connector)
    defer func() { _ = db.Close() }()
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    if err := db.PingContext(ctx); err != nil { panic(err) }
    var answer int
    if err := db.QueryRowContext(ctx, "SELECT ?", 42).Scan(&answer); err != nil {
        panic(err)
    }
    logger.Info("query completed", "answer", answer)
}
```

Oracle 兼容模式使用 `ModeOracle`，示例查询为 `SELECT ? FROM dual`。`ModeAuto` 在每条物理连接认证后识别并固定模式；它不会翻译业务 SQL。TLS、DSN 转义、精确数值、资源关闭和错误处理见[使用指南](docs/go-driver-usage.md)。

## 文档与示例

- [使用指南](docs/go-driver-usage.md)：配置、调用约定、可运行示例及当前限制。
- [Java 驱动结构大纲](docs/obconnector-j-architecture-outline.md)、[Go 架构](docs/go-driver-architecture-design.md)：功能与包边界。
- [开发计划](docs/go-driver-development-plan.md)、[开发进度](docs/go-driver-development-progress.md)：D01–D32 与验证证据。
- [配置映射](docs/go-driver-options-mapping.md)、[兼容矩阵](docs/go-driver-compatibility-matrix.md)：逐项适配状态。
- [本地基准](docs/go-driver-benchmarks.md)：两模式的场景、复现命令、样本与适用边界。
- [独立数据库验收工具](docs/go-driver-test-tool.md)：`go build -o bin/obtest.exe ./cmd/obtest`，配置 DSN 后自动建表/准备数据、执行 44 个场景、清理并输出 JSON 报告。
- [综合示例](examples/usage/main.go)：查询、事务、预处理、批量、过程、多结果、LOB、复杂类型和游标。
- [XA 示例](examples/xa/main.go)：固定会话上开启、结束并回滚分支。

两个程序都需要显式配置的测试数据库。综合示例会创建、修改并删除临时命名的表/过程/类型，运行前请阅读使用指南；本阶段仅编译，不连接数据库。

## 本地检查

```sh
go test ./... -count=1
go vet ./...
golangci-lint run
go test -tags=integration -run '^$' ./tests/integration
```

最后一条只编译集成测试。远程 CI、真实 OceanBase/OBProxy 联调、Linux 实际运行及成功的 race 验证仍需独立证据，不能用本地编译替代。

## GitHub Actions

CI 在 `master`、`main`、`develop` 的 push、所有 PR 和手动运行时执行。Go 1.26 在 Linux、Windows、macOS 构建、vet、编译集成测试源码，并以 `CGO_ENABLED=0` 运行覆盖率测试，持续验证零 cgo 构建；覆盖率文件保存为独立 artifact。驱动为纯 Go 且不导入 `C`，而 `-race` 按工具链设计强制 cgo，故 CI 不启用 race，竞态检测需在 cgo 环境手动执行。Linux/Windows 的 lint 独立执行，同时检查 integration 标签；Linux 额外检查格式、module 文件和工作流语法。

协议检查保留 Go 1.26 Linux/386 实际测试，22 个 fuzz 目标分成四组并行，每个目标精确匹配、运行 30 秒，保存日志与回归语料。Linux GSSAPI/OBProxy 的系统与 ABI 夹具独立运行并检查标签 lint；这些夹具不连接数据库。PR 和手动 CI 还会运行 Go 1.26 本地协议对端基准并保存结果，供人工比较，不设置依赖共享 runner 性能的硬阈值。

手动 CI 的 `integration`、`stability` 默认关闭，前者仍要求显式配置测试库 secrets 与 `OB_TEST_REQUIRE=1`，后者运行扩展离线资源检查。自动 push/PR 检查不读取数据库凭据。重复的自动运行会取消旧任务，手动运行不自动取消。CodeQL 在 push/PR、每周和手动入口扫描 Go；Dependabot 每周检查 Go modules 和 Actions 更新。更新 PR 仍需通过 Go 1.26 检查，不得无意提高最低 Go 版本。

覆盖率和 fuzz 产物保留 14 天，基准产物保留 30 天。工作流定义与本地验证不等于远程执行成功；平台、零 cgo 构建、安全扫描与真实数据库的验收状态继续以开发进度中的实际证据为准。
