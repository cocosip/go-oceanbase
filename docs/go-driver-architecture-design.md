# OceanBase Go 驱动架构设计

状态：设计提案，尚未实现。本文面向完整功能适配，不以基础 CRUD 作为最终范围。

## 1. 目标、已确认要求与设计基线

已确认要求：

1. 驱动面向 OceanBase，统一承载 Oracle、MySQL 两种兼容模式。
2. 将 Java 驱动的完整功能纳入 Go 架构，逐项说明承载方式和验证条件。
3. 遵守 Go 习惯，保持普通业务的 API 简洁，不复刻 JDBC 的类层次。
4. 最低 Go 版本为 **1.25**，同时支持 **1.26**；本设计不依赖 Go 1.27 新增接口。
5. 日志使用调用方传入的 `*slog.Logger`；不使用全局默认 logger。
6. 设计文档保存在本仓库 `docs` 目录。

参考基线：

- [Java 源码结构与功能大纲](obconnector-j-architecture-outline.md)。
- 实施拆解见 [开发计划](go-driver-development-plan.md)，任务状态与验证证据统一维护在 [开发进度](go-driver-development-progress.md)。
- Java 源码：[oceanbase/obconnector-j](https://github.com/oceanbase/obconnector-j)，版本 `2.4.14`，提交 `6adc311ba5db8b2f19f98f3e6e4734667fdfdda1`。
- 标准库源码参考版本：Go `1.27.2`。已阅读 `database/sql` 和 `database/sql/driver` 的接口契约；选用的是 Go 1.25 已存在的接口。最终需要用真正的 Go 1.25、1.26 工具链分别编译及测试。
- Go 历史 API 清单也已核对：`RowsNextResultSet` 自 Go 1.8 提供，`sql.Out` / `NamedValueChecker` 自 Go 1.9 提供，`slog` / `context.AfterFunc` 自 Go 1.21 提供；这些核心机制均不要求 Go 1.26 或 1.27。
- 以下 API 名称、包划分、默认策略和资源限制是本方案建议；不是已经发布的兼容承诺。

“全功能”按可观察能力适配：包括普通执行、两类预处理、名称绑定、过程/函数、OUT/INOUT、RETURNING、批量、游标及可更新结果、大对象、复杂类型、元数据、本地与 XA 事务、地址发现、故障切换、安全认证、OB20、追踪和诊断。Java 的 OSGi、JMX、JDBC 旧类名及 Java 对象序列化应映射到 Go 的相应机制，不成为 Go 用户必须学习的 API。

能力本身受 OceanBase/OBProxy 的版本和协商结果限制。设计包含这些能力，不等于任意版本、任意模式都能执行它们；不支持时应给出明确错误或采用保持语义的实现。

## 2. 总体方案

采用 **`database/sql` 标准入口 + 一个公共 `oceanbase` 包 + 共享会话/执行内核 + 显式的模式处理**。

| 决策 | 设计 |
| --- | --- |
| 普通调用 | `sql.DB`、`sql.Conn`、`sql.Tx`、`sql.Stmt`、`sql.Rows` |
| 连接池 | 由 `database/sql` 管理，驱动不再实现一个池 |
| 物理连接 | 一个底层 Socket 对应一个物理会话；执行过程中不换 Socket |
| 兼容模式 | 从认证响应识别；配置中的模式仅作为期望约束 |
| 扩展能力 | 标准接口能表达的继续使用标准接口；不能表达的提供小型函数或具体类型 |
| 类型扩展 | 输入使用 `NamedValueChecker`；普通读取使用 `Scanner` 和标准值 |
| Go 1.25 适配 | 不依赖 `RowsColumnScanner`、`sql.ConvertAssign`，不使用标准库内部符号或 `go:linkname` |
| 日志 | Connector 实例持有调用方 logger，nil 表示关闭日志 |
| 重试 | 新建连接可更换候选地址；已发送业务操作不自动重放 |
| 实现方式 | 优先纯 Go、标准库；字符集及平台认证等确有需要时使用范围明确的依赖 |

不采用在一个入口下简单转发给两个现有数据库驱动的总体结构。Java 已显示 Oracle 兼容模式涉及 OceanBase 自身的命令、类型、LOB 和游标扩展，必须有本驱动可控制的共享协议实现。[J01][J02]

```mermaid
flowchart TD
    APP[业务代码] --> SQL[database/sql]
    APP --> EXT[批量 / LOB / Cursor / 元数据 / XA 扩展]
    EXT --> SQL
    SQL --> ADAPTER[Driver / Connector / Conn / Stmt / Rows / Tx]
    ADAPTER --> CORE[会话与执行内核]
    CORE --> MODE[Oracle / MySQL 兼容模式处理]
    CORE --> WIRE[命令、响应、标准包流 / 压缩 / OB20]
    CORE --> ROUTE[连接建立时的选址和地址发现]
    WIRE --> NET[net.Conn / TLS / 认证]
    NET --> OB[OceanBase / OBProxy]
    CORE -. 结构化诊断 .-> LOG[外部注入的 slog.Logger]
```

扩展层复用同一内核、连接和错误规则，不另建一套连接管理系统。

## 3. 比 Java 骨架更深入的约束

| 源码事实 | Go 架构必须作出的处理 |
| --- | --- |
| 认证 OK 状态位识别 Oracle 兼容模式 | `Config.Mode` 不强制改解释器；连接成功后校验实际模式。[J01] |
| 相同类型码存在两套类型解释 | 解码上下文必须包含实际模式，不能仅按类型码转换。[J03] |
| Java 接收普通、callable OUT、cursor 和多结果响应 | 响应解析是一个完整状态机，不能读到第一份结果就归还连接。[J04] |
| Java COM_RESET_CONNECTION 会清理 prepare 缓存 | Go 的 session reset 必须同时处理 statement ID 的失效和重新 prepare。[J05] |
| Java 的部分 LOB 路径为避开活跃流式结果另开连接 | Go 不复制隐式辅助连接；大对象通过明确的会话归属和游标批次边界完成读写。[J06] |
| Java 会判定某些 SELECT 可以重放 | Go 不依据 SQL 首词判定安全性；有函数、副作用、锁语义的 SELECT 也不能据此重放。[J07] |
| Java PrepareExecute 及 offset fetch 另有版本条件 | 功能判定结合模式、选项、版本和能力；不把一个 `isOracle` 布尔值当作全部能力。[J08] |
| Go 的 `ErrBadConn` 会触发标准库重试 | 业务操作可能已执行时必须返回原错误，并通过 Validator 淘汰连接。[G01][G02] |
| Go 的 Rows 与 Scanner 主要传递标准值 | 复杂值必须有明确定义的无损表示；连接相关句柄使用独立扩展入口。[G03][G04] |

## 4. 包与文件组织

建议先保留一个公共包，按 Go 文件组织职责；只有协议边界和可独立复用的内部子系统进入 `internal`。

```text
go-oceanbase/
├── go.mod                         go 1.25
├── driver.go / connector.go        注册、配置快照、建连入口
├── config.go / dsn.go              Go 配置与连接串
├── conn.go / session.go            物理会话、生命周期、操作状态
├── mode.go / capabilities.go       实际模式、版本与生效能力
├── stmt.go / execute.go            普通执行及两类预处理
├── bind.go / bind_oracle.go         参数计划、名称绑定、类型处理
├── rows.go / result.go             标准 Rows、多结果、计数和生成键
├── codec.go / codec_mysql.go       共享值转换及 MySQL 兼容模式
├── codec_oracle.go / types.go      Oracle 兼容模式及公开扩展值
├── tx.go / savepoint.go / xa.go    本地事务、保存点、XA
├── batch.go / call.go              批量、过程、输出参数
├── lob.go / cursor.go              连接相关扩展资源
├── metadata.go / metadata_sql_*.go 数据库结构和类型字典
├── error.go / logging.go           错误、注入日志及诊断
├── trace.go / local_infile.go      请求追踪、本地数据导入
├── internal/
│   ├── wire/                      包边界、缓冲、命令与响应基础结构、OB20
│   ├── auth/                      认证算法及算法自身的阶段状态
│   ├── routing/                   地址、TNS/OCP、负载均衡、黑名单
│   └── charset/                   字符集编码与 collation 映射
├── examples/                      标准用法与扩展用法
└── docs/                          架构、能力矩阵、协议证据和 API 说明
```

这些文件名用于说明职责，不要求一次创建所有空文件。`wire` 不依赖 `database/sql` 或公共业务类型；`routing` 不执行用户 SQL；模式处理留在同一 Go 包内，避免为了两个内置模式建立跨包接口层。

允许的生产包依赖固定为 `oceanbase → internal/{wire,auth,routing,charset}`；这四个内部包不得导入根包，初始也不互相导入。它们可以依赖标准库和所属任务确认的必要算法/平台库。根包负责组合、握手顺序、认证安全门禁、会话执行、模式和策略；内部包只接收自身定义的小型输入值、返回自身结果，不接收整个 Config/Conn，也不为共享类型增加 common/core/interfaces 包。

| 类型/行为 | 归属及转换边界 |
| --- | --- |
| Config、Mode、公开凭据回调及认证工厂/插件接口 | 根包；公开签名只使用公开类型/标准库类型，不泄漏 internal 类型 |
| 算法输入、challenge/response、算法阶段状态 | auth 自有类型；根包适配公开插件与内置算法，受控发送及 TLS 门禁留在根包；auth 不操作 wire/Socket |
| 包头、基础握手字段、命令/响应结构 | wire 自有类型；根包将字段转成实际 Mode、Capabilities 和错误 |
| 地址、发现结果、负载选择状态 | routing 自有类型；根包提供必要配置值并检查模式/角色策略，不传整个 Config |
| 编码器、collation 数据 | charset 自有类型；根包 codec 负责数据库类型和值语义 |

测试辅助 `internal/testutil` 只依赖标准库，用 net.Conn、字节及 context 模拟对端，不导入根包或上述内部包；生产代码不得导入它。包测试可以单向使用 testutil，外部 API 测试和 integration 可导入根包。D01 起 CI 用 `go list -deps ./...` 检测实际循环；D02 增加根包外部测试 `architecture_test.go`，解析 `go list -deps -json -test -tags=integration ./...` 校验生产及测试 import 边，阻止反向/横向依赖。新增内部包或依赖边先同步本节及测试规则，不能仅因编译暂时通过而放行。

模式分派采用具体函数和小范围 `switch`，例如 `initSession`、`encodeParameter`、`decodeColumn`、`metadataSQL`。只有两种实现且调用点固定时，不先引入 DialectFactory、ProtocolFactory、ServiceContainer 等结构。

## 5. 普通用户的入口

连接字符串入口继续保留：

```go
db, err := sql.Open("oceanbase", "oceanbase://app:secret@127.0.0.1:2881/demo?mode=auto")
```

需要 logger、TLS 或外部凭据时，建议使用程序化配置：

```go
// oceanbase 已按项目的最终模块路径导入；示例中的符号属于设计 API。
logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

connector, err := oceanbase.NewConnector(oceanbase.Config{
    Addr:     "127.0.0.1:2881",
    User:     "app@tenant#cluster",
    Password: password,
    Database: "demo",
    Mode:     oceanbase.ModeAuto,
    Logger:   logger,
})
if err != nil {
    return err
}
db := sql.OpenDB(connector)
defer db.Close()

db.SetMaxOpenConns(20)
db.SetMaxIdleConns(10)
db.SetConnMaxLifetime(30 * time.Minute)
if err := db.PingContext(ctx); err != nil {
    return err
}
```

这是用法片段，不是已可运行的包。`NewConnector` 验证配置并生成快照，不进行网络连接；实际建连发生在 `Connect`。用户名按 OceanBase/OBProxy 所需形式传递，不擅自拆写 tenant/cluster 语义。

主要入口建议保持为：

```go
func ParseDSN(dsn string) (Config, error)
func NewConnector(cfg Config) (driver.Connector, error)
```

不再提供另一个 DB、Connection、Transaction 或 Pool 包装类。直接使用 `sql.OpenDB`，避免重复维护 Go 标准库已经提供的构造和连接池配置。

## 6. 配置模型和默认值

`Config` 的常用字段置于第一层：`Addr`、`User`、`Password`、`Database`、`Mode`、`Logger`、`TLSConfig`、`ConnectTimeout`。高级选项按真实职责分组，不照搬 Java 的全部布尔开关。

| 分组 | 承载能力与建议默认值 |
| --- | --- |
| 连接 | `Addr` 默认 `127.0.0.1:2881`；静态 User 或 Credentials 回调必填；连接建立总预算默认 10 秒，受调用 context 更早期限约束 |
| 模式 | `ModeAuto` 为零值；`ModeMySQL` / `ModeOracle` 是实际模式校验约束 |
| 传输 | `TLSConfig *tls.Config`、`DialContext`；TCP 为默认；nil 不主动启用 TLS，非 nil 必须成功验证 TLS，禁止降级；明文凭据限制见 §17.2 |
| 凭据/认证 | 外部 Credentials 回调、按 Connector 配置的认证插件工厂；未提供回调时使用静态 User/Password |
| 协议 | OB20、压缩、校验、新元数据等采用 auto/off/required 等有意义策略；auto 以协商结果为准 |
| 执行 | 自动选用服务端预处理；显式支持客户端预处理、合并执行、缓存和批量策略；`AllowMultiStatements` 默认 false；`RowsAffectedMode` 默认 `MatchedRows`，见 §11.4 |
| 会话 | 字符集、NCHAR 字符集、Location、会话初始化设置、Managed/Full 两种 reset 策略 |
| 资源 | 最大逻辑消息、LOB 物化上限、FetchSize、`MaxResultItems`、关闭/清理时间上限、缓存容量 |
| HA | 地址来源、随机/轮转/亲和/组策略、失败退避与黑名单；在新建连接时生效 |
| 诊断 | 慢请求阈值、受限包追踪回调、完整链路追踪；日志开关由 Logger 是否为 nil 决定 |

建议初始资源默认值：逻辑消息上限 64 MiB、单个 LOB 自动物化上限 16 MiB、游标 fetch 128 行、一次 Execute 的结果摘要上限 `MaxResultItems=1024`、清理时间上限 1 秒。具体限制可以配置；超过限制返回明确错误，不截断后宣称成功；大对象提示使用流式入口。物理分包长度和逻辑消息上限是两个不同概念。

认证配置现有接口为 `Credentials func(context.Context) (Credentials, error)`、`AuthOptions{RSAKey, Plugins}`。Plugins 按协议插件名映射到每次认证的工厂，返回实现 Security/Respond 的插件。RSAKey 使用调用方固定的公钥且在 Connector 中复制，禁自动检索。回调、插件工厂及外部 TLS 对象内的回调/私钥对象需满足并发安全与 context 契约。接口的安全边界见 [兼容矩阵](go-driver-compatibility-matrix.md)。

配置规则：

- DSN 使用 `net/url` 的转义规则；参数名严格校验，未知项报错，敏感重复项报错。
- `mode` 等与行为有关的参数不得静默忽略；错误信息不得包含完整 DSN 或密码。
- logger、函数回调、TLS 对象通过 Go 配置传入，不用全局注册表或字符串名称寻找实例。
- Connector 复制切片和 map，并 clone TLS 配置；公开 cfg 后续修改不改变已建立 Connector。外部回调和 logger 自身需满足各自并发契约。
- 不提供允许任意 Java 参数透传的 `map[string]any`。每个公开选项必须对应已有功能和明确语义。

## 7. 标准库接口实现表

| 内部类型 | 必须实现的接口 | 关键规则 |
| --- | --- | --- |
| driver | `driver.Driver`、`driver.DriverContext` | 注册名 `oceanbase`；Open/OpenConnector 使用同一解析器 |
| connector | `driver.Connector` | 并发安全，创建多个等价会话；不保存 Connect 的 context 作后续查询 context |
| conn | `driver.Conn`、`ConnPrepareContext`、`ConnBeginTx` | 所有主路径支持 context；旧方法仅委托同一实现 |
| conn | `ExecerContext`、`QueryerContext` | 统一快速执行路径；不在已发送命令后返回 `ErrSkip` |
| conn | `Pinger`、`SessionResetter`、`Validator` | 真实探活、复用前检查、归还前淘汰；`IsValid` 不进行网络 I/O |
| conn / stmt | `NamedValueChecker` | 标准值按标准库转换；扩展值显式校验，不能一律返回 nil |
| stmt | `driver.Stmt`、`StmtExecContext`、`StmtQueryContext` | statement 绑定具体物理会话，NumInput 遵守绑定计划 |
| tx | `driver.Tx` | Commit/Rollback 的旧接口用有界网络 I/O；不假定存在 CommitContext |
| rows | `driver.Rows`、`RowsNextResultSet` | 流式读取、完整多结果清理、缓冲所有权清晰 |
| rows | 五个 `RowsColumnType*` 接口 | 支持 `Rows.ColumnTypes()`；未知属性返回 `ok=false` |
| result | `driver.Result` | int64 溢出检查；无可靠生成 ID 时 LastInsertId 返回错误 |

Go 1.25/1.26 不提供 `RowsColumnScanner` 和 `sql.ConvertAssign`，本方案不将自定义 struct 作为普通 `Rows.Next` 的输出。Go 1.27 未来若优化扫描，只作为内部优化，不改变公开值语义。[G03][G05]

## 8. 模式、版本、能力和会话状态

```go
type Mode uint8

const (
    ModeAuto Mode = iota
    ModeMySQL
    ModeOracle
)
```

内部应分开保存：

- `expectedMode`：来自 Config，可以是 Auto。
- `actualMode`：认证后的实际兼容模式，只能是 MySQL 或 Oracle。
- `serverVersion` / `obVersion`：协议版本字符串与 OceanBase 版本。
- `negotiatedCapabilities`：握手及连接属性反馈确定的能力。
- `effectiveOptions`：结合配置、版本及能力计算出的生效行为。
- `sessionState`：事务状态、schema、字符集、时区、活跃结果和资源代次。

模式不匹配：关闭新连接，返回包含期望/实际模式的错误，不重解释同一份响应。服务端缺少要求的必要能力时同样在使用前报错。

不把所有能力都视为可直接通过一个握手位判断。PrepareExecute、offset fetch 等要结合源码中的版本门槛及真实服务器验证；版本字符串无法解析时采用已确认的保守路径，不假定“最新版本”。

一个连接池保持同一模式：ModeAuto 在该 Connector 第一个成功初始化的连接上原子锁定模式，后续候选地址若返回另一模式则拒绝。地址源按同一 OceanBase 逻辑服务配置，不允许一个池混入两个不同租户或数据库服务。检查过程不记录密码。

## 9. 建连、传输与协议层

建连顺序：选址 → Dial → 初始握手 → TLS → 认证 → 模式识别 → OB 能力协商 → 包流选择 → 会话初始化 → 基线快照。

- 建连 context 和 ConnectTimeout 覆盖选址、外部凭据、TLS、认证与初始化的总预算，不能为每个候选地址重新取得一整份预算。
- 支持 TCP、Unix socket；Windows Named Pipe、Shared Memory、OBProxy VC 通过提供的 Go 平台适配及 `DialContext` 接口接入。平台专用实现使用 build tags，不让所有平台加载原生认证/传输依赖。
- 标准包、压缩包和 OB20 复用命令编解码；OB20 自身维护 request ID、序号、扩展信息和校验。
- 必须处理分包、重组、EOF/OK 终止差异、附加 OK、session tracking、OUT 响应与多结果。
- 包长度、列数、嵌套深度、压缩解开后的大小都在分配前检查；各 int 转换在 32 位平台做溢出检查。
- 未知字段若可按长度安全跳过，则保留连接同步；无法确定边界的未知协议结构使该连接失效。
- 不采用未识别类型一律当作 BLOB 的隐式兜底；Java 中存在此路径，Go 版改为明确的 UnsupportedType 错误，避免把类型解释错误传播给业务。[J03]

## 10. 执行内核与绑定

### 10.1 路径选择

- 无参数普通执行优先 COM_QUERY。
- 有参数默认走服务端 prepare/execute，避免在普通调用中实现文本插值。
- Config.PrepareMode 默认 PrepareServer；显式 PrepareClient 采用模式感知的词法计划和文本编码。MySQL 文本使用明确字符集的 hex literal；Oracle 使用引用及类型字面量。执行时使用当前 NO_BACKSLASH_ESCAPES，不插入 sql_mode 查询。当前不自动降级。
- Oracle 游标、LOB 或需要扩展协议的语句可选择服务端执行，即使没有 IN 参数。
- 连接中保留原 SQL、绑定计划和 statement 代次；服务端缓存按物理会话存放，LRU 有界，驱逐时释放服务端资源。
- PrepareExecute、checksum、参数类型头复用、long/piece data、多发包、bulk、array binding 均是同一内核内的实现策略。

Oracle RETURNING 所需的 COM_STMT_PREPARE_EXECUTE 基础编码、输出解码及能力门禁随 D14 实现，不等到 D24 优化任务。D24 显式依赖 D14，复用该路径扩展通用执行选择、缓存和性能策略；D14 不反向依赖 D24。协议是语义前提时不能退回未证实支持该语义的普通执行路径。[J17]

降级只在尚未执行业务操作时发生。服务端 prepare 返回可确认的不支持错误，才可按配置退到客户端 prepare；执行响应丢失不触发另一条执行路径。

多语句文本必须显式启用 `AllowMultiStatements`；过程调用或 Oracle 匿名块内部的分号不等于多个顶层语句。带参数的多语句若不能走服务端预处理，应在发送前报能力错误，或使用调用方已显式选择的客户端预处理；不能为支持多语句而偷偷打开文本插值。需要保留混合结果的完整顺序和逐项计数时使用 §11.4 的 Execute。

### 10.2 占位符与名称绑定

| 模式 | 支持规则 |
| --- | --- |
| MySQL 兼容模式 | `?` 按位置绑定；带名称的参数不静默按位置处理，明确报错 |
| Oracle 兼容模式 | `?` 位置绑定；`:name` 配合 `sql.Named("name", value)`；`:1` 等数字名称按位置计划处理 |

同一语句混合位置与名称绑定、缺失参数、重复提供同名参数、多余参数均在发送前报错。SQL 中同名绑定多次出现时，同一输入值映射到各个物理位置；OUT 重复绑定只接受服务端可证明一致的输出语义，否则报错。

词法计划必须覆盖单/双引号、反引号、注释、转义、Oracle q-quote 和 `:=`。不按字符串替换冒号，不把匿名块内部的分号认作多语句分隔符。

普通 SQL 使用服务端原生语法。Java JDBC escape 的等价能力由显式兼容转换函数提供，转换仍使用词法状态机；不对所有 Go SQL 默认改写。

### 10.3 扩展操作如何进入同一个内核

标准库不能直接表达批量、scroll fetch 或 locator 操作。扩展 helper 内部可传递一个**包私有请求值**给 ExecContext/QueryContext，由 `NamedValueChecker` 接受，并在发送前从 SQL 参数中分离。

- 请求值只在本包构造，含具体操作和结果承载指针；不是公共 `any` 指令容器。
- 不通过全局变量、goroutine ID、context 中的字符串命令或特殊 SQL 文本传递操作。
- 不用 `ErrRemoveArgument` 在参数检查阶段修改连接，避免后续参数校验失败时留下隐式状态。
- 所有参数先转换、校验，再执行；批量内部的每个参数也走同一转换规则。
- 该机制只服务标准接口没有表达位置的能力，不绕过标准库的连接选择、事务绑定和并发管理。

## 11. 结果、错误与值转换

### 11.1 普通结果

`Rows.Next` 输出 `nil`、`int64`、`float64`、`bool`、`string`、`[]byte`、`time.Time`，必要时使用标准协议允许的子游标 `driver.Rows`。不把一个任意结构体直接交给 `database/sql`。[G03]

数据缓冲可在 Next 间复用，但必须遵守 `[]byte` 和 `sql.RawBytes` 的寿命规则。对调用方拥有的副本和类型 Scanner 内部保存的数据负责复制；不能在 Close 时覆盖调用方仍引用的独立内存。

响应解析器处理普通结果、更新计数和 callable OUT 的完整边界。`NextResultSet` 只切换普通行结果集，不能承担公开逐项计数的职责；更新计数和结果顺序由 §11.4 的 Execute 暴露。OUT 控制结果不伪装成用户的普通结果集。

### 11.2 类型映射

| 数据语义 | 普通 Scan 来源 | 精确/特殊用法 |
| --- | --- | --- |
| 有符号整数 | int64 | 超界报错 |
| 无符号大整数 | int64 能表示时为 int64，否则十进制 string | 可 Scan 到 uint64；输入 uint64 由 NamedValueChecker 保留并检查 |
| DECIMAL、NUMBER | 精确十进制 string | 自带轻量 `Number` 或第三方 Scanner；不经 float64 中转 |
| FLOAT、DOUBLE、BINARY_FLOAT/DOUBLE | float64 | 按协议精度转换；NaN/Inf 是否允许按类型和配置校验 |
| CHAR/VARCHAR、NCHAR/NVARCHAR2 | UTF-8 string | NCHAR 输入使用 `NString` 保留绑定类型；固定 CHAR 的空格语义由服务端和列类型决定 |
| BINARY、RAW、BLOB 内联值 | []byte | RAW/ROWID 的显式类型用于指定输入协议类型 |
| BOOL、BIT | bool 或 []byte，按列类型和配置固定 | 不把所有 TINYINT(1) 强制转换为 bool |
| JSON | string | 可 Scan 到 []byte/自定义 JSON Scanner；大数不经 JSON float64 转换 |
| 无时区 DATE/DATETIME/TIMESTAMP | 规范文本；可配置解析为 time.Time | 保留日期或本地时间语义；不默认为 UTC 瞬间 |
| 带时区 TIMESTAMP | 包含精度、偏移及可用 region 信息的规范文本 | `TimestampTZ` Scanner/输入类型保留 region；不得只剩格式化偏移 |
| TIMESTAMP LTZ | 依据明确的会话时区读取的规范文本 | `TimestampLTZ` 保留其与无时区值不同的类型身份 |
| TIME、YEAR、零日期 | TIME 为可含负号及超过 24h 的文本；YEAR 为整数 | 零日期默认保留原值；要求解析 time.Time 时遇非法日期明确报错 |
| INTERVAL YM / DS | 无损规范文本 | `IntervalYM` / `IntervalDS` 保留月数或日秒精度，不全部塞入 time.Duration |
| OBJECT、ARRAY/集合 | 带版本和类型描述的无损文本表示 | `Object` / `Array` Scanner；绑定时发送真实复杂类型协议 |
| BLOB/CLOB/NCLOB | 普通查询受限物化为 []byte/string | 流式及修改使用 Blob/Clob 扩展，不用假装字节值的连接句柄 |
| REF CURSOR | 标准子 driver.Rows 或明确的输出游标入口 | 在父资源及原会话内读取 |
| GEOMETRY | 原始 []byte | 不擅自按 UTF-8 解码 |

`Number` 仅负责无损表示、验证和 Scanner/Valuer，不实现一套十进制运算库。输入扩展类型首先由 `NamedValueChecker` 识别并保持协议类型；普通第三方 `driver.Valuer` 再按标准库契约转换。[G04]

复杂值的普通 Scan 表示需自描述：格式版本、schema/type 名称、字段顺序与类型、NULL、精确数值、时间和二进制都有明确标记。建议采用 `encoding/json` 实现的带类型标签表示，数值以十进制文本保存、二进制以 base64 保存；`Object.Scan`/`Array.Scan` 负责解析。它是公开、文档化的类型表示，不是进程内句柄或对未知类型的兜底。

输入 Object/Array 保留明确的类型名和字段顺序；不将任意 struct 反射成数据库对象，不将任意 slice 自动当作 Oracle 集合。类型描述缓存以会话模式、schema、type 名称及可得版本为键；DDL 或类型失配后刷新描述，业务执行不盲目重放。

### 11.3 错误

```go
type Error struct {
    Code     uint32
    SQLState string
    Message  string
}
```

服务端错误通过 `errors.As` 取得 Code/SQLState；传输、context、能力不支持、类型转换和批量错误保留各自信息及 `Unwrap`。不根据一份 MySQL 错误表强行解释 Oracle 兼容模式错误。

`Error.Message` 保留服务端返回信息；服务端信息自身可能包含 SQL 片段，日志默认只记录 Code/SQLState 和错误类别，不自动输出完整 Message。

### 11.4 混合结果、逐项计数与执行状态

普通业务继续使用标准 Exec/Query。对多语句、过程或需要完整结果摘要的执行，增加一个具体结果对象，不包装 DB/Conn 或实现第二套行读取 API：

```go
type ResultKind uint8

const (
    ResultRows ResultKind = iota
    ResultCommand
    ResultFailure
)

type ResultInfo struct {
    Kind              ResultKind
    RowSetIndex       int // 非行结果为 -1；行结果从 0 开始
    RowsAffected      int64
    RowsAffectedKnown bool
    InsertID          string // 无损十进制值，不受 int64 限制
    InsertIDKnown     bool
    Complete          bool
    Err               error
}

type ExecutionSnapshot struct {
    Results []ResultInfo // 服务端可观察结果的原始顺序
    Done    bool
    Err     error
}

type Execution struct { /* 私有字段 */ }

func Execute(ctx context.Context, session Session, query string,
    args ...any) (*Execution, error)
func (e *Execution) Rows() *sql.Rows
func (e *Execution) Snapshot() ExecutionSnapshot
func (e *Execution) Close() error
```

Session 的定义见 §14.1。Execute 只接受非 nil 的 `*sql.Conn` 或 `*sql.Tx`，通过其 QueryContext 和包私有请求完成执行，由标准库构造 Rows。它同时承载普通过程结果和 §13.2 的连接相关输出，不另设 Call 或多结果专用入口。

- `Rows()` 始终返回同一个标准 Rows，读取方式仍为 Next/Scan/NextResultSet。成功时即使只有命令结果，也返回一个无行的 Rows，调用方仍需消费或关闭它。
- 解析器在第一份行结果之前、两份行结果之间及最后一份行结果之后读取命令响应，按顺序追加摘要。调用方推进 Rows 或关闭执行，摘要才继续更新；Snapshot 不驱动网络读取。
- 例如 `UPDATE → SELECT → UPDATE` 的摘要顺序为 Command、Rows、Command；只有中间一项的 RowSetIndex 为 0，两个命令的计数各自保存。不会为计数插入假行集，也不会把 SELECT 返回行数当作 RowsAffected。
- 一个命令结果可以有已知的 0 计数；Known 区分“已知为 0”与“服务端没有提供”。无法表示为 int64 的计数返回明确溢出错误。生成 ID 仅在协议可靠提供时可知，不补查、不猜测。
- 过程返回多少项以协议实际可观察结果为准。保留确有计数语义的最终 CALL 汇总响应；纯 OUT/session tracking 等控制响应不新增业务项。不从一个汇总计数虚构过程内部各语句的计数，也不一概丢弃尾随 OK 或零计数。
- Rows 项在读到完整结果边界后标记 Complete；成功 drain 也可以使它完成，不表示业务读取了每一行。命令项收到完整 OK 后完成；明确的独立服务端 ERR 可形成 Failure 项。行结果中途失败则保留该项未完成及其错误，不生成虚假的后续项。
- Snapshot 并发安全并返回摘要副本。Done 表示该执行已结束，包括失败结束；成功条件为 Done 且 Err 为 nil。失败时保留已确认的前缀和原错误，未观察到的语句是否执行不能由摘要推断。
- 参数/配置在发送前失败可以返回 nil、error；开始发送后若 QueryContext 直接失败，仍返回非 nil Execution 和 error，保留已经确认的信息，此时 Rows 可为 nil。不能以 ErrBadConn 隐藏部分执行结果。
- Close 幂等，有界关闭/排空 Rows 并完成摘要、OUT 和资源发布。它返回读取、最终化或清理的错误，即使 Rows 已自动关闭，也不能丢失此前错误。取消、超时或清理失败时标记未完成结果，清理未发布资源并淘汰无法同步的会话。调用方检查 Execute 错误、Rows.Err、Close 错误和最终 Snapshot。
- `Rows().Close()` 走同一最终化逻辑；已发布 Cursor/LOB 的寿命见 §13.2，不随 Execution.Close 关闭。Rows 的读取与关闭仍遵守标准库并发契约；仅 Snapshot 允许并发观察，不承诺并发 Next。
- 摘要达到 MaxResultItems 后不再无界分配：返回资源上限错误，保留已记录前缀并有界清理，不能将后续项默认为未执行或静默丢弃后返回成功。

普通 `sql.Result` 仍只有标准 LastInsertId/RowsAffected；多语句 Exec 返回最后一个命令结果的可知计数/ID，没有可用命令结果时相应方法返回明确错误，不把不同语句的计数相加。需要逐项信息使用 Execute；给内部 driver.Result 加方法不能穿透标准库包装。[G10]

计数策略单独固定，不由某次读取方式改变：

```go
type RowsAffectedMode uint8

const (
    MatchedRows RowsAffectedMode = iota
    ChangedRows
)
```

Config.RowsAffectedMode 默认 MatchedRows。MySQL 兼容模式通过 CLIENT_FOUND_ROWS 选择 UPDATE 的匹配行数或实际修改行数，与 Java 默认策略一致。[J13] INSERT、DELETE、upsert 等保留服务端在该能力位下的原生计数，不统一改成 1。Oracle 兼容模式保留其原生匹配行语义，不模拟 ChangedRows；显式要求 ChangedRows 时在识别实际模式后的初始化阶段拒绝，不能开始业务操作。

标准 Exec、Execute、ExecBatch 和游标 DML 使用同一生效策略；未知计数、零和溢出有不同状态。可更新游标及模拟 BLOB 的存在性判断必须遵守 §14.4、§15，不能把 ChangedRows 下的无变化更新判为记录不存在。

## 12. 连接、事务、重试和取消

### 12.1 连接状态与并发

核心状态至少区分：`ready`、`commandRunning`、`rowsActive`、`transactionActive`、`broken`、`closed`，并另存 XA/游标/LOB 的资源状态。它们是可组合的状态约束，不强行压成一个互斥枚举。

`database/sql` 管理 Conn 的串行调用。驱动只为取消回调、关闭和连接相关扩展保留必要同步，不为每条查询创建一个常驻工作 goroutine，不实现命令多路复用。

标准查询持有连接直到 Rows 结束。扩展游标允许在完整 fetch 响应已经读完的边界执行匹配该资源的后续命令；未读的网络响应绝不与另一个命令交错。

### 12.2 ErrBadConn 与结果不确定性

| 场景 | 返回和处理 |
| --- | --- |
| 进入操作时已知连接失效，尚未开始该操作 | 可返回 `driver.ErrBadConn`，由标准库重新获取连接 |
| 建连失败 | 返回具体建连错误；Connect/Open 不返回 ErrBadConn |
| 参数、类型、包大小在发送前验证失败 | 返回具体错误；连接仍同步时可复用 |
| 开始发送后的网络错误或响应丢失 | 返回原传输错误，标记连接 broken；不能包装为 ErrBadConn |
| COMMIT 响应丢失 | 返回具有“提交结果未知”语义的错误，不自动再提交或回滚并声称已撤销 |
| 已收到服务端错误 | 返回服务端错误；是否保留连接取决于协议同步及会话状态 |

关闭坏连接和允许重试是两个独立决定。Validator 负责淘汰连接，不能为了淘汰而把所有错误变成 ErrBadConn。[G01][G02]

### 12.3 本地事务和保存点

- `BeginTx` 校验隔离级别、只读参数及模式能力；不能默默降到其他隔离级别。
- 事务中的所有操作使用原物理会话；不在事务中透明换地址、换模式或重新建连。
- Commit/Rollback 恢复驱动更改的事务设置；恢复失败时丢弃连接，事务结果与恢复错误分别保留。
- `Savepoint`、`RollbackTo` 等函数操作调用方已有的 `*sql.Tx`，校验并按模式引用标识符。服务端没有 RELEASE 语义时返回明确能力错误。
- 事务 context 被取消时配合标准库的 Rollback；网络已断时不伪称已完成服务端回滚。

### 12.4 Session reset

默认 Managed 策略恢复驱动管理的设置，检查未完成事务、未清理响应和异常资源。它不承诺识别任意 SQL 引起的临时表、包状态和用户变量变化；依赖这些状态的业务使用 `sql.Conn` 或事务固定会话。

Full 策略在池复用边界执行经验证的服务端 reset，并恢复初始化基线。服务端无可靠 reset 时丢弃旧连接，让 Connector 创建新会话。该模式会失去旧 statement ID；内部以 generation 标记失效，并在下一次使用标准 sql.Stmt 时重新 prepare。

`ResetSession` 无法恢复基线时先标记连接失效，再返回 `driver.ErrBadConn`；此时下一项用户操作尚未开始，允许标准库安全获取新连接。不能只返回一个普通 reset 错误并假定标准库一定停止使用该连接。

不能在固定 `sql.Conn` 的相邻调用之间随意 reset，也不能在事务中 reset。不会通过猜测 SQL 首词维护“已经清理了全部会话”的假象。

### 12.5 Context、超时与关闭

- 所有网络操作同时考虑 context deadline 和配置的 I/O/清理预算。
- 操作开始前检查取消；执行中使用 deadline 加 `context.AfterFunc` 中断阻塞 I/O。
- 默认取消会关闭底层连接并使其失效，以保证后续不会把残留响应当成下一条响应。
- AfterFunc 的 stop 返回 false 时要协调回调结束；不能让旧请求的回调在连接重用后关闭新请求。[G06]
- QueryContext 的操作 context 生命周期延续到 Rows 读取/关闭结束；Connect 和 Prepare 的 context 不存入长期连接/statement。
- Rows.Close、Stmt.Close 和 Conn.Close 的网络清理均有上限。清理不了就关闭会话，不无限 drain，也不使用无界 Background 网络请求。
- 需要主动服务端 `KILL QUERY` 的场景提供显式 CancelQuery 能力，目标由先前取得的连接信息和会话代次确定；辅助通道由调用方已配置的 Connector 建立，使用同一认证/TLS策略。该能力与默认 context 取消分开，不将 KILL 成功当作原操作未执行的证明。

## 13. 批量、过程、OUT 和 RETURNING

### 13.1 标准过程调用

Oracle 兼容模式沿用原生匿名块与命名参数：

```go
var out string
_, err := db.ExecContext(ctx,
    "begin demo_proc(:id, :result); end;",
    sql.Named("id", int64(42)),
    sql.Named("result", sql.Out{Dest: &out}),
)
```

INOUT 使用 `sql.Out{Dest: &value, In: true}`。驱动在发送前校验非 nil、可写的 Dest，读取 INOUT 初值，并在全部所需响应消费完成后写回。[G07]

无法仅由 Go 指针推断的 OUT 类型使用小型 `Out` 扩展值，明确数据库类型和必要的长度/精度。可推断的普通类型优先直接用 sql.Out。

普通 OUT 目的支持指向 bool、各宽度有符号/无符号整数、float32/64、string、[]byte、time.Time 的可写指针，以及 sql.Scanner；整数和精度转换进行边界检查。NULL 必须写入可表示 NULL 的目的，例如 sql.Null[T] 或本包的可空 Scanner，不能静默变成零值。不支持的目的类型在发送前拒绝。Object/Array 等值通过自己的 Scanner 读取；连接句柄不走普通标量赋值。

MySQL 兼容模式使用其 CALL/函数语法和能力对应的 OUT 路径。对于不同服务端只支持变量中转的情况，中转在同一固定会话内完成并清理，不在整个 sql.DB 上跨连接执行。

内置标量输出先完成服务端响应及临时值转换，再写回所有目标，避免转换失败造成部分写回。调用方自定义 Scanner 可能具有不可回滚的副作用；逐个调用时一旦失败就返回明确错误，并说明先前目标可能已更新，不承诺任意 Scanner 的原子性。批量 OUT 则按项保存状态。

### 13.2 REF CURSOR 及连接相关输出

Go 1.25 的驱动不能自行构造 `*sql.Rows` 写入 `sql.Out.Dest`。过程的普通结果集、逐项摘要及一个或多个输出游标统一通过 §11.4 的 Execute 承载：

```go
type CursorOut struct {
    Dest **Cursor
}
```

例如将 `sql.Named("items", oceanbase.CursorOut{Dest: &cursor})` 与普通 `sql.Out` 一起传入 Execute。规则如下：

- Execute 只接受非 nil 的 `*sql.Conn` 或 `*sql.Tx`，由标准库构造 Rows；不构造标准库内部对象。
- 普通过程结果使用 Execution.Rows() 的 Next/Scan/NextResultSet 读取；摘要通过 Snapshot 取得，OUT 控制响应在内部处理，不加入普通结果集。
- 完整消费普通结果、OUT 和最终响应后，才写回输出值及游标句柄。输出游标目的必须初始为 nil；多个 CursorOut 对应多个独立输出游标。
- 不需要普通结果时可显式 Execution.Close；驱动有界 drain，成功完成后输出才可用。读取或最终化失败时返回错误，未发布的游标全部清理，不返回可用句柄；检查错误的方式见 §11.4。
- 过程 Rows 或 Execution 关闭后，已发布 Cursor 仍可用；调用方负责逐个 Close。它们保留固定 Session、资源标识、连接/事务代次；不保留原始 driverConn。
- 若服务端要求父 statement 与输出游标同寿命，内核为其保留资源租约，最后一个输出游标关闭后才释放。事务结束或会话失效仍立即使所有句柄失效。
- 过程响应结束就结束该操作的取消回调；后续 Fetch 使用新的 context，不继承已经结束的过程读取 context。
- CursorOut 只由 Execute 接受。普通 DB/Conn/Tx 的 Exec/Query 遇到该标记在发送前报错；`sql.Out{Dest: &rows}` 不作为输出 REF CURSOR 的替代写法。
- 流式 LOB 输出同样只能在 Execute 中通过显式类型的 Out 绑定到 `**Blob` / `**Clob`，使用相同的发布、租约和固定会话规则；普通 sql.Out 的 LOB 输出读取受限物化内容。

若查询本身的列是 REF CURSOR，仍可使用标准库支持的子 `driver.Rows` 扫描路径；其子 Rows 随父 Rows 关闭，与上述能够在过程 Rows 关闭后继续使用的显式 Cursor 分别遵守各自寿命契约。

### 13.3 RETURNING 和生成键

- Oracle 使用 `RETURNING ... INTO ...` 配合输出绑定；Java 对应服务端接口明确要求 PrepareExecute。D14 实现其最低必要协议及版本/能力门禁，普通分离式 prepare/execute 未经取证不能被当作等价替代；不支持时在发送业务前返回能力错误。[J17]
- MySQL 的 OK insert ID 可支持 LastInsertId；不能表示为 int64 时该标准方法返回溢出错误，Execute 摘要中的 InsertID 可保留完整十进制值。
- Oracle 不为了凑出 LastInsertId 另查全局序列或猜测主键；调用方通过显式 RETURNING 获得所需列。
- Java 的指定生成列能力通过明确的 RETURNING 绑定/小型返回列 helper 表达，不要求用户设置 JDBC 风格常量。

### 13.4 批量接口

```go
type Execer interface {
    ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ExecBatch(ctx context.Context, exec Execer, query string,
    rows [][]any, opts BatchOptions) (BatchResult, error)
```

`*sql.DB`、`*sql.Conn`、`*sql.Tx` 均可直接使用。调用方用 Tx 明确控制原子性，helper 不偷偷创建或提交事务。

BatchResult 按输入索引返回影响行数或不可知状态；BatchError 标明失败项和已确认完成项。继续执行/遇错停止由 BatchOptions 明确指定，默认遇错停止。

内核根据模式、事务、参数类型和能力选择：逐项服务端执行、多发包、SQL 批量改写、bulk 或 Oracle array binding。限制单包大小、总参数、分块行数；任何策略都不得改变调用方事务边界、输出项顺序或错误统计语义。无法保持逐项计数的协议返回“成功但计数未知”，不编造数字。

没有等价执行语义时采用逐项路径。例如自动提交下不能把原本可部分成功的多个 INSERT 改写成一个整体失败的语句；仅把计数改成未知不足以使这种改写等价。逐项路径同样遵守停止/继续策略和结果不确定性规则，不替调用方重放已发送项。

长数据 Reader 属于一次执行；报错后不自动 rewind 和重发。客户端改写需要完整词法验证，不把任意 INSERT 都当成可安全改写 SQL。[J09]

## 14. LOB 和复杂类型的 Go 1.25 方案

### 14.1 普通值与连接句柄分开

普通 BLOB/CLOB 查询返回数据内容。对 out-row LOB，Oracle 查询使用服务器游标/完整 fetch 响应边界，再在原会话读取内容；受自动物化大小上限约束。文本流式路径无法安全插入 LOB 命令时，要先使用有界完整响应缓冲，或选择游标路径。

需要超大对象、随机访问、流式上传下载、空 LOB 或修改 locator 时，使用明确的 Blob/Clob 扩展。Go 1.25 不要求把有连接身份的对象塞进 Scanner。

```go
// Session 只表示标准库已有的固定会话调用面，不创建新的 Session 对象。
type Session interface {
    ExecContext(context.Context, string, ...any) (sql.Result, error)
    QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func OpenBlob(ctx context.Context, session Session, query string, args ...any) (*Blob, error)
func OpenClob(ctx context.Context, session Session, query string, args ...any) (*Clob, error)
```

这些资源函数在入口通过具体类型检查，只接受非 nil 的 `*sql.Conn` 或 `*sql.Tx`；`*sql.DB` 虽在结构上满足 Session，也必须在网络调用前拒绝，其他包装类型同样不在该入口契约内。普通批量等无后续句柄的函数不受该限制。Open 查询应返回恰好一个所需 LOB，否则明确报错并清理所有结果。

Blob/Clob 保存调用方的标准会话引用、原生 locator 或 §14.4 的表/行身份和资源代次；后续操作通过该会话调用，驱动再次校验所属连接，不保存 `Conn.Raw` 回调中取出的原始 driverConn。MySQL 没有原生 locator 时不能把普通 BLOB 字节值假装成 locator；表内分块访问使用 OpenTableBlob。

资源拥有 locator，不拥有调用方的 Conn/Tx：关闭资源不关闭会话，调用方应先关闭资源再关闭会话或结束事务。每次操作同时校验物理连接身份、会话代次和事务代次；旧事务产生的句柄不能在同连接的新事务中继续使用。未释放资源随会话关闭或 reset 一并失效；无法可靠清理时淘汰该物理连接。

### 14.2 大对象操作

- Blob 提供按字节定义的 Reader/Writer、ReadAt/WriteAt、长度、截断和释放；网络读写都有 context 入口。
- Clob/NCLOB 提供字符读写和字符长度；字符偏移与 UTF-8 字节偏移分开定义，不套用 Blob 的字节索引。
- Locator V1/V2、内联/外联、空 LOB、临时/持久对象在内部类型中区分。
- 明确“释放服务端临时 LOB”与“关闭本地 reader”两种行为；Close 幂等，不替调用方提交事务。
- 对原会话、事务和代次失效返回资源失效错误；不能通过重新登录猜测旧 locator 仍有效。
- PIECE_DATA / LONG_DATA / DBMS_LOB 路径按模式和能力选择，保持原事务语义。

LONG_DATA/PIECE_DATA 解决参数传输，不能替代 MySQL 表内 BLOB 的随机访问。其模拟 locator 后端见 §14.4。[J14]

### 14.3 活跃结果与 LOB 的调度

同一个网络响应未读完时，禁止插入 DBMS_LOB 或其他命令。标准游标 fetch 先读完一个受限批次的响应，再 materialize 所需 LOB；剩余批次行保留在受限缓冲中。显式 OpenBlob/OpenClob 则先完整结束 locator 查询，再返回句柄。

Oracle 的“保持游标并在同连接执行 LOB 操作”需要真实服务端验证。若目标版本不支持该交错方式，使用完成查询后读取 locator 的有界方案；不能改用另一个物理连接来冒充原事务可见性。

复杂类型描述读取同样只在命令边界执行。需要补充描述时先完成当前响应，并由根包的内部调度执行字典查询/LOB 读取。即使响应已完整，驱动 QueryContext/Rows.Next 仍可能持有 database/sql 的连接锁；禁止从这些调用内部再进入公开 Metadata、Session 或 sql.DB/Conn/Tx 方法。内部直接使用当前物理会话的命令执行器，不重新取得标准库连接锁；调度器只持有一次会话操作所有权，不递归获取自己的操作锁。只共享查询计划、类型描述和缓存，见 §16.1。

### 14.4 表内 BLOB 与 MySQL 模拟 locator

采用显式目标描述取得可靠行身份，不从任意查询、列别名或 BLOB 内容推测表和主键。这是本方案的 API 选择，返回的仍是同一个 Blob 类型：

```go
type BlobTarget struct {
    Schema string
    Table  string
    Column string
    Key    map[string]any // 完整主键：列名到参数值
}

func OpenTableBlob(ctx context.Context, session Session,
    target BlobTarget) (*Blob, error)
```

- 固定会话条件与 OpenBlob 相同。Schema 为空时在当前会话解析实际 schema，随后固定对象身份；Table/Column 必填，各字段表示一个标识符组成部分，不接收 SQL 片段。
- 通过实际模式的元数据确认单个基表、可读的二进制 BLOB 列和完整主键。拒绝视图/联表、缺失或多余主键字段、NULL 主键和不支持的类型；作为目标的 BLOB 列不得同时是主键或生成列。列名按字典解析后引用，主键值全部绑定，不拼接成 SQL。
- 输入在打开时完成参数校验；Valuer 只求值一次，复制 map、切片和可变字节值，保存规范化后的不可变身份。后续修改调用方 Key 不改变句柄目标，不使用全局 token 注册表。
- Open 只取得所需元数据、存在性、NULL/长度或原生 locator，不读取整个 BLOB。无该行返回 sql.ErrNoRows；该列为 SQL NULL 返回 nil、nil；空的非 NULL BLOB 返回长度为 0 的有效句柄。初始化 NULL 使用普通显式 SQL。
- MySQL 后端按主键生成受限分块 SQL：长度使用 OCTET_LENGTH，读取使用 SUBSTRING；Oracle 可由相同目标查询取得原生 BLOB locator。MySQL 后端不是 LONG_DATA，也不承诺查询得到的任意 BLOB 都能转成持久句柄。[J14][J15]
- Blob 字节偏移采用 Go 的 0 起点；转换为 SQL 的 1 起点和计算 off+len 时检查溢出。每块同时受逻辑消息和 LOB 块大小限制；不得在后端不支持分块时偷偷整体物化超大对象。
- MySQL ReadAt/长度查询在每次调用时重新按主键访问。使用 `*sql.Conn` 时不承诺跨块快照；需要稳定读取时由调用方使用合适隔离级别的 `*sql.Tx`，必要时显式加锁。事务外也不承诺识别同一主键被删除后重新插入的另一行。
- MySQL 模拟句柄的 WriteAt/Truncate 必须运行在调用方的 `*sql.Tx` 内；否则在修改前返回错误，不隐式 Begin/Commit。每次修改先在该事务中按主键 SELECT ... FOR UPDATE，确认行和非 NULL 内容、锁定后读取当前长度，再执行参数化 UPDATE；只读事务或不支持所需锁语义时拒绝修改。
- WriteAt 只允许 `0 <= off <= 当前字节长度`，可在末尾追加，不自动填充稀疏空洞。例如二进制列的替换可使用 `CONCAT(LEFT(col, off), ?, SUBSTRING(col, off+len(p)+1))`；col 安全引用，其余值绑定。Truncate 仅缩短到 `0..当前长度`，不扩展。空写入或写入相同值，在锁定存在性已确认后可以成功；ChangedRows 返回 0 不等于行不存在。
- 不同块是多条 SQL，原子性和失败后的回滚由已有 Tx 控制。每条完整响应消费后才能发下一条；网络失败后不自动重发。表 DDL、权限或类型变化返回原错误/资源失效，不重新猜测目标。
- 表目标句柄遵守相同连接、会话和事务代次；Close 幂等，不关闭 Conn/Tx，也不替调用方提交。模拟后端没有可释放的原生 locator，Close 只清理本地身份与占用；原生后端继续释放相应服务器资源。

这一入口覆盖 Java emulateLocators 的表/主键/列定位及分块读写能力，同时保留 Go 普通 SQL 的直接用法；其额外 SQL 次数和事务要求是公开行为。

## 15. 游标、滚动和可更新结果

标准 `sql.Rows` 保持向前读取。Java 中 scroll、sensitive、updatable 的能力通过专门 Cursor 提供，不向所有 Rows 添加 Java 风格的 getter/setter。

```go
func OpenCursor(ctx context.Context, session Session, query string,
    opts CursorOptions, args ...any) (*Cursor, error)

func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error)
func (c *Cursor) Close() error
```

设计规则：

- Cursor 绑定传入的 sql.Conn/Tx；建立游标的控制响应读完后才返回对象。
- 每次 Fetch 返回一个标准 sql.Rows 批次；关闭批次只结束该批次，不关闭整个服务器游标，下一次 Fetch 前必须关闭前一批次。
- Position 明确表示 next、absolute、relative 等语义；Oracle 使用可用的 offset fetch。需要缓存的 static scroll 采用有界缓存，超限报错，不无界存储整个结果。
- Sensitive 读取及 Refresh 使用服务端可用的重新定位/刷新语义；不能用 static 缓存冒充。
- 可更新游标只接受能明确定位到单表行的结果，按主键或 ROWID 生成模式正确的参数化 DML。
- Update/Delete/Insert/Refresh 接受明确的列变更和行身份；参数化值，安全引用标识符，不拼接业务值。不对 join、聚合或缺失行标识的结果猜测更新目标。
- 定位预期为一行，检查匹配语义下的计数及行身份；零或多行时给出明确并发变化/定位错误。ChangedRows 下的 UPDATE=0 不能证明记录不存在：使用同一事务内的锁定存在性检查等可靠条件区分“无变化”和“未匹配”。无法提供该保证时在开放可更新能力/发送修改前报能力错误，不用自动提交之后的另一次查询猜测，也不无条件报告更新成功。
- 执行失败、关闭、事务结束、会话 reset 都使相应游标状态失效；服务端不支持的游标类型返回能力错误。

这是真正需要的扩展对象：标准库没有相同的滚动操作面。其查询数据仍通过标准 Rows.Scan 读取。[J10]

## 16. 元数据、XA 和会话辅助功能

### 16.1 元数据

`Rows.ColumnTypes()` 提供结果列信息；数据库结构通过函数及明确的 Filter 返回 Go 结构体或普通 Rows。

覆盖范围包括：catalog/schema、表/视图/同义词、列、主键/外键、索引、权限、过程/函数及参数、类型定义、UDT、数组元素类型、支持能力和数据库版本。与 Java 相同，服务端本身没有的信息返回“未知/不支持”，不伪造完整元数据。

模式 SQL 分文件维护，过滤值全部参数化；确需动态标识符时使用模式专用引用函数。按固定 sql.Conn 识别模式后执行相应查询，不能从一个自动模式池获取模式、再随机在另一连接上查询。

复杂类型中的 schema、同义词、嵌套对象描述属于此模块与 codec 的共享数据，不重复建两套字典缓存。

公开 `Metadata(ctx, session, filter)` 在入口检查固定 *sql.Conn/*sql.Tx，经包私有请求进入标准 QueryContext 内核，返回普通 *sql.Rows。内部类型描述解析器和 LOB 物化器则直接调用根包内的字典/命令执行器，只能在 §14.3 的合法响应边界使用当前物理会话；不能调用公开 Metadata 或标准库会话方法，也不能另开连接读取原事务字典。两条入口复用参数化 SQL 计划、描述类型及按会话权限/模式/schema/类型版本隔离的缓存，不复用高层调用栈。

D13 固定内外查询边界；D16/D18 必须用真实 database/sql 入口配合协议故障对端验证缓存未命中路径：在 QueryContext 与 Rows.Next 内需要字典/LOB 读取时均能在限定时间结束，业务和内部命令落在同一物理会话，未发生锁重入或未读响应插入命令。真实服务端另验证未提交事务可见性；模拟对端不能替代该证据。

### 16.2 XA

提供 `XID` 值和 Start/End/Prepare/Commit/Rollback/Recover/Forget 等显式操作，参数包含固定 `*sql.Conn`、XID 和标准 XA flags。XA 入口不接受 `*sql.Tx`；开启 XA 的会话不得同时 BeginTx。

- MySQL 兼容模式走对应 XA 命令；Oracle 兼容模式走 DBMS_XA 和其复杂 XID 表示。[J11]
- Start 到 End/Prepare 阶段保持物理会话约束；两阶段事务的外部协调和持久化日志由调用方负责。
- Recover 返回真实 XID 列表；准备后的分支不因一个 Go 对象 Close 就被误当成本地未提交事务回滚。
- 分支状态与连接可复用条件明确；异常或无法确认脱离分支的连接不得回池。
- 一阶段/两阶段提交、join/resume/suspend、服务端支持的超时及返回码逐项适配；不支持的 flags 明确拒绝。
- XA 失败没有透明业务重放；提交结果不确定时由协调方查询恢复。

### 16.3 其他会话功能

schema/catalog、用户切换、客户端信息、warning、会话变量及版本能力通过固定 sql.Conn 上的函数承载。ChangeUser 必须重新认证、重建基线并使所有旧 statement/locator/cursor 失效；不在普通池连接上隐式切用户。显式 ChangeUser 后将该物理连接标记为不可归还原池，调用方关闭 sql.Conn 时淘汰它，防止另一个调用方取得错误身份的会话。

warnings 作为结构化结果诊断暴露，不能把成功带 warning 的操作改成随意失败；也不能仅因日志未启用就丢失所有可查询 warning 信息。

## 17. 选址、HA、认证与导入

### 17.1 地址发现和 HA

- 地址源覆盖静态列表、扩展描述、TNS 文件和 OCP API；解析与刷新不依赖每连接线程。
- 负载策略覆盖随机、轮转、亲和和组选择；主/从角色及只读路由纳入模型，Java 的 replication/aurora 拓扑发现放在明确的拓扑适配器中。
- 读写角色作为 Connector 的建连约束；需要分别访问主/只读服务时，由调用方持有两个标准 sql.DB，不增设 SQL 级路由池。
- Go 的 ReadOnly 是事务属性，不把普通 SELECT 自动搬到另一物理连接。BeginTx 在当前物理连接校验并设置事务属性，兼容的主库也可执行只读事务；需要不同角色时调用方从相应 DB 获取连接，不能在 BeginTx 内替换现有 Socket。
- 黑名单和地址健康信息按 Connector 共享，有界且并发安全；不会把一个租户的错误传播成所有服务的全局黑名单。
- 地址失败重试共享建连总预算；认证拒绝、模式不匹配和配置错误不做无意义的无限循环。
- Refresh 使用 Connector 生命周期 context；实现 io.Closer 供 sql.DB.Close 停止共享刷新设施。TNS/OCP 未启用时不启动对应 goroutine。
- Connector 关闭后仍需允许已获得的活动连接有界完成/关闭；不会因取消发现刷新 context 而直接取消正在执行的业务查询。
- 成功建立的物理会话内部不故障换线。坏连接退出，由数据库池获取新连接；会话迁移、事务和执行结果未知不能靠恢复几个变量掩盖。

### 17.2 认证、TLS 和扩展点

- 提供 Java 中已注册认证方式的 Go 适配路径：native、SHA-256、caching SHA-2、clear/old password、Ed25519、GSSAPI、PAM。
- 实际支持由服务端协商和调用方启用策略共同决定。认证工厂按连接创建可变状态，不能把一个握手对象并发用于所有连接。
- 插件描述声明算法及各阶段的传输安全要求；共享握手状态机在发送凭据之前统一检查。公开扩展点只得到认证消息、受控发送接口、只读传输状态和 context，不得到原始 Socket 或整个 Conn。插件不能以“服务端要求”绕过该检查。
- GSSAPI/SSPI、特定操作系统身份和网络传输使用平台适配；不为它们引入 Java 风格的运行时类加载器。
- TLS 使用调用方 clone 后的 tls.Config；不因兼容模式关闭校验。弱旧算法需要显式启用，不由自动协商悄悄放开。
- 外部凭据回调在新认证/显式 ChangeUser 时获取凭据；不偷偷重认证正在执行的事务。

传输判定和敏感认证按下列统一契约执行，默认策略是本方案的安全选择：

| 场景 | 必须执行的行为 |
| --- | --- |
| TLSConfig 为 nil | 驱动不主动发起 TLS；普通 TCP 不满足明文凭据插件要求；仍可使用允许非 TLS 的挑战应答算法 |
| TLSConfig 非 nil | 必须由驱动完成 TLS 握手、证书链及服务端名称验证；服务端不支持 TLS、证书/主机名失败均终止，不降级为明文连接 |
| 自定义 DialContext | 返回原始协议传输，由驱动按相同流程升级/验证 TLS；仅返回包装 net.Conn、提供类似 ConnectionState 的方法或声称“已加密”均不构成证明 |
| 自定义 CA/验证回调 | 私有 CA 和名称通过 RootCAs/ServerName 配置，额外回调可收紧校验。本方案在 NewConnector 拒绝 InsecureSkipVerify=true，不提供用任意回调替代标准链/名称验证的旁路 |
| clear password、会传输密码的 PAM 阶段 | 仅在上述驱动完成并验证的 TLS 上发送；否则在写入该阶段凭据前失败。配置了 TLS 对象、服务端安全标志或选择了可信插件都不能替代实际握手结果 |
| SHA-256/caching SHA-2 的 full-auth | 若需要发送明文密码，采用相同 TLS 门禁；无 TLS 时仅允许算法支持且使用显式受信任/固定公钥的加密路径。不把未认证的自动公钥检索当作对端身份验证，不退回明文密码 |
| IPC/平台传输或外部已加密通道 | 仍需满足相同证明条件；仅凭“本地”或自定义 Dial 的承诺不能允许明文凭据。没有可验证 TLS 的路径可使用其合适的挑战应答/平台认证，不隐式授予明文豁免 |

门禁覆盖初始认证、每次 auth switch、多阶段认证及 ChangeUser；服务器临时改换算法同样重验。拒绝后不调用敏感阶段的凭据编码/发送，关闭未完成认证的连接；不能先发送密码再检测 TLS。已通过挑战应答发送的摘要不等于允许后续 full-auth 明文。日志和包诊断始终过滤密码、认证内容及相关密钥。[J16]

### 17.3 LOCAL INFILE

保留本地导入功能，默认不允许服务端按任意文件名读取本机文件。调用方显式传入 Reader 或按请求名解析 Reader 的回调；采用请求级或 Connector 级配置，不使用全局文件白名单。

导入时有 context、大小/时间约束及明确 Close 所有权；协议拒绝和数据发送中断也要正确结束/丢弃连接。该功能不会因未设计便被删掉，也不会为“全功能”而默认开放任意文件读取。

## 18. slog、诊断与完整链路追踪

### 18.1 日志注入契约

```go
type Config struct {
    // 常用连接字段和高级配置省略。
    Logger *slog.Logger
}
```

- `Logger == nil`：完全不输出驱动日志。
- 使用 `cfg.Logger` 或由其 With 派生的 logger；不调用 `slog.Default()`、`slog.SetDefault()`、包级 `slog.Info/Error`，也不退回 `log.Default()`、fmt.Print 或 stderr。
- 不提供 `SetLogger` 全局函数；不同 sql.DB/Connector 可以使用不同 logger。
- 不包装一套自有 Logger 接口，不强制选择 JSON/Text handler，不改变调用方的级别或输出目的地。
- 使用 Enabled 避免无谓计算，用 LogAttrs 保留结构化字段，并传入当前操作 context。
- 记录时机和字段由驱动定义，输出、采样、持久化和日志切分由调用方 handler 处理。

### 18.2 记录内容

建议字段：driver、operation、mode、server/connection/request 标识、duration、rows_affected、error_code、sql_state、retry_stage。默认不记录密码、认证包、DSN、参数值、LOB 内容、原始 SQL 或完整服务端错误文本。

SQL profiling 通过时长、命令类别和可选指纹表达。确需 SQL/包诊断时使用独立、显式启用且有大小上限的回调；认证等敏感包仍过滤，不因 Debug 级别自动泄露。

驱动日志主要用于内部诊断和慢请求，不能把每个返回给调用方的业务错误再自动记录一遍 Error。网络锁内收集必要字段，释放内部锁后调用外部 handler；handler 不应同步递归访问同一受限连接池。驱动不为“异步日志”另建无界 goroutine/队列。

### 18.3 完整链路追踪

OB20 Full Link Trace 与 slog 是两项能力：

- trace/span、ExtraInfo、采样和服务端控制信息遵守 OceanBase 协议；slog 关闭时也可传递已启用的协议追踪。
- 请求级 trace/app info 使用类型明确的 context helper，包含 module/action/client info 等，不保存为全局可变状态。
- 对接 OpenTelemetry 采用可选桥接或由调用方提供信息，不让基础驱动强制启动 exporter。
- query、fetch、LOB piece、批量和建连分别有明确的 span/请求边界；不能借复用连接把上一个请求的 trace 带给下一个请求。[J12]

## 19. Java 全功能到 Go 的覆盖矩阵

下表是交付核对表。工作可以分批完成，但表中的能力不是因为实现顺序靠后就从目标中删除。

| Java 模块 | Go 承载方式 | 完成依据 |
| --- | --- | --- |
| M01 Driver/DataSource/JDBC API | 注册名、DriverContext、Connector、标准 sql 对象；扩展具体类型/函数 | 两模式相同入口和标准库契约测试 |
| M02 URL/Options/地址描述 | 严格 DSN、类型化 Config、TNS/OCP、拓扑配置 | 参数迁移表、转义/错误/刷新测试 |
| M03 传输/TLS/认证/凭据 | net.Conn、必需时验证 TLS、统一凭据门禁、平台适配、实例级认证工厂与凭据回调 | 初始/切换/多阶段认证、无 TLS 零明文凭据发送、无降级及平台测试 |
| M04 握手/模式/能力 | 实际模式识别、期望校验、版本及协商能力分离 | 不匹配、缺失能力、OBProxy 路径测试 |
| M05 连接/会话/本地事务 | sql.Conn/Tx、BeginTx、保存点、schema/catalog、warning、ChangeUser、reset、计数策略 | 事务属性、状态恢复、匹配/修改计数和资源失效测试 |
| M06 普通/两类预处理 | COM_QUERY、客户端 prepare、服务端 prepare/execute、PrepareExecute、缓存/checksum | 文本/二进制/合并执行等价性与无重复执行测试 |
| M07 绑定/批量/长数据 | ?/:name、sql.Named、Parameter 编码、批量多种策略、LONG/PIECE_DATA | 词法/参数数/长数据/逐项计数/异常矩阵 |
| M08 结果/生成键/多结果/游标 | 标准 Rows、Execute/Execution 摘要、OUT/CursorOut、REF CURSOR 列、Cursor、更新/刷新函数 | 混合结果顺序/计数/错误前缀、未读结果清理、滚动和可更新条件测试 |
| M09 全类型/编码/时间 | 标准值、Scanner/Valuer、显式特殊类型、字符集/时区 | 全类型往返、NULL/空值/精度/区域时区矩阵 |
| M10 LOB/复杂类型 | Blob/Clob、Locator V1/V2、OpenTableBlob/MySQL 模拟 locator、Object/Array、类型字典 | 同事务读取、表/主键定位、分块读写/截断、嵌套/同义词/失效测试 |
| M11 数据库/结果/参数元数据 | ColumnTypes、结构查询函数、参数计划与字典查询 | 对照两模式数据库真实字典 |
| M12 多地址/HA | 地址源、拓扑适配、负载/组/黑名单、连接级故障转移 | 失败注入、地址刷新、无跨会话事务迁移 |
| M13 池化 | sql.DB 管池；reset/Validator 保证复用契约；DB.Stats 替代自有池 JMX | 池压力、取消、reset 后 sql.Stmt 重用 |
| M14 XA | XID 与显式 XA 函数、DBMS_XA/原生命令、恢复 | 两阶段、join/resume、进程/网络故障验证 |
| M15 日志/包诊断/OB20/FLT | 注入 slog、受限诊断回调、OB20/校验、请求级 trace | nil logger 静默、隔离、追踪串扰与校验测试 |
| M16 构建/SPI/平台/性能测试 | Go module、接口注入、build tags、go test/fuzz/bench | Go 1.25/1.26 工具链和目标平台 CI |
| LOCAL INFILE | 显式 Reader/回调；请求级权限和大小限制 | 拒绝任意文件、传输中断和清理测试 |
| JDBC scroll/updatable 与旧 API | Cursor/参数化 DML；保留能力而不复制 JDBC 方法名 | 真实支持条件、语义与并发变更测试 |
| OSGi/JMX/Java 序列化/兼容类名 | Go 模块和接口、DB.Stats/诊断；序列化值由调用方显式处理 | Go 侧能力可观察，不引入 Java 运行时依赖 |

需要另外维护逐项 option 对照表：每个 Java 选项标明 Go 配置、标准库替代、平台适配或仅 Java API 兼容用途，避免把“没做同名配置”误当作“功能已经覆盖”。不能只凭这个高层矩阵宣布功能实现完成。

## 20. 验证矩阵与实施依赖

### 20.1 验证层次

1. **标准库契约**：接口断言、参数转换、NumInput、NextResultSet、ErrBadConn、Validator、SessionResetter。
2. **协议单测及 fuzz**：包边界、压缩、OB20、截断/超长/未知字段、NULL bitmap、类型/嵌套数据。
3. **两模式真实服务端**：普通/客户端/服务端/合并执行、所有类型、过程、批量、LOB、游标、元数据和 XA。
4. **直连与 OBProxy**：能力协商、trace、用户格式、会话行为、路由和失败。
5. **故障注入**：发送前断线、部分写、响应丢失、COMMIT 丢响应、取消与完成竞态、LOB 中断、XA 恢复。
6. **资源和并发**：Rows 提前关闭、句柄泄漏、池复用、reset 代次、凭据刷新、Logger 隔离、共享地址状态。
7. **工具链与平台**：Go 1.25、1.26；Linux/Windows，支持平台上的 socket/认证适配；重点解析和边界代码在 386 验证。
8. **性能**：普通 CRUD、prepare 缓存、批量、LOB 流式、游标 fetch；对照结果正确性后再讨论吞吐。

类型矩阵至少交叉模式、文本/二进制协议、NULL/空值、最大/最小/越界、字符编码、时区和精度。复杂类型及 locator 测试不能仅用 mock 声称与服务器兼容。

CI 在 D01 建立，实际安装 Go 1.25.x/1.26.x，在 Linux/Windows amd64 执行 test/vet 和依赖检查；D02 增加包依赖边检查及协议 fuzz/386，D04 接入显式 require 的集成入口，D06 增加受支持平台的 race。后续能力同步扩展检查，D32 汇总补全矩阵，不到交付末期才建立 CI。每项必需验证集及状态聚合规则固定在[开发进度 §5](go-driver-development-progress.md#5-验证组合与证据)，不能由实现者临时选择一个成功组合验收。

### 20.2 本次补齐契约的验收场景

| 契约 | 必须有的证据 |
| --- | --- |
| 混合结果与摘要 | UPDATE→SELECT→UPDATE 的顺序及 RowSetIndex；零计数/未知计数；纯命令执行；最终 CALL 汇总与 OUT 控制响应区分；完整 uint64 insert ID；计数溢出 |
| 错误与最终化 | 中途服务端 ERR、部分响应断线、QueryContext 直接失败仍保留摘要；NextResultSet/Rows.Close/Execution.Close 的一致状态；提前关闭有界 drain；上限、取消、未完成项及错误前缀不冒充未执行 |
| 连接相关输出 | 正常消费和 Close 后才发布 Cursor/LOB；输出转换失败不泄漏未发布资源；多个句柄、父 statement 租约及后续 Fetch 独立 context；关闭 Execution 后句柄仍能使用 |
| 计数策略 | 两模式实际行为；MySQL MatchedRows/ChangedRows 的 no-op UPDATE、INSERT/DELETE/upsert；Oracle 拒绝 ChangedRows；游标与模拟 BLOB 不把 0 修改误判为缺行 |
| 认证门禁 | 初始 clear、native→clear 的 auth switch、PAM/全认证阶段在无 TLS 时无敏感凭据写出；缺 TLS 能力、证书/名称失败无降级；伪装安全状态的 Dial 不被采信；ChangeUser 重验 |
| MySQL 模拟 BLOB | 完整/复合主键、引号标识符、缺键/NULL/越界/不存在、Key 被调用方修改、NULL 与空 BLOB；超过物化上限仍能分块；首/中/尾 ReadAt/WriteAt、相同值写入、追加、Truncate、断线不重放 |
| BLOB 事务与资源 | Conn 只读路径与 Tx 修改门禁；同事务未提交可见性、锁定后删除/并发更新、rollback、隔离级别、代次失效；驱动不隐式提交，不以辅助连接读取原事务 |

这里列的是实现验收要求，尚未执行。协议模拟验证门禁和边界，真实 OceanBase/OBProxy 验证 SQL、计数、认证及事务行为；两者不可互相替代。

### 20.3 实施依赖顺序

```text
公开契约 + 模式/能力模型
  → 包流/握手/认证/基础类型
  → 会话状态 + 普通与预处理执行 + Rows + 本地事务
  → 精确/扩展类型 + OUT/RETURNING + 元数据
  → 批量 + LOB + 复杂类型 + Cursor
  → 完整 HA/平台认证 + XA + FLT
  → 全矩阵集成、故障与性能验证
```

这表示开发依赖，不表示只交付最前面几项。日志注入、取消、资源清理、错误边界和真实功能验证随对应模块同步实现，不放到最后补。

每项功能的完成记录应包含：服务端/OBProxy 版本、兼容模式、使用路径、测试证据及已知限制。没有真实服务端证据的项标记为“实现但待联调”，不得标记为已完成适配。

## 21. 当前仍需取证的协议条件

这些是实施期需要用源码/服务端证据落实的条件，不作为删减功能的理由：

- 各 OceanBase/OBProxy 目标版本对 reset、PrepareExecute、offset fetch、新元数据、LOB V2、array binding 的确切支持组合。
- Oracle 同连接保留 cursor、执行 LOB/类型字典读取及 DML 的可交错边界。
- OUT/INOUT 和 REF CURSOR 在两模式、标准/扩展执行路径中的完整响应顺序。
- 区域时区与 LTZ 的字节/语义、复杂类型版本及同义词描述、Locator 生命周期。
- 平台认证和特定传输适配的实际目标平台与必要依赖。
- Java 功能测试中被忽略或服务端不支持的场景；不能把源码类名存在当作已承诺行为。

当前已完成的是架构设计与源码契约核对。Go 驱动尚未实现，真实数据库兼容性和 Go 1.25/1.26 构建均未验证。

## 22. 核心证据索引

Java 深入阅读：

- [J01：认证后的实际模式与 OB 能力识别][J01]
- [J02：标准/扩展命令和查询执行][J02]
- [J03：按模式解释类型码及未知类型回退][J03]
- [J04：普通结果、OUT 与 cursor 响应分派][J04]
- [J05：reset 与预处理缓存清理][J05]
- [J06：LOB 辅助连接路径][J06]
- [J07：Java 故障切换中的查询重放判定][J07]
- [J08：PrepareExecute 与 offset fetch 的条件][J08]
- [J09：批量与 array binding 分派][J09]
- [J10：可更新结果集的 DML 和行定位][J10]
- [J11：两种模式的 XA 实现][J11]
- [J12：OB20 完整链路追踪字段与控制][J12]
- [J13：CLIENT_FOUND_ROWS 与 Java 的 useAffectedRows 策略][J13]
- [J14：MySQL emulateLocators 与原生 BLOB 分支][J14]
- [J15：模拟 BLOB 的表/主键/列身份及分块 SQL][J15]
- [J16：明文密码插件的 SSL 要求与握手检查][J16]
- [J17：Oracle RETURNING 对 PrepareExecute 的明确要求][J17]

Go 标准库契约；链接固定到 Go 1.27.2 源码，设计接口范围限定为 Go 1.25 已有 API：

- [G01：ErrBadConn 的重复执行约束][G01]
- [G02：database/sql 对 ErrBadConn 的重试][G02]
- [G03：driver.Value 和子游标][G03]
- [G04：NamedValueChecker 契约][G04]
- [G05：Go 1.27 新增 RowsColumnScanner，本设计不依赖][G05]
- [G06：context.AfterFunc 的停止与竞态规则][G06]
- [G07：Scanner 和 sql.Out][G07]
- [G08：ResetSession 与 Validator][G08]
- [G09：Raw 回调不能向外泄漏 driverConn][G09]
- [G10：sql.Result 及标准库对 driver.Result 的包装][G10]

[J01]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823
[J02]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L470
[J03]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java#L326
[J04]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L2483
[J05]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L152
[J06]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseConnection.java#L2733
[J07]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/failover/AbstractMastersListener.java#L445
[J08]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778
[J09]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ServerPreparedStatement.java#L701
[J10]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4UpdatableResultSet.java#L1125
[J11]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseXaResource.java#L162
[J12]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/flt/FullLinkTrace.java#L69
[J13]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L312
[J14]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ResultSet.java#L2154
[J15]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/BlobFromLocator.java#L90
[J16]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/authentication/ClearPasswordPlugin.java#L80
[J17]: https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ServerPreparedStatement.java#L328
[G01]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L152
[G02]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L1579
[G03]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L49
[G04]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L404
[G05]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L458
[G06]: https://github.com/golang/go/blob/go1.27.2/src/context/context.go#L315
[G07]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L452
[G08]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/driver/driver.go#L299
[G09]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L2072
[G10]: https://github.com/golang/go/blob/go1.27.2/src/database/sql/sql.go#L3581
