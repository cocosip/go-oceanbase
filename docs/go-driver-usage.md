# OceanBase Go 驱动使用指南

本文描述当前公开 API 的用法。实现状态见[开发进度](go-driver-development-progress.md)，目标限制见[兼容矩阵](go-driver-compatibility-matrix.md)。Go 最低版本为 1.25，同时验证 1.26；本阶段暂不连接真实数据库，下列联网示例只有编译证据。

## 1. 建连、兼容模式与日志

只需引入 `github.com/cocosip/go-oceanbase`。命名导入即可完成 `database/sql` 的 `oceanbase` 驱动注册，无需重复空白导入。普通业务直接使用 `*sql.DB`、`*sql.Tx`、`*sql.Stmt`、`*sql.Rows`。

- `NewConnector(Config)` 校验并快照配置，不联网；再用 `sql.OpenDB(connector)` 建立池。
- `sql.Open("oceanbase", dsn)` 是字符串配置入口，同样延迟建连。使用带超时的 `PingContext` 检查实际连接。
- `ModeMySQL`、`ModeOracle` 是 OceanBase 的兼容模式；显式模式与实际模式不符会拒绝建连。`ModeAuto` 认证后在每条物理连接上固定实际模式，不执行 SQL 方言转换。
- MySQL 的 `Database` 表示初始数据库；Oracle 的初始 schema 规则、引号和大小写见[架构 §6](go-driver-architecture-design.md#6-配置模型和默认值)。不要把 Oracle schema 当成 MySQL 数据库操作。

DSN 使用 URL 格式，例如下面的占位配置，其中 `%40` 是用户名内 `@` 的转义：

```text
oceanbase://app%40tenant:password@127.0.0.1:2881/demo?mode=mysql
oceanbase://app%40tenant:password@127.0.0.1:2881/?mode=oracle
```

密码中的 `@`、`#`、`/`、`?` 等必须按 URL 转义。应用优先使用 `Config`，或通过 `net/url.UserPassword` 构造 URL，不手拼含密码的 URL。DSN 不接受任意 JDBC 选项和未知键，实际支持项见[配置映射](go-driver-options-mapping.md)。不打印 DSN 或完整配置。

日志只由调用方注入，`nil` 表示静默；驱动不调用 `slog.Default`、不注册全局 logger、不写日志文件：

```go
logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelWarn,
}))
cfg.Logger = logger
```

应用设置 `db.SetMaxOpenConns`、`SetMaxIdleConns`、`SetConnMaxLifetime` 等标准池参数。每次调用传入业务 `context`，不要用共享的无限期 context 替代调用超时。连接失效或提交失败时，应先判断业务状态，不能无条件重放非幂等写入。

如需独立限制网络读取等待，可设置 `Config.ReadTimeout = 5 * time.Second` 或 DSN `readTimeout=5s`；默认0关闭，两种兼容模式都适用。它按每次底层 Read 重新计时，context 的较早期限仍生效；持续收到数据的长结果仍需业务 context 限制总时长。超时会关闭连接并保留网络错误（errors.As 到 net.Error 检查 Timeout），不自动重放已写出的业务。详见[架构 §12.5.1](go-driver-architecture-design.md#1251-d32-已实现的网络读取超时)。

## 2. TLS 与认证配置

需要 TLS 的应用通过 `Config.TLSConfig` 传入 `*tls.Config`，设置可信 CA 和服务端名称。下面假设 `caPEM` 已由应用读入：

```go
roots := x509.NewCertPool()
if !roots.AppendCertsFromPEM(caPEM) {
    return errors.New("no valid CA certificate")
}
cfg.TLSConfig = &tls.Config{
    MinVersion: tls.VersionTLS12,
    RootCAs: roots,
    ServerName: "oceanbase.example.com",
}
```

不以 `InsecureSkipVerify` 代替 CA/主机名校验。未设置 `TLSConfig` 不代表加密连接；按部署要求显式配置 TLS。认证插件、凭据回调及各平台要求见[架构设计](go-driver-architecture-design.md)。

双向 TLS 将 `tls.LoadX509KeyPair` 加载的客户端证书链和私钥放入 `Certificates`；动态选择使用 `GetClientCertificate`，回调应遵守 `CertificateRequestInfo.Context()`。完整[文件加载示例](../tls_example_test.go)会检查 CA、证书和私钥加载错误。JKS/PKCS12/加密私钥由应用或部署侧解锁转换，驱动不读取 Java keystore 密码，也不解析其 DSN 别名。TLS 的 MinVersion/MaxVersion、CipherSuites 与额外验证回调直接使用 Go 语义；CipherSuites 不控制 TLS1.3。默认与边界见[架构 §17.2.1](go-driver-architecture-design.md#1721-d32-tls-配置的-go-原生承载)。

TCP 源接口、keep-alive、缓冲和 NODELAY 使用标准 `net.Dialer` / `net.TCPConn` 配合 `Config.DialContext`；不需要额外驱动配置结构。例如 `cfg.DialContext = (&net.Dialer{KeepAlive: -1}).DialContext` 可禁用默认 TCP keep-alive，仍由驱动处理后续 TLS 与初始化。完整[可编译示例](../tcp_example_test.go)展示显式 LocalAddr、KeepAliveConfig 和 setter 错误清理；示例只在程序化配置中使用，Java 的 TCP DSN 参数不会生效。源 IP 必须属于本机接口；回调捕获的配置应不可变并支持并发调用。详见[架构 §9.0.2](go-driver-architecture-design.md#902-d32-tcp-配置的-go-原生承载)。

`Credentials` 回调返回的 `User`、`Password`、`ProxyUser` **完整替换**静态身份。Oracle 代理登录使用独立 `ProxyUser` 字段，认证用户和代理目标分开；不支持括号用户名语法，普通连接属性不能覆盖代理目标。结构体建议使用命名字段，避免新增字段破坏位置字面量。完整契约见[架构 §6.3](go-driver-architecture-design.md#63-d32-已实现的代理用户登录)。

凭据来源和固定 RSA 公钥使用现有配置，见[应用示例](../auth_example_test.go)：Credentials 按每次物理认证读取应用来源，出错不回退静态身份；示例要求 OB_USER/OB_PASSWORD 存在，允许显式空密码，OB_PROXY_USER 可选。来源回调必须响应 context 并支持并发，环境变量不是每连接隔离存储。Auth.RSAKey 接收应用已加载的预置 *rsa.PublicKey，公钥在 NewConnector 时复制，轮换时创建新 Connector；加载失败应直接返回。RSA 只保护 SHA 认证密码，不替代 TLS 的会话加密或服务器身份验证；不支持自动取服务端公钥。示例只解析单个 PKIX PUBLIC KEY，其他格式由应用使用对应标准解析器处理。默认及边界见[架构 §17.2.2](go-driver-architecture-design.md#1722-d32-凭据来源与固定-rsa-公钥)。

`Session.Collation` 只适用于 MySQL 兼容模式，且必须与 `Session.Charset` 匹配；Oracle 显式拒绝。`Interactive` 请求服务端交互超时策略，不是客户端保活。`ConnectionAttributes` 只能传应用元数据，不得注入协议保留键。

初始 MySQL 数据库需要由连接显式创建时，设置 `Config.CreateDatabaseIfMissing=true` 或 DSN `createDatabaseIfMissing=true`，并指定非空 Database。默认关闭，Oracle 拒绝。驱动认证后在同一连接执行 IF NOT EXISTS 并选库，随后才运行 InitSQL；需账户具备相应权限，初始化失败也可能已经留下数据库，不会自动删除或重试建库。池替换连接会再次创建/选择；ChangeUser 和取消查询辅助连接不执行创建。详见[架构 §6.4](go-driver-architecture-design.md#64-d32-已实现的初始数据库创建)。

## 3. 查询、参数与本地事务

查询优先用 `QueryRowContext` / `QueryContext`，写入用 `ExecContext`。两模式均可使用 `?`；Oracle 还可用 `:name` 与 `sql.Named`。占位符只绑定值，不能绑定表名、列名或关键字；动态标识符需要独立可信校验。不要把业务值拼入 SQL。

`PrepareMode` 默认为服务端预处理；需要客户端插值时显式选择 `PrepareClient`。普通参数执行仍通过同一标准库 API。`sql.Stmt` 关闭先于所属事务/连接关闭；不要跨会话保存服务端 statement ID。

```go
tx, err := db.BeginTx(ctx, nil)
if err != nil { return err }
defer func() { _ = tx.Rollback() }()
if _, err := tx.ExecContext(ctx, "UPDATE app_table SET v=? WHERE id=?", "new", 1); err != nil {
    return err
}
return tx.Commit()
```

事务内所有操作使用 `tx`，不能混用 `db` 查询来观察未提交数据。`Commit` 失败可能意味着结果未知；`Rollback` 清理也不能证明远端已经回滚。示例中的延迟回滚只处理尚未结束的事务。

金额及高精度数值使用 `Number` / `Decimal` 或字符串精确表示，不先转 `float64`。可空值使用标准 `sql.Null*`；Oracle `Object` / `Array` 用 `Null` 区分 SQL NULL，集合里的 `nil` 表示 NULL 元素。日期解析选项和时区应显式按业务选择，不假定所有日期自动返回 `time.Time`。

`Rows` 必须及时关闭，迭代后检查 `Rows.Err()`；多结果还要循环 `NextResultSet()`。`sql.RawBytes` 按标准库契约只在下一次行操作前有效，需长期持有时复制。

## 4. 扩展能力与资源所有权

扩展仍通过同一个公开包使用。资源类 API 的 `Session` 只接受固定会话 `*sql.Conn` 或 `*sql.Tx`；`*sql.DB` 不是固定会话，不能用于打开游标或 locator。

| 能力 | 入口 | 关键约定 |
| --- | --- | --- |
| 批量 | `ExecBatch(ctx, exec, query, rows, opts)` | `exec` 可为 DB/Conn/Tx；不会自动开启或提交事务；需要原子性时传 Tx |
| OUT/INOUT | `sql.Out`、`Out` | `Dest` 是非 nil 指针；INOUT 设 `In:true`；显式类型/长度用 `Out` |
| 多结果与计数 | `Execute`、`Rows()`、`Snapshot()` | 可同时返回部分 Execution 和 error；非 nil 对象始终要 Close；完整读取后再解释快照 |
| 表内 BLOB | `OpenTableBlob`、`BlobTarget` | 提供完整主键；两模式可用；MySQL 写入必须在调用方可写 Tx 中 |
| Oracle 原生 LOB | `OpenBlob`、`OpenClob` | 查询必须得到单个 locator；SQL NULL 返回 nil,nil；不换辅助会话冒充同一事务 |
| Oracle 复杂类型 | `Object`、`Array`、`Attribute` | 使用真实类型名/有序字段；驱动从当前会话查询描述；MySQL 不支持 Oracle 类型 |
| 游标 | `OpenCursor`、`Fetch` | 每批 Rows 消耗完并关闭后再 fetch；Forward/Static/Sensitive 能力按目标限制 |
| 元数据 | `Metadata` | 返回标准 Rows，查询结束要关闭；列名与筛选契约见架构和源码注释 |
| XA | `XAStart`、`XAEnd`、`XAPrepare`、`XACommit`、`XARollback` | 整个分支使用固定连接，不混用 sql.Tx 或裸 COMMIT；持久协调器由应用负责 |

`ExecBatch` 保留输入顺序、实际 `Strategy`、每项 `Status/Started`。`BatchFailed` 不保证没有副作用，`BatchUnknown` 不能自动重试；当前 rewrite/bulk/array binding 回退逐项，不应宣称原生优化已完成。批量成功同样不表示调用方事务已提交。

`Execute` 的命令结果不伪装成行集；`ResultInfo` 的计数只有在对应 `Known` 为真时才可使用。Oracle 匿名块不合成内部每条 DML 的独立计数。读取异常、关闭异常和 `Snapshot.Err` 均需要处理。

生命周期顺序为：关闭每批 `Rows` → 关闭 `Cursor` / `Blob` / `Clob` / `Execution` → 提交或回滚 `Tx` → 关闭固定 `Conn` → 应用结束时关闭 `DB`。Blob 的偏移单位为字节；Clob 为 Unicode 码点，均从零开始。读到文件尾可能返回 `io.EOF`；LOB 返回 nil 时先处理 SQL NULL，不能解引用。

普通 `QueryContext` 返回的 LOB/复杂值与显式 locator 不是同一种所有权语义；需要有界物化时设置 `Limits.MaxLOBBytes` 等限制，不能用无限物化读取大对象。`CursorStatic` 需要有界缓存；`CursorSensitive`、可更新游标及原生 locator 的具体版本限制见兼容矩阵。示例使用 Forward，不证明其他模式已完成真实验收。

## 5. 可运行示例

综合程序位于 [examples/usage](../examples/usage/main.go)，分为 [标准调用](../examples/usage/basic.go)和[资源操作](../examples/usage/resources.go)。只依赖公开根包和标准库，不新增运行时封装。已有 [XA 程序](../examples/xa/main.go)独立展示 XA 生命周期。

综合示例要求：

- 专用测试数据库/schema，允许 CREATE/DROP TABLE、PROCEDURE；Oracle 分支还需要 CREATE/DROP TYPE 和类型描述查询权限。
- `OB_DSN` 必须包含显式 `mode=mysql` 或 `mode=oracle`，与目标一致；拒绝 Auto，避免用未知模式选择 SQL。
- 示例强制使用服务端预处理；MySQL 分支为固定 SQL 打开 `AllowMultiStatements`，业务服务不应据此全局打开该功能。
- 程序使用随机大写对象名，创建表/过程/类型，写入示例数据并检查结果；退出时尝试按依赖逆序删除。DDL 可能隐式提交，不能靠回滚撤销。

设置好 `OB_DSN` 后在仓库根执行；下列命令会实际连接数据库，本阶段没有执行：

```sh
go run ./examples/usage
go run ./examples/xa
```

不要把真实密码直接写进命令历史；通过应用环境或安全配置注入。包含 `@` 的用户名需按前述 URL 规则编码。示例执行错误会输出至 stderr，服务端错误可能包含 SQL/值，这种交互排错方式不应直接复制到生产日志。

| 示例步骤 | MySQL 兼容模式 | Oracle 兼容模式 |
| --- | --- | --- |
| `basic` | 事务内预处理+批量，提交后查询 | 同一公开 API |
| `call` | CALL + OUT/INOUT | 匿名块+命名绑定+OUT/INOUT |
| `results` | UPDATE/SELECT/UPDATE，多命令+行结果 | 匿名块作为一个服务端可观察结果 |
| `tableBlob` | Tx 中按完整主键读写 BLOB | 同一公开 API |
| `cursor` | Forward，逐批读取并及时关闭 Rows | 同一公开 API |
| `nativeBlob` | 不执行，打印模式限制 | 在固定连接读取原生 BLOB locator |
| `complexTypes` | 不执行，打印模式限制 | 建 OBJECT/ARRAY 类型，精确 Number 绑定与 Scan，回滚示例数据 |
| 独立 XA 程序 | 开启/结束/回滚随机分支 | 同一公开 API；不是持久 2PC 协调器 |

示例没有在数据库上运行。它们不替代两模式、直连/OBProxy、版本/认证/协议组合的集成测试。执行中断、断连或进程被终止可能导致清理失败，错误会保留对象名；重新连到同一测试 schema 后人工确认并清理，不删除不明归属的对象。

## 6. 离线验证与当前边界

```sh
go test ./... -count=1
go vet ./...
golangci-lint run
go test -tags=integration -run '^$' ./tests/integration
go test -run '^Example' .
```

全包测试会编译两个 main 示例，但不会执行 `main`；`Example` 只有含 `Output` 注释的函数才运行，其他只编译。`ExampleNewConnector_proxyUser` 和 `ExampleSessionOptions_collation` 为不联网的配置演示。integration 的 `-run '^$'` 也只是编译。

本地性能采样使用不联网的协议对端，见[本地基准](go-driver-benchmarks.md)；普通单测会检查同一组操作与结果。

尚未闭环：Shared Memory/VC 平台适配、全部 Java 配置的行为/默认差异核对、真实数据库完整基准与长期资源曲线、真实数据库联调、Linux 实际运行、远程 CI 和成功 race 证据。完整范围仍保留在 D28/D32 及其依赖任务，本文和示例不构成新增验收结论。

手动集成入口 `TestCreateDatabaseSessions` 的 MySQL 两个 profile 另需 `OB_MYSQL_CREATE_DATABASE_DSN` / `OBPROXY_MYSQL_CREATE_DATABASE_DSN` 专用凭据，允许创建/删除临时数据库、查询 information_schema.SCHEMATA 并读写其中的表。用例生成随机名称，确认不存在后创建，验证新建/既有库和 InitSQL，再关闭业务会话并尝试删除；权限不足、清理失败必须报告。`OB_TEST_REQUIRE=1` 时缺少 fixture 直接失败。该用例本阶段仅编译，未连接数据库。

手动入口 `TestReadTimeoutSessions` 的四个 profile 分别需要 `OB_MYSQL_READ_TIMEOUT_SQL`、`OB_ORACLE_READ_TIMEOUT_SQL`、`OBPROXY_MYSQL_READ_TIMEOUT_SQL`、`OBPROXY_ORACLE_READ_TIMEOUT_SQL`：必须是首个响应前等待至少3秒的专用只读 SQL。测试用1秒读取预算验证网络超时、淘汰和新物理连接；建连/普通 Ping 应在该预算内完成。缺 fixture 在 require 模式下失败。本阶段仅编译，未执行；连接关闭不保证服务端慢查询已经停止，不得提供有业务副作用的 SQL。
