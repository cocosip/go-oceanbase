# go-oceanbase

面向 OceanBase 的 Go `database/sql` 驱动，统一处理 MySQL、Oracle 两种兼容模式。普通查询、事务和预处理使用 Go 标准库；LOB、游标、批量及 XA 等扩展由同一个 `oceanbase` 包提供。

**开发中，尚未完成真实数据库兼容验收。** 当前实现与未闭环能力以[开发进度](docs/go-driver-development-progress.md)和[兼容矩阵](docs/go-driver-compatibility-matrix.md)为准，不能据此认定已具备完整生产可用性。

最低 Go **1.25**，同时验证 **1.26**。核心协议使用 Go 实现；可选系统认证和平台传输的要求见[架构设计](docs/go-driver-architecture-design.md)。

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
