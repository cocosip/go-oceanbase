# OceanBase Go 驱动架构设计

状态：架构与接口契约，分项本地实现及验收状态以开发进度为准。本文面向完整功能适配，不以基础 CRUD 作为最终范围。

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
| 连接声明 | `Interactive` 默认 false；`ConnectionAttributes` 默认 nil，仅显式提供应用元数据；见 §6.1 |
| 初始数据库 | `CreateDatabaseIfMissing` 默认 false；仅新 MySQL 模式物理连接可显式创建并选择非空 Database，见 §6.4 |
| 读取预算 | `ReadTimeout` 默认0，关闭独立读取超时；正值限制每次底层网络 Read，context 的较早期限仍生效，见 §12.5.1 |
| 模式 | `ModeAuto` 为零值；`ModeMySQL` / `ModeOracle` 是实际模式校验约束 |
| 传输 | `TLSConfig *tls.Config`、`DialContext`；TCP 为默认；nil 不主动启用 TLS，非 nil 必须成功验证 TLS，禁止降级；明文凭据限制见 §17.2 |
| 凭据/认证 | 外部 Credentials 回调、按 Connector 配置的认证插件工厂；未提供回调时使用静态 User/Password/ProxyUser；代理登录见 §6.3 |
| 协议 | `Config.Protocol`：Compression、OB20、Checksum、ProxyChecksum、NewMetadata；零值 FeatureAuto，另有 FeatureOff/FeatureRequired；以建连协商结果为准 |
| 执行 | 自动选用服务端预处理；显式支持客户端预处理、合并执行、缓存和批量策略；`AllowMultiStatements` 默认 false；`RowsAffectedMode` 默认 `MatchedRows`，见 §11.4 |
| 会话 | 字符集、NCHAR 字符集、Location、会话初始化设置、Managed/Full 两种 reset 策略 |
| 资源 | 最大逻辑消息、LOB 物化上限、FetchSize、`MaxResultItems`、关闭/清理时间上限、缓存容量 |
| HA | Config.Routing：静态地址、固定角色、组内策略、单次/总建连预算、有界黑名单；ValidationInterval 可选复用前探活；见 §17.1.1 |
| 诊断 | 慢请求阈值、受限包追踪回调、完整链路追踪；日志开关由 Logger 是否为 nil 决定 |

建议初始资源默认值：逻辑消息上限 64 MiB、单个 LOB 自动物化上限 16 MiB、游标 fetch 128 行、一次 Execute 的结果摘要上限 `MaxResultItems=1024`、清理时间上限 1 秒。具体限制可以配置；超过限制返回明确错误，不截断后宣称成功；大对象提示使用流式入口。物理分包长度和逻辑消息上限是两个不同概念。

认证配置现有接口为 `Credentials func(context.Context) (Credentials, error)`、`AuthOptions{RSAKey, Plugins}`。Plugins 按协议插件名映射到每次认证的工厂，返回实现 Security/Respond 的插件。RSAKey 使用调用方固定的公钥且在 Connector 中复制，禁自动检索。回调、插件工厂及外部 TLS 对象内的回调/私钥对象需满足并发安全与 context 契约。接口的安全边界见 [兼容矩阵](go-driver-compatibility-matrix.md)。

配置规则：

- DSN 使用 `net/url` 的转义规则；参数名严格校验，未知项报错，敏感重复项报错。
- `mode` 等与行为有关的参数不得静默忽略；错误信息不得包含完整 DSN 或密码。
- logger、函数回调、TLS 对象通过 Go 配置传入，不用全局注册表或字符串名称寻找实例。
- Connector 复制切片和 map，并 clone TLS 配置；公开 cfg 后续修改不改变已建立 Connector。外部回调和 logger 自身需满足各自并发契约。
- 不提供允许任意 Java 参数透传的 `map[string]any`。每个公开选项必须对应已有功能和明确语义。

### 6.1 D32 已实现的连接属性与交互标志

`Config.Interactive` 对应 Java `interactiveClient`，两者默认均为 false；DSN 使用 `interactive=true/false`。启用时发送 CLIENT_INTERACTIVE 客户端请求位，不要求 greeting 先声明该位；实际空闲超时由服务端的 interactive_timeout 配置决定，不改变 Go context 或查询超时。

`Config.ConnectionAttributes map[string]string` 仅程序化提供，默认 nil；键和值使用 Go UTF-8 字符串，由驱动快照并按 `Session.Charset` 严格编码，握手和 ChangeUser 共用该路径。允许空值，拒绝空键、NUL、无效 UTF-8、不可表示字符和编码后键冲突。以下划线开头的键及 `ob_capability_flag` 保留给协议，不允许借此覆盖能力位或代理身份；代理登录使用独立的 §6.3 身份字段。

属性区（包含驱动声明、键值及全部长度前缀）累计受 `Limits.MaxMessageBytes` 限制；编码前按字符数检查最小空间，编码后逐项扣减预算，避免 GBK 收缩误拒与无界聚合。完整握手仍受原有消息限额约束。配置错误只返回字段名；目标未声明 CLIENT_CONNECT_ATTRS 时，显式非空属性在请求凭据前返回 ErrUnsupported，不静默丢弃。

Go 不自动采集 Java 的 PID、线程、OS、主机等动态属性，因此 Java `sendConnectionAttributes`/默认属性排除列表映射为调用方控制该 map 的内容，而不是复制全局开关。nil/空 map 不发送应用属性，驱动协议声明及显式代理身份仍由各自配置产生。属性不会被驱动日志或诊断回调输出；是否保护传输仍遵循 `TLSConfig`，启用 TLS 时属性只存在于验证成功后的握手响应内。

本地覆盖两模式、UTF-8/GBK、GB18030 扩展编码、长度前缀阈值、配置快照、保留键、能力缺失、TLS，以及普通/压缩/OB20 的 ChangeUser 重发；不代表已验证服务端属性表可见性或 interactive_timeout 的实际效果。

### 6.2 D32 已实现的连接排序规则

`SessionOptions.Collation string` / DSN `collation` 对应 Java `connectionCollation`；Go 零值为空，Java 默认 null，均不额外选择自定义排序规则。非空值先按原始 ASCII 标识符校验（最长128字节），再转小写；拒绝空白、引号、分隔符、NUL、非 ASCII 和无效 UTF-8，不用字符串拼接承载任意 SQL。

MySQL 兼容模式初始化使用 `SET NAMES <Charset> COLLATE <Collation>`，以同一命令设置 client/connection/results 字符集和 connection 排序规则。排序规则不擅自改写调用方 Charset，未设置 Charset 时仍默认 utf8mb4；名称是否存在、是否匹配该字符集由目标服务原子校验，失败保留服务端错误并关闭连接，不回退成默认规则。依据为固定 Java `AbstractConnectProtocol.setCollationVariables` 的模式条件，以及 [OceanBase v4.3.5_CE MySQL 语法](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/sql/parser/sql_parser_mysql_mode.y) 的 `NAMES charset_name_or_default opt_collation`。

执行 Session.Variables/InitSQL 后复用同一设置恢复基线，ChangeUser 初始化和 Full reset 的新连接继续应用该配置；Managed reset 的职责边界不变。驱动编码始终由 Charset 决定，结果解码按列的实际 collation ID，不从名称猜测字节编码。

Java 在 Oracle 兼容模式忽略该选项，Go 显式配置则拒绝：已指定 ModeOracle 时 NewConnector 返回 `ConfigError{Field:"Session.Collation"}`；ModeAuto 认证后识别为 Oracle 时，在任何初始化 SQL 前返回 ErrUnsupported。此字段不替代 Oracle 的 NLS 设置。

本地覆盖配置/DSN、UTF-8/GBK/utf8mb3 别名、初始化恢复、ChangeUser、Full reset 替换连接、普通/压缩/OB20、服务端错误及 Oracle 拒绝。`ExampleSessionOptions_collation` 可运行且不建立网络连接。四目标 TestSessionCollation 集成源码已接手动 CI：MySQL 正向验证会话状态、比较效果、预处理 Unicode 和重建基线，Oracle 验证配置拒绝；本阶段仅编译，不提供真实效果或版本兼容承诺。

### 6.3 D32 已实现的代理用户登录

`Config.ProxyUser string` / DSN `proxyUser` 是 Oracle 兼容模式的代理目标，`User`/`Password` 始终是认证账户及其密码。零值为空，关闭代理登录，对应 Java `useProxyUser=false`；非空直接给出目标，不解析 Java `padmin[admin]@tenant` 用户名语法，不自动大小写折叠或去引号。驱动不生成代理授权 SQL，目标身份及授权关系由服务端确认。

`Credentials` 增加同名 `ProxyUser` 字段；凭据回调每次完整替换 User/Password/ProxyUser，包括用空 ProxyUser 清除静态目标。静态字段仍须通过 NewConnector 配置校验，回调不用于绕过错误配置（例如 ModeMySQL 的非空静态目标）。构造该公开结构体应使用命名字段；已有非命名双字段字面量需要改为 `Credentials{User: user, Password: password}`，不能把新增字段称为对该用法的源码兼容。字符串配置快照、回调并发/context 契约、密码与 TLS 门禁沿用原规则。

目标按 Session.Charset 严格编码到专用 `__ob_client_proxy_user_name` 属性，拒绝 NUL、无效 UTF-8、不可表示字符；与应用属性、协议声明和全部长度前缀共用属性区预算，并继续受整条认证消息的上限约束。应用 map 不能覆盖该键。静态配置在 NewConnector 校验，动态凭据在创建认证插件/发送响应之前校验；启用 TLS 时仅在成功验证后的认证响应中发送目标。

目标非空而没有 CLIENT_CONNECT_ATTRS 时返回 ErrUnsupported。静态目标且无回调时在请求凭据前拒绝；有回调时先取得完整凭据，允许它清除目标，仍非空才拒绝。显式 ModeMySQL 在配置/ChangeUser 预检拒绝；ModeAuto 最终识别 MySQL 时在初始化 SQL 前返回 ErrUnsupported 并关闭连接。服务端拒绝（包括1235）保留原错误，不自动重试普通登录；属性能力和认证 OK 不是独立的代理目标证明，实际身份仍需目标联调验证。

ChangeUser 每次显式传入代理目标，空值清除，不继承旧目标或调用原凭据回调。成功后记录认证用户与代理目标，重新初始化会话；默认 Oracle schema 仍按认证用户名的 @ 前部分构造，与固定 Java sendChangeUser 路径一致，代理目标不隐式替代显式 Database。旧句柄/CancelTarget 失效、失败淘汰和切换后不回原池的规则不变。新池连接仍使用原 Connector 的目标。

CancelQuery 除原有入口、认证用户、模式、TLS 与连接 ID 检查外，还比较两条连接认证成功时的代理目标；辅助凭据回调返回不同目标或清除目标时，拒绝发送 KILL。拒绝不会改变原查询的身份或自动重放业务。用户名/目标不写入驱动日志或诊断。

固定源码依据：`Credential.java` 的目标分离、`SendHandshakeResponsePacket.java` 的专用属性（L88、L399）和 ChangeUser 重发（L280）、`ConnectionOracleTest.testProxyUser` 的代理与实际用户断言。`TestProxyUser*` 覆盖配置、DSN、动态覆盖/清除、能力/模式/编码/预算、TLS、普通/压缩/OB20 的 ChangeUser、池隔离及取消身份。外部 ExampleNewConnector_proxyUser 可离线运行。TestProxyUserSessions 提供 Oracle 直连/代理的真实身份、切换/清除与新池恢复用例，MySQL 验证配置拒绝；本阶段仅编译。

### 6.4 D32 已实现的初始数据库创建

`Config.CreateDatabaseIfMissing` / DSN `createDatabaseIfMissing=true/false` 对应 Java `createDatabaseIfNotExist`，默认均为 false。必须显式开启并指定非空 Database；名称按 Session.Charset 严格编码，拒绝 NUL、无效 UTF-8 和不可表示字符，不在配置错误中回显名称。显式 ModeOracle 在 NewConnector 拒绝；ModeAuto 认证后实际为 Oracle 时在初始化 SQL 之前返回 ErrUnsupported 并关闭，不像 Java 静默忽略创建请求。

开启后初始握手不声明 CLIENT_CONNECT_WITH_DB，不发送数据库名，也不要求服务端支持该握手位；完成认证、模式检查与协议协商后，在同一物理连接建立字符集/自动提交/版本/时区基线，再发送 `CREATE DATABASE IF NOT EXISTS` 和 COM_INIT_DB，最后运行 Session.Variables/InitSQL。标识符使用反引号并对内含反引号双写，名称不作为 SQL 值或未转义片段拼接；COM_INIT_DB 使用原始名称的连接字符集编码。关闭时沿用普通握手选库语义。

创建与选库服从同一个 ConnectTimeout/路由尝试预算，保留服务端错误；失败关闭连接，不重试其他地址或回退普通登录。DDL 可能已生效，初始化失败、取消或断连不会自动删除数据库；IF NOT EXISTS 也不保证无 CREATE 权限的账户能连接既有数据库，必须按服务端权限处理。驱动不为数据库指定额外默认字符集/排序规则，沿用服务端 CREATE 默认值。

池中新物理连接/Full reset 替换会再次执行 IF NOT EXISTS；普通复用不重复执行。ChangeUser 的派生配置清除此选项，仅按其 database 参数重新认证，不自动建库；CancelQuery 辅助连接也清除此选项，正常选库后直接执行取消，不产生建库副作用。原 Connector 配置不受这些局部清除影响，不新增包或全局状态。

固定 Java 依据：AbstractConnectProtocol 的 CLIENT_CONNECT_WITH_DB 条件（L337–L342）和 additionalData 创建/USE 顺序（L1378–L1386）。本地 TestCreateDatabase* 覆盖握手、三种 framing、UTF-8/GBK、转义、初始化顺序、两模式拒绝、服务端失败/总预算与 ChangeUser/取消隔离。TestCreateDatabaseSessions 提供 MySQL 直连/代理的创建、既有库、InitSQL 和物理替换用例，要求专用 CREATE/DROP DATABASE 权限 fixture；Oracle 只验证配置拒绝，本阶段均不联网。

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

### 9.0.1 D28 已实现的 TCP、Unix 与 Named Pipe

`Config.Network` 默认 `tcp`；`unix` 使用 `net.Dialer.DialContext` 连接 `Addr` 中的 Unix socket 字面地址，路径不做环境变量或用户目录展开。`pipe` 在 Windows 使用 `github.com/Microsoft/go-winio v0.6.2`，Addr 为显式 UNC Named Pipe 名称。已建立管道的收发使用 overlapped I/O 和 deadline；拨号时检查 context，并在管道忙时轮询。非 Windows 的内置 pipe 拨号返回 ErrUnsupported；调用方自定义 DialContext 仍可以接管相同 network/address。平台实现以 build tags 隔离，不加载额外本地库；直接使用 `golang.org/x/sys/windows` 识别管道暂时不存在的错误，让其进入已有重试流程，权限错误仍直接返回。

IPC 是一个显式固定端点，拒绝与 Routing.Endpoints 或 Discovery 混用；次数、单次/总建连预算、初始化、空闲验证与后续命令继续使用既有流程。不会因 IPC 失败自动回退 TCP。TLSConfig 非 nil 时，IPC 必须显式指定证书 ServerName，不能把文件路径或管道名当证书主机名；nil TLS 不构成明文密码豁免。DialContext 收到实际 Network 和 Addr，并继续承担 context/失败资源所有权契约。

DSN 使用 `network=unix|pipe&socket=转义后的地址`；必须成对出现，tcp/默认网络禁止 socket。URL authority 不再是 IPC 拨号地址；database path 仍是数据库名。程序化 Config 直接指定 Network/Addr，无需伪造 TCP 主机。解析后再次传入 NewConnector 的配置保持可用。

Unix 的实际目标取决于 Go/OS 的 AF_UNIX 支持，Linux 抽象地址也由 Go/OS 处理；本机 Windows 的文件 socket 已运行测试。Named Pipe 已运行本机 Windows 对端；远端 UNC 还需对应部署和权限验收。go-winio 底层打开管道使用同步 CreateFile，远端 SMB 打开若阻塞，context 无法中断该调用，ConnectTimeout 无法作为其严格墙钟上限；调用返回后驱动仍检查取消并关闭已取得连接。两者均有本地协议握手、基线、Ping、QUIT/关闭及明文门禁测试，不等于真实 OceanBase/OBProxy 已暴露相应端点。Shared Memory、OBProxy VC 继续独立开发，不计入此处支持声明。

### 9.0.2 D32 TCP 配置的 Go 原生承载

TCP 的七个 Java 配置项通过现有 `Config.DialContext` 与 Go 标准库完成适配，不增加 TCPOptions、第二套拨号器接口或全局工厂。回调收到当前候选入口的 network/address 和剩余建连 context，返回原始 net.Conn；驱动随后执行相同 TLS、模式协商和初始化，回调不能通过包装连接取得明文认证豁免。回调及其捕获的 Dialer/地址由调用方管理，Connector 不复制函数内部对象，应在使用期间保持配置不可变、允许并发拨号。

| Java option | Go 承载和默认差异 |
| --- | --- |
| tcpNoDelay | `(*net.TCPConn).SetNoDelay(bool)`；Go 1.25/1.26 的内置 TCP 连接默认 true，与 Java 相同 |
| tcpAbortiveClose | `SetLinger(0)` 显式开启，`SetLinger(-1)` 保持正常后台发送/关闭；默认不强制 RST，与 Java false 对应 |
| localSocketAddress | `net.Dialer.LocalAddr = &net.TCPAddr{IP: localIP}`，端口0由系统分配；这是 TCP 源接口绑定，不是 Unix socket 地址 |
| tcpKeepAlive | `net.Dialer.KeepAliveConfig`；禁用可用 KeepAlive=-1 且 KeepAliveConfig.Enable=false。Go 默认尝试启用，idle/interval 各15秒、count9；Java 默认 true，但没有在此项固定相同探测参数 |
| tcpRcvBuf / tcpSndBuf | `SetReadBuffer` / `SetWriteBuffer`；内置驱动保持 OS 默认/自动调优，不硬编码缓冲大小；OS 可调整实际值 |
| socketFactory | `Config.DialContext` 函数注入；不解析 Java 类名、反射加载或注册进程级工厂 |

完整[可编译示例](../tcp_example_test.go)使用 LocalAddr/KeepAliveConfig、TCP_NODELAY、读写缓冲和 linger，并检查 setter 错误、在失败时关闭已取得连接。示例的64KiB缓冲与30秒/10秒/3次 keep-alive 只是应用配置示范；内置默认不因此改变。net.Dialer 对 keep-alive 设置采用 best effort，若应用必须确认显式策略，应在取得 TCPConn 后调用 SetKeepAliveConfig 并检查错误，示例已包含此步骤。需要在 SYN 之前设置 socket option 时使用标准 `net.Dialer.ControlContext`，其原生选项与支持条件由平台决定。

`SetLinger(0)` 可能丢弃待发送数据并使对端观察到连接重置；它不证明 COM_QUIT 已送达，也不回滚已执行/提交的 SQL，不能作为防重放或未知结果恢复策略。示例默认关闭此选项，独立用例只检查显式关闭终止本地传输，不强求跨平台相同的 RST 错误码。keep-alive 探测也不替代 ReadTimeout、调用 context、池空闲/生命周期配置或业务状态确认。

这些策略只通过程序化 Config 注入，七个同名 Java DSN 参数继续明确拒绝，不静默透传。TCP 专用示例对 unix/pipe 返回 ErrUnsupported，不回退 TCP；原有 IPC 实现保持独立。TestTCPDialContext* 在本地原生 TCP 对端验证源地址、选中入口、单次工厂调用、握手/初始化/Ping/QUIT、明文认证拒绝、取消及显式 abortive close；ExampleConfig_tcpOptions 本身不联网。实际远端接口、OS 缓冲/探测行为与数据库部署仍待验证。

固定 Java 依据：DefaultOptions 的 tcpNoDelay/tcpAbortiveClose/localSocketAddress（L100–L116）、tcpKeepAlive/tcpRcvBuf/tcpSndBuf/socketFactory（L225–L249），以及 AbstractConnectProtocol.createSocket（L249–L285）。默认 Go 行为以1.25/1.26标准库 net.Dialer/newTCPConn 为依据；不把 Java 选项有对应 Go API 当作真实网络验收已完成。

### 9.1 已实现的包流与协商（D23）

`ProtocolOptions` 的六项策略及 DSN `compression`、`ob20`、`checksum`、`proxyChecksum`、`newMetadata`、`fullLinkTrace` 均只接受 `auto|off|required`。配置非法或互相冲突时在 Dial 前拒绝。策略只在建连阶段选择，业务执行后不换协议、不重放。

| 能力 | 选择和行为 |
| --- | --- |
| Compression | greeting 中 CLIENT_COMPRESS 与客户端策略的交集；认证最终 OK 后使用 zlib 外层 7 字节头；压缩无收益则发送未压缩帧 |
| OB20 | CONNECT_ATTRS 声明代理位 8/15，最终认证 OK 的 session tracking `ob_capability_flag` 位 8 确认；优先于可选压缩；CompressionRequired 不请求 OB20 |
| Checksum | OB20 CRC16 头与零初始、无取反 CRC32C 负载校验；auto 发校验并验证收到的非零校验；off 不发校验，但收到非零校验仍验证；required 要求 OB20 且对零校验同样比较实际值 |
| ProxyChecksum | 对应 Java `useObChecksum` 的代理能力位 3/6，与 OB20 CRC 独立；auto 请求，required 必须确认两位，off 不声明且拒绝非零回显；驱动不因此宣称已验证代理到后端的校验 |
| NewMetadata | 客户端属性位 1（数值 2）请求，`__ob_client_capability_flag` 确认；列包布局不变；Oracle NCHAR/NVARCHAR2 长度为字符数，RAW/ROWID 长度来自 precision，LOB 长度未知；不照搬 JDBC 的 Java 类和显示宽度规则 |
| FullLinkTrace | D25；OB20 + FLT 位14，支持新 extra 位15及 ShowTrace 位18；日志独立，详见 §18.3.1 |

未确认的 auto 能力保持标准行为；required 缺失、off 却被启用及非法能力回显均使建连失败。CompressionRequired 与 OB20Required/ChecksumRequired/FullLinkTraceRequired 冲突；OB20Off 与 ChecksumRequired/FullLinkTraceRequired 冲突。新属性保留已实现的 locator-v2 位，不请求尚未实现的 extra-OK、session sync 或路由；D25 FLT 协商见 §18.3.1。

两层大小独立：外层长度及 zlib 解压长度均由 24 位字段约束，最多 `0xffffff` 字节；OB20 外层体另含 28 字节开销。一个外层帧可包含多个逻辑消息；每个逻辑消息继续单独执行 `Limits.MaxMessageBytes` 检查，不能将聚合帧误算作一条消息。解压输出最多读取声明长度加一，并拒绝长度不符、损坏流和尾随压缩数据；内层跨帧头、跨 16 MiB 续包及空终止包共用原逻辑消息状态机。

OB20 每条命令重置双层序号，24 位 request ID 递增并回绕；接收校验 connection ID、request ID、序号、标志及新 extra 的 TLV 长度。未知有界 extra 安全跳过；旧 extra 按总长度跳过，不假称已处理其业务字段。所有损坏、部分写或 I/O 中取消使连接失效；错误保留实际网络读写字节数。已有未消费帧数据时不能通过重置开始另一条命令。外层 last 标志不替代内层完整响应边界。

当前 compression/OB20 下批量 multi-send 在发送前回退逐项，`BatchResult.Strategy` 报告 Sequential。未来只有为各在途命令维护独立包流身份并验证等价性后才启用流水线。

源码依据：[握手与两类 checksum 属性](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L361)、[最终 OK 及包流切换](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823)、[OB20 布局](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/flt/OceanBaseProtocolV20.java)、[新元数据解释](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/ColumnDefinition.java#L443)。本地协议验证与真实兼容验收分开，见兼容矩阵 §14。

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

### 10.1.1 已实现的可选执行策略（D24）

通过 `Config.Execution ExecutionOptions` 显式启用；零值保留原有分开 prepare/execute、不缓存、每次发送类型头、不发送 SQL checksum。普通业务仍使用 `database/sql`，没有第二套连接池或 Statement API。

```go
cfg.Execution = oceanbase.ExecutionOptions{
    PrepareExecute:         true,
    StatementCacheSize:     64,
    StatementCacheSQLLimit: 1024,
    ReuseParameterTypes:    true,
    StatementChecksum:      true,
}
```

五项 DSN 为 `prepareExecute`、`statementCacheSize`、`statementCacheSQLLimit`、`reuseParameterTypes`、`statementChecksum`；布尔只接受 `true|false`，负缓存限制在拨号前拒绝。缓存容量 0 关闭；SQL 长度上限 0 归一为 1024，按原 Go SQL 字符串的 UTF-8 字节数计算。

| 策略 | 已实现选择与边界 |
| --- | --- |
| 通用 PrepareExecute | Oracle 兼容模式、显式启用且实际 ob_version >= 2.2.76；普通带参数调用直接用 ID 0 合并，后续复用取得的 ID；显式 sql.Stmt 保留标准 Prepare 与 Close 所有权 |
| 能力缺失 | MySQL、未知或旧实际版本在发送前保留标准 prepare/execute；不把 greeting 版本当作实际 OceanBase 版本。显式客户端预处理保持客户端路径 |
| OUT 与长参数 | D14 必需 OUT 合并协议不受通用优化开关影响；长参数先取得 ID 再上传，普通执行保留标准 execute；原生 OUT/游标使用其必需扩展命令。没有上传后的重新选择或业务重放 |
| SQL checksum | 原始零初始、无取反 CRC32C，输入为实际发送字符集编码后的重写 SQL；标准 execute 仅 Oracle 且实际版本 >= 4.1.2，占原 iteration 字段；合并命令放尾部专用字段，iteration 仍为 1。与 OB20/代理 checksum 独立，批量和游标的标准 execute 应用同一门禁 |
| 类型头复用 | 同一 statement、完整响应成功后才保存类型向量；类型含 unsigned 位及复杂描述字节，任何变化或执行失败后重发。只保留类型/描述，不保留上次参数数据；NULL bitmap 和值仍逐次编码 |
| 自动 statement 缓存 | 每物理会话各有有界 LRU；仅完整成功且无资源租约的自动 SELECT/INSERT/UPDATE/DELETE/REPLACE 回收。SQL 文本为键，借出的 handle 不在空闲缓存内；失败、超长、驱逐及连接关闭释放服务端 ID |
| 会话变化 | 普通文本及 OUT 路径开始前清空空闲缓存；非缓存类 prepared SQL 同样清空，NO_BACKSLASH_ESCAPES 变化拒绝复用。Full reset 淘汰物理会话，旧缓存和 ID 不迁移到新连接 |
| 过程及连接资源 | CALL/匿名块不进入透明 LRU，避免过程会话副作用使旧缓存失效；重复调用使用显式 sql.Stmt，由标准库和调用方管理寿命。OpenBlob/OpenClob、普通复杂结果、REF CURSOR 和 Execution 摘要沿用完整响应及同会话资源边界 |

不另建 Java 式 callable facade、全局 SQL 字符串缓存或共享服务端 ID。发送后失败直接报告，SQL 错误不回退另一种策略；响应损坏、取消及传输失同步淘汰连接。本地字节协议测试与编码 benchmark 不证明真实数据库性能或版本兼容；四目标 `TestExecuteStrategies` 源码及 CI 入口已提供，当前只编译。

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

复杂值的普通 Scan 表示需自描述：格式版本、schema/type 名称、字段顺序与类型、NULL、精确数值、时间和二进制都有明确标记。已采用 `encoding/json` 实现的 format=1 带类型标签表示，数值以十进制文本保存、二进制以 base64 保存；`Object.Scan`/`Array.Scan` 负责解析。它是公开、文档化的类型表示，不是进程内句柄或对未知类型的兜底。

输入 Object/Array 保留明确的类型名和字段顺序；不将任意 struct 反射成数据库对象，不将任意 slice 自动当作 Oracle 集合。类型描述缓存以会话模式、schema、type 名称及可得版本为键；DDL 或类型失配后刷新描述，业务执行不盲目重放。

### 11.2.1 已实现的标量约定（D12）

公开类型使用轻量值：`Number`、`NString`、`RowID`、`TimestampTZ`、`TimestampLTZ`、`IntervalYM`、`IntervalDS` 的底层为 string，`Raw` 为 []byte；均实现 Scanner/Valuer。NULL 使用 `sql.Null[T]` 或 nil 指针；Raw 的 nil 表示 NULL，非 nil 空切片表示空值。驱动在通用 Valuer 之前识别这些具体类型及其 `sql.Null[T]`，保留输入协议身份；普通 Scan 仍只输出标准 driver.Value。Oracle 是否将空字符串/空 RAW 归为 NULL 由服务端决定，驱动不伪造空值。

Number 验证十进制与指数文本，不接收 float64 来重建精确值，也不实现运算。输入按精确 DECIMAL/NUMBER 编码；指数仅在有界展开后发送，避免 MySQL 将其当作 DOUBLE。超界、非法编码或无法无损表达的转换返回错误，不截断或替换。RowID 保留行标识文本，Oracle 输入沿用字符串参数；客户端显式构造 CHARTOROWID。

时间默认保留文本；`Session.ParseTime`（DSN `parseTime`）默认 false，启用后仅将无时区 DATE/DATETIME/TIMESTAMP 解析为 time.Time，依据明确的 `Session.Location`。默认 UTC，建连实际设置会话 time_zone；Variables/InitSQL 完成后重新应用连接字符集、时区及 NCHAR 基线，使编解码一致。业务执行期间如自行修改这些设置，必须保证与 Config 一致；Managed reset 不解析任意用户 SQL 来追踪这些变化。零日期、DST 缺口/重叠在解析为瞬间时返回 ErrType，TIME 和 YEAR 保留原有持续时间文本/整数语义。

带时区值的文本例为 `2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT`。偏移必需，region/缩写可选；已知 region 与偏移不一致时拒绝。未知 region 原文和显式偏移保留，Time() 使用该偏移，不猜测 region。LTZ 原生字段只有会话墙上时间；固定偏移会话追加偏移，具转换规则的会话追加 region，例如 `2024-11-03 01:30:00.000000001 America/New_York`。其 Time() 在歧义时拒绝；同会话 region 可原样绑定，本机瞬间转换到歧义 LTZ 墙上时间也拒绝，防止丢失 fold。需要指定 fold 时使用 TimestampTZ 绑定，由服务端转换到 LTZ 列。

Oracle 时间协议保留纳秒与原有精度；普通 time.Time 输入使用原生 TIMESTAMP(9)，MySQL 协议只支持微秒，超过精度明确报错。IntervalYM/DS 保留符号、最高九位年/日与最高九位秒小数，不换算成 time.Duration。二进制和文本行中的 Oracle 扩展时间均按原生字段体解码，不能把它误当成字符文本。

字符集叶包使用 x/text v0.31.0；连接 SQL、预处理 SQL、参数、结果和列名均进行相应编解码。已实现映射与限制见[兼容条件矩阵](go-driver-compatibility-matrix.md)。NString 使用 NCharset；未显式指定时回退到连接 Charset。UTF16 仅用于 NCHAR/结果，不能作为 SQL 客户端字符集；未知编码/collation 明确拒绝。所有语法判断在 Go UTF-8 文本上完成，然后编码；不在 GBK 字节中寻找引号或占位符。

### 11.2.2 已实现的 OBJECT/ARRAY 约定（D18）

公开值均位于根包，不增加公开 codec/descriptor 包：

```go
type Attribute struct {
    Name, Type string
    Value any
}
type Object struct {
    Schema, Type string
    Version uint64
    Fields []Attribute
    Null bool
}
type Array struct {
    Schema, Type string
    Version uint64
    ElementType string
    ElementVersion uint64
    Elements []any
    Null bool
}
```

Schema、Type、字段 Name/Type 使用字典精确拼写；Schema 省略时在原会话查询 CURRENT_SCHEMA。Object 输入按字典字段顺序逐项检查，Attribute.Type 可省略；不反射任意 struct 或 slice。Array.Type 为空表示匿名集合，ElementType 是内置类型或显式对象类型；匿名对象集合的 Schema/ElementVersion 描述元素。具名集合从字典解析元素类型。Version/ElementVersion 来自协议；0 表示服务端当前版本，不从字典虚构数字版本。

`Null=true` 表示整个 SQL NULL；普通 nil/空 Elements 均表示空集合，元素 nil 保留为元素 NULL。Object/Array 均实现 Scanner/Valuer，Scan(nil) 设置 Null；失败的 Scan 不修改目标。标准查询返回 format=1 JSON 字符串，包含 kind、schema、type、version、null、fields（name/type/value）或 elementType、elementVersion、elements。空 fields/elements 可省略，扫描后规范化为空容器。NUMBER 使用十进制字符串，RAW/BLOB 使用 base64，DATE/TIMESTAMP 使用墙钟文本，子对象/集合使用同格式嵌套文档，元素顺序与字段顺序不变。Valuer 提供该无损表示；本驱动先由 NamedValueChecker 保留值身份，再发送原生复杂参数，不把 JSON 当作服务器对象值。

原生绑定与结果支持 Oracle 兼容模式，使用 COMPLEX(160) 及服务端预处理二进制结果。对象参数头携带 schema/name/version；集合参数头为匿名头和元素协议类型，对象元素另携带其类型名/版本。文本结果缺少可验证的复杂值表示，明确 ErrUnsupported；客户端插值不生成复杂值字面量，已发送操作不会为切换协议而重放。MySQL 兼容模式的原生复杂输入同样明确拒绝。

已实现字段输入为 NUMBER/INTEGER/DECIMAL/NUMERIC、CHAR/VARCHAR/VARCHAR2、NCHAR/NVARCHAR2、RAW、DATE 及嵌套对象/集合；非 NULL TIMESTAMP/LOB 字段输入没有已确认的写协议，返回 ErrUnsupported。内置集合元素头当前仅支持数值、CHAR/VARCHAR/VARCHAR2、RAW、DATE；NCHAR/NVARCHAR2/TIMESTAMP/LOB 集合即使为空或全 NULL 也在发送前拒绝。结果支持以上基础字段以及 TIMESTAMP、BLOB/CLOB/NCLOB 的有界内容物化。未支持的字段/元素类型明确报错，不兜底成未知字节或伪造成功；这些边界尚不能作为全部复杂类型的真实兼容验收。

物化前先有界缓冲整个剩余响应，包括后续结果集和隐藏 OUT；完成响应后才能同会话查字典或读取嵌套 LOB。Close/Exec 排空同样执行这条路径，Close 的清理 context 传到最终复杂 OUT 解码，错误时不发布 OUT。根复杂值共享剩余预算，节点、字段元数据、普通值和嵌套 LOB 累计计入；LOB 读取前检查剩余额度，RAW/BLOB 计入 base64 膨胀，最终 JSON 及整响应再次受限。最大嵌套深度 32、节点 65536、JSON 16 MiB，并与更小的 MaxMessageBytes、MaxLOBBytes 同时约束；不通过扩大单个 LOB 预算绕过总量限制。

描述复用 D13 的类型/属性/集合/同义词查询计划。优先实体类型，再 private/PUBLIC 同义词；拒绝远程 DB_LINK、循环和超深依赖。缓存仅存在于一个物理会话的单个业务命令内，以模式/schema/name/协议版本为键；每次业务命令和 reset 清空，不跨权限/schema 变化复用。帧解析与描述失配时清缓存并仅重新解析已缓冲字节，业务 SQL 不重放；相同形状下的类型语义变化仍需目标服务端版本/字典联调验证。描述查询累计响应也受 MaxMessageBytes 限制。

上述能力已有本地协议/标准 database/sql 回归；`TestComplexTypes` 仅编译，Oracle/OBProxy 的类型定义、同义词、协议版本和原事务可见性尚未联调。


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
- Close 幂等，有界关闭/排空 Rows 并完成摘要、OUT 和资源发布。它返回读取、最终化或清理的错误，即使 Rows 已自动关闭，也不能丢失此前错误。取消、超时或清理失败时标记未完成结果，清理未发布资源并淘汰无法同步的会话。调用方停止行迭代后调用 Close，检查 Execute 错误、Rows.Err、Close 错误和最终 Snapshot。
- Snapshot 只复制驱动内存状态，可与行迭代并发调用；它不查询标准 Rows。标准库内部事务取消没有向驱动公开，显式 Commit/Rollback 自动关闭未读 Rows 时，其取消错误由 Execution.Close 读取 Rows.Err 并合并到最终摘要，保留此前驱动错误和原始超时原因。Done 表示驱动已完成收尾；取得包含标准库错误的最终摘要以 Close 为边界。
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

### 12.5.1 D32 已实现的网络读取超时

`Config.ReadTimeout` / DSN `readTimeout` 使用 Go duration，零值关闭，负值/无效值拒绝；例如 `readTimeout=5s`。对应固定 Java `socketTimeout` 的 SO_TIMEOUT 语义，不接受毫秒数字或同名历史别名。Java 标准默认0、Aurora 模式默认10000ms；Go 所有路由/兼容模式统一默认关闭，ConnectTimeout 与业务 context 继续独立生效。

内置或外部 DialContext 成功返回 net.Conn 后、认证/TLS 之前，在底层传输接入读取预算；因此握手、TLS、初始化、普通/压缩/OB20、预处理、Rows、LOB、ChangeUser 和取消辅助通道均使用同一机制。每次底层 Read 开始时，以“当前时间 + ReadTimeout”和当前请求 deadline 的较早者设置 read deadline。完成读取后，下次读取重新计时，连接空闲、调用方处理 Rows 的时间不消耗下次预算；TLS 或解压缓存的读取不产生新的底层网络等待。并发流水线写入刷新 deadline 不得延长正在阻塞的读取预算，清除外部 deadline 也不能清除该上限。

它不是总查询/总逻辑消息期限：持续有字节到达时可多次重置，完整命令仍需调用方 context 限时。写入、拨号、发现源、凭据/认证回调与调用方 Reader 不受该读取值约束，继续使用各自 context/已有预算；TLS 内部写控制记录也保留写方向预算。默认0不创建额外传输包装，不新增包、依赖或后台协程；取消继续复用已有 AfterFunc/关闭协调。

独立读取超时保留原网络错误，可用 errors.As 到 net.Error 并检查 Timeout；调用 context 未到期时不伪装成 context.DeadlineExceeded。context 更早到期或取消时保留其原错误。失败淘汰当前物理会话，不能返回池或把已经写出的业务映射为 ErrBadConn 触发重放；写出后的执行结果未知，批量保留已知/未知状态。新建连接的握手传输失败仍遵循原有有限切换规则，初始化失败不切换，均受同一总建连预算限制。

固定 Java 依据：DefaultOptions SOCKET_TIMEOUT（L117–L127）及 AbstractConnectProtocol.createSocket 的 setSoTimeout（L255–L257）。TestReadTimeout* / TestDSNReadTimeout 与外部离线 Example 覆盖两模式、三种包流、握手/初始化/TLS、Rows、批量、逐次重置、context 优先和并发刷新。TestReadTimeoutSessions 四目标源码要求只读慢 SQL fixture，本阶段仅编译，真实服务端执行、代理缓冲与断连后后台查询是否继续运行仍待联调。

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

D14 的具体入口与边界：

```go
type Out struct {
    Dest any
    In bool
    Type string
    Length int64
    Precision int64
    Scale int64
}
```

- Type 为封闭的 SQL 标量类型名，忽略两端空白及大小写；空值由 Go 目的类型推断。自定义 Scanner 必须显式声明 Type，INOUT 还需实现 driver.Valuer。Number、NString、Raw、RowID、时间/间隔以及标准 sql.Null[T] 保留输入身份；普通 unsigned 值支持完整 uint64 范围。
- Length 为文本字符数或二进制字节数，单位由声明类型决定；0 表示不额外约束。Precision/Scale 用于 NUMBER/DECIMAL 的无损范围验证，Precision=0 时 Scale 必须为 0。Oracle 精度最大 38、scale -84..127；MySQL 精度最大 65、scale 0..30 且不大于精度。输出超界报错，驱动不截断或舍入。
- Type 和尺寸用于客户端模式/值约束。纯 OUT 按 Java 路径发送 NULL，服务端从 SQL 或例程形参确定类型；需要强制数据库类型时在 SQL 中显式转换。句柄、LOB、OBJECT/ARRAY 的协议绑定由后续任务实现，标量入口在发送前拒绝相应目的。
- Oracle OUT/INOUT/RETURNING 使用真实 ob_version() >=2.2.76 的 COM_STMT_PREPARE_EXECUTE；版本未知或更旧时在发送业务前返回 ErrUnsupported。该语义要求也适用于 PrepareClient。接收参数/列描述、内部输出、强制尾随 OK/ERR，临时 statement 关闭后才发布输出；显式服务端 statement 可复用原 ID。
- MySQL 使用每次调用随机命名的私有会话变量，固定同一连接完成初始化、原 SQL、输出 SELECT 和变量清空。主命令的普通结果继续流式读取；辅助命令不加入 Execution 摘要。变量替换共用当前 SQL mode 的严格词法/字面量路径；无法安全替换的 SQL 在发送前拒绝。清理失败淘汰物理会话并保留错误。
- Query/Execute 的输出随最终响应消费或 Close 完成后生效；Exec 在返回前完成该步骤。清理后再次检查操作取消；失败、取消、输出形状错误均不写回内置目的。重复逻辑 OUT 的各物理输出必须一致，只写回一次；不同逻辑 OUT 指向同一目的时拒绝。
- ErrOut 可识别输出验证/转换错误；OutError.Parameter 为 SQL 的逻辑绑定位置，Partial=true 表示自定义 Scanner 已可能产生部分副作用。使用 errors.As 获取详细错误，继续保留底层 Scanner 错误链。多个输出先完成全部内置转换，再按 SQL 中首次出现的顺序写回；自定义 Scanner 失败后停止发布后续目的。

上述协议布局已对照固定 Java 源码并用合成对端验证；真实服务端/OBProxy 的例程与 RETURNING 仍须按兼容矩阵联调。

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

#### 当前实现与边界（D20）

- Oracle 兼容模式且 OceanBase 版本已知 >=2.2.76，使用原生 PrepareExecute 的隐藏 OUT 和最终响应；MySQL 兼容模式、旧/未知版本在业务发送前返回 ErrUnsupported。CursorOut、显式 BLOB/CLOB/NCLOB Out 不支持 INOUT、长度/精度提示或非 nil 初始句柄；SQL NULL 保持 nil。
- REF CURSOR 使用二进制类型 163、固定小端四字节 cursor ID。Fetch 使用独立 cursor ID 的九字节 COM_STMT_FETCH，首批建立列元数据；关闭先释放 cursor ID，最后一个输出资源再释放父 statement。Blob/Clob 复用原生 locator 与已有流式读写接口，句柄不受普通 LOB 总物化上限约束，单次协议与读取仍有界。
- 普通输出先完成内置转换和自定义 Scanner，再统一发布全部资源；Scanner 失败可有其自身副作用，但驱动不发布任何句柄。发布前再次检查 context 和资源代次；未发布租约在失败清理时释放。服务端尾错不发布，损坏/无法安全恢复的响应淘汰连接。
- 普通 REF CURSOR 列要求 Oracle 二进制行。完整消费父响应后，在原会话内有界获取子数据并关闭服务器 cursor ID，再作为标准子 driver.Rows 交给 database/sql。所有层共享 MaxMessageBytes，计入行、完整列元数据及嵌套内容；最多 16 层并拒绝循环 ID，超限报错。标准父 Rows 关闭会关闭已扫描子 Rows；需要跨过程 Rows 寿命和分批获取时使用 CursorOut。
- 子 Rows 使用父 Rows 的调用方迭代 context，内部网络操作 context 的结束不会提前取消子读取。Static 游标缓存计入全部嵌套快照，每次 Fetch 创建独立子读取状态并使用本次 context；回看不复用上次关闭的子 Rows。

本地协议/故障测试与真实服务器验收分开：`TestProcedureResources` 包含两模式的支持/拒绝、多游标和临时 BLOB/CLOB、失败发布及标准子 Rows 场景；当前仅编译，服务器过程语法、locator、版本及 OBProxy 行为仍待联调。

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

D22 已落地的接口与选择规则：

```go
type BatchOptions struct {
    ContinueOnError bool
    Strategy BatchStrategy
    ChunkRows int
    ChunkParameters int
}
type BatchItem struct {
    Status BatchStatus
    Started bool
    RowsAffected int64
    RowsAffectedKnown bool
    Err error
}
type BatchResult struct {
    Items []BatchItem
    Strategy BatchStrategy
}
type BatchError struct {
    Index int
    Completed int
    Err error
}
```

- Strategy 是偏好：BatchAuto（零值）、BatchSequential、BatchMultiSend、BatchRewrite、BatchBulk、BatchArrayBinding；结果报告实际策略。后三种当前均回退 BatchSequential，不声称原生优化已实现。ContinueOnError 默认 false，确保失败后不再发后项；true 也必须在取消、损坏响应、断线或结果不确定时停止。
- ChunkRows=0 使用 128，ChunkParameters=0 使用 65535；负值和非法策略发送前拒绝。multi-send 同时限制块行数、展开后的物理参数位总数及编码内容总字节数，单命令受 MaxMessageBytes 和既有值上限约束；逐项路径逐行限制输入参数，不聚合数据包。
- *sql.DB 在整个批次固定一个 *sql.Conn；原生 Conn/Tx 用一次标准 ExecContext 私有分派持有标准库连接锁。没有隐藏事务、提交、辅助会话或整批重试。自定义 Execer 逐项调用，物理会话和事务由其实现负责；未知错误保守记为 BatchUnknown 并停止。
- Status 为 BatchNotExecuted、BatchSucceeded、BatchFailed、BatchUnknown。Started 只表示业务命令可能开始发送，辅助 prepare/上传不算。BatchFailed 记录已知错误，不保证没有数据库副作用；BatchSucceeded 同样不代表事务已提交。已读完整成功响应后发生清理/取消错误，保留成功状态和已知计数，并通过 Err 返回错误；不将确认成功改成未知。
- RowsAffectedKnown=false 表示没有可安全表示的计数，含 uint64 超出 int64；不推算各项数字。BatchError.Index 是首个携带错误的输入索引（从 0 开始），没有对应输入的最终化错误为 -1；Completed 是确认成功项数，包含错误后继续执行的成功项。errors.Is/As 保留 SQL、context 和清理错误链。
- Auto/MultiSend 仅在 ContinueOnError=true、至少两行、PrepareServer、全部显式标量输入和单条 INSERT/UPDATE/DELETE/REPLACE 时启用；共享 prepare 前复用普通参数校验，本地非法项或 Oracle 物理参数位超过单块限制时整批回退逐项；共享 prepare 失败按首项错误处理，后项仍遵循逐项继续策略；SQL 在两种引号解释下均通过词法检查，排除分号、可执行注释、RETURNING、CALL/查询/会话 SQL。OUT、长参数、复杂值、自定义 Valuer 等回退逐项，继续复用既有绑定/输出/一次消费规则；资源句柄输出仍只接受 Execute。
- multi-send 只 prepare 一次，将独立 COM_STMT_EXECUTE 命令并行写入/按序读响应，双向背压不产生先写完再读的死锁。每条命令独立重置序号；收齐 OK/ERR 才确认该项。服务端 ERR 可继续；断线/取消/损坏或额外结果淘汰连接，保留已确认前缀，仅可能发出且未确认的项标 Unknown，未发的后续块保留 NotExecuted；结束前等待写协程退出。

原生优化证据与保守选择：固定 Java 基线的 [AbstractQueryProtocol](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java#L1287) 明确记录 OceanBase 不支持 COM_STMT_BULK_EXECUTE；其中 MariaDB 风格的版本判断不能作为 OceanBase 支持证据。[ComStmtExecute](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComStmtExecute.java#L165) 存在 Oracle array flag/列式参数布局，但 [JDBC4ServerPreparedStatement](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/JDBC4ServerPreparedStatement.java#L665) 的事务/PrepareExecute 条件及特殊错误计数，尚不足以证明全部参数类型与逐项停止/继续等价。SQL 多行改写也可能改变自动提交的部分成功、触发器和错误边界。因此三类偏好目前选择逐项，不猜测版本门槛；取得真实目标及逐项语义证据后才增加优化，正确执行能力不依赖它们。

本地故障对端已覆盖状态、计数、原事务、OUT/流、分块、多发包、取消和不重放。`TestBatchExecution` 提供四目标部分成功、策略和原事务回滚源码，本阶段仅编译，真实 SQL/服务器/OBProxy 行为未验收。

### 13.5 一次性长参数（D21）

公开入口使用 `*LongData`，直接作为标准 `ExecContext` / `QueryContext`、`Execute`、服务端 `Stmt` 或 `OpenCursor` 的 IN 参数；保留同一物理会话和事务。普通显式 PrepareClient 在读取 Reader 前返回 ErrUnsupported；OUT 本来就必须使用原生协议，混合长参数继续走该路径。

```go
input := &oceanbase.LongData{
    Reader: file, Length: -1, MaxBytes: 256 << 20,
}
_, err := tx.ExecContext(ctx, "INSERT INTO files(id, content) VALUES (?, ?)", id, input)
```

- Reader 始终由调用方关闭；Length 是源字节精确长度，-1 表示到 EOF，0 要求空流。已知长度同样探测至多一个额外字节以发现超长；不足返回 io.ErrUnexpectedEOF，不截断冒充成功。
- MaxBytes 为每个输入的源字节和转换后字节分别设上限；0 采用 Limits.MaxLOBBytes。显式增大允许流超过普通 LOB 物化上限；每块仍受 MaxMessageBytes 限制。Oracle hex 的协议膨胀不计入内容长度，每包仍受限。
- Text=false 发送二进制；Text=true 接受 UTF-8，按 Session.Charset 增量转换，拒绝非法/不可表示字符，跨块保留不完整码点；不把 NCharset 字节混入普通文本流。目标 CLOB/NCLOB 的服务端类型转换仍须真实环境验证。
- 首次开始上传即原子消费该对象；成功或中途失败均不能再次使用，也不复制、seek 或 rewind Reader。Oracle 同一具名参数的多个位置逐块分发同一数据，不再次读取来源；调用方重新构造对象不代表驱动拥有重放授权。
- MySQL 使用 COM_STMT_SEND_LONG_DATA(24)，每段无响应；Oracle 已知版本 >=2.2.76 使用 COM_STMT_SEND_PIECE_DATA(162)，逐段读取 OK。Oracle 二进制先 hex，文本按会话字符集，execute 参数头为 STRING(254)；两种模式均省略 execute 中对应的值体。Oracle 的已知版本下限复用现有能力门禁，不把 Java 选项首次出现的驱动版本当作服务端最低版本。
- 普通执行、向前/敏感游标、Oracle OUT 的预先 prepare + command161，以及 MySQL OUT 的会话变量 + 带流占位的服务端 CALL，均在同一 gate 内上传并执行。辅助 prepare/上传不冒充已执行业务语句的 Execution 结果。
- Reader 报错、超限、非法字符、段响应错误、网络部分写或取消使上传连接失效，清除无法确认的服务端长数据；上传后、execute 发送前的取消也淘汰连接。Reader 错误保留 errors.Is，特殊 ErrBadConn 不向 database/sql 暴露为自动重试信号；没有第二次业务执行。
- 不为任意阻塞 Reader 启动可遗留的后台读取协程。调用方需要让 Reader 在取消时返回，可通过自己的上下文/关闭机制实现；驱动不能代为关闭借用来源。LongData 的字段和 Reader 在执行期间不能被并发修改。

源码依据为 Java 同一基线的 [二进制流](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBStreamParameter.java)、[字符流](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBReaderParameter.java)、[MySQL 长数据](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/StreamParameter.java) 及 [原生执行](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java)。当前使用有界块，不暴露 Java 分块大小开关，不整流物化；真实大小限制、空流存储语义、LOB 转换和代理行为仍待四目标联调。

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

失效操作可用 `errors.Is(err, oceanbase.ErrResource)` 判断；结束后的标准 Conn/Tx 也可能先返回标准库的 `sql.ErrConnDone` / `sql.ErrTxDone`。Close 幂等，已失效或已结束会话上的关闭不重新建立连接。发送前取消保留再次关闭的机会。内部记录由物理连接持有，句柄只保存标准 Session 引用与不透明身份/代次；不增加公开资源管理器。尚未发布的记录在外层操作完整结束时清理，内部字典/LOB 辅助响应保留外层资源；服务器观察到的事务状态切换同样使旧代次失效。事务开始、结束及 reset 的资源清理都受 CleanupTimeout 约束。

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

D16 已实现上述原生入口和 `Blob.Reader(ctx)` / `Blob.Writer(ctx)`，适配器各自持有顺序偏移和本次传入的 context，不借用 Open 的 context，也不拥有 locator。`Blob.Truncate` 只缩短；短读返回已读字节及 io.EOF，分块写失败返回已完成的前缀，失败块不自动重放。普通查询和标量 `sql.Out` / `Out` 中的 LOB 返回内容，默认每值 16 MiB；显式句柄可分块处理超出该上限的对象。

当前实现使用已有 Oracle COM_STMT_PREPARE_EXECUTE 内核调用 DBMS_LOB.READ/WRITE/TRIM/FREETEMPORARY，要求已确认的实际 ob_version >= 2.2.76。读取数量同时接受原生整数和无损整数 NUMBER，拒绝小数、溢出及数量/内容不匹配。每块受 DBMS_LOB 的 32767 字节和 MaxMessageBytes 双重约束。V2 临时标记驱动服务端释放，V1 通过同会话 ISTEMPORARY 判定；持久 locator 只释放本地记录。清理失败淘汰物理连接，不结束调用方事务。

D19 的服务器游标到位前，首次遇到原生 LOB 的结果集会有界缓冲本次响应剩余的所有行集、命令结果和隐藏 OUT；读到完整响应边界后才物化内容。累计计入列信息、行容器、字段和物化后内容，受 MaxMessageBytes / MaxResultItems / MaxLOBBytes 限制。内部命令保留现有 gate，不进入公开 Session 或 Metadata，不换辅助连接；已缓冲行使用原调用方 context，与网络操作结束时的内部取消分开。Open 也先结束整个 locator 查询，再原子发布；基数不合要求时清理已取得的临时对象。

字符 API 的 off/count/返回计数均为 Unicode 码点。已知 UTF16 collation 的服务端偏移按 UTF-16 单位转换，其他受支持字符集按码点转换；out-row 路径从起点分块扫描，校验服务端数量和编码长度，不能把 UTF-8 字节当字符。UTF-16 覆盖要求已有位置与新字符的代理对宽度相同，宽度改变时在任何写入前返回 ErrUnsupported；追加和相同宽度覆盖可用。不能表示的文本、非法代理对或不匹配的服务端字符单位明确失败。目标服务端 NCLOB 字符单位和 collation 行为仍须由 TestNativeLOB 取证。

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

- 固定会话条件与 OpenBlob 相同。Schema 为空时在当前会话解析实际 schema，随后固定对象身份；Table/Column 必填，各字段表示一个标识符组成部分，不接收 SQL 片段。Oracle 使用字典中的精确拼写；MySQL 列名按字典忽略大小写解析，schema/table 遵守服务端字典比较规则。
- 通过实际模式的元数据确认单个基表、可读的二进制 BLOB 列和完整主键。拒绝视图/联表、缺失或多余主键字段、NULL 主键和不支持的类型；作为目标的 BLOB 列不得同时是主键或生成列。列名按字典解析后引用，主键值全部绑定，不拼接成 SQL。
- 输入在打开时完成参数校验；Valuer 只求值一次，复制 map、切片和可变字节值，保存规范化后的不可变身份。后续修改调用方 Key 不改变句柄目标，不使用全局 token 注册表。
- Open 只取得所需元数据、存在性、NULL/长度或原生 locator，不读取整个 BLOB。无该行返回 sql.ErrNoRows；该列为 SQL NULL 返回 nil、nil；空的非 NULL BLOB 返回长度为 0 的有效句柄。初始化 NULL 使用普通显式 SQL。
- MySQL 后端按主键生成受限分块 SQL：长度使用 OCTET_LENGTH，读取使用 SUBSTRING；Oracle 可由相同目标查询取得原生 BLOB locator；调用方传入可写 Tx 时，目标查询使用 FOR UPDATE 取得持久 locator 所需行锁，只读 Tx/Conn 的打开不加该锁。MySQL 后端不是 LONG_DATA，也不承诺查询得到的任意 BLOB 都能转成持久句柄。[J14][J15]
- Blob 字节偏移采用 Go 的 0 起点；转换为 SQL 的 1 起点和计算 off+len 时检查溢出。每块同时受逻辑消息和 LOB 块大小限制；不得在后端不支持分块时偷偷整体物化超大对象。当前每块最多 32767 字节，按消息预算进一步缩小；配置 MaxLOBBytes 更小时使用更小块，整个句柄内容不受单值物化上限约束。
- MySQL ReadAt/长度查询在每次调用时重新按主键访问。使用 `*sql.Conn` 时不承诺跨块快照；需要稳定读取时由调用方使用合适隔离级别的 `*sql.Tx`，必要时显式加锁。事务外也不承诺识别同一主键被删除后重新插入的另一行。
- MySQL 模拟句柄的 WriteAt/Truncate 必须运行在调用方的 `*sql.Tx` 内；否则在修改前返回错误，不隐式 Begin/Commit。每次修改先在该事务中按主键 SELECT ... FOR UPDATE，确认行和非 NULL 内容、锁定后读取当前长度，再执行参数化 UPDATE；只读事务或不支持所需锁语义时拒绝修改；当前仅对字典 ENGINE=InnoDB 的 MySQL 基表开放修改，未知引擎保留读取能力。
- WriteAt 只允许 `0 <= off <= 当前字节长度`，可在末尾追加，不自动填充稀疏空洞。例如二进制列的替换可使用 `CONCAT(LEFT(col, off), ?, SUBSTRING(col, off+len(p)+1))`；col 安全引用，其余值绑定。Truncate 仅缩短到 `0..当前长度`，不扩展。空写入或写入相同值，在锁定存在性已确认后可以成功；ChangedRows 返回 0 不等于行不存在。
- MySQL 写入前检查完整写入计划的目标容量：TINYBLOB 255、BLOB 65535、MEDIUMBLOB 16777215、LONGBLOB 4294967295 字节，超出时在首条 UPDATE 前 ErrLOB。UPDATE 的完整响应若带 warning，也返回 ErrLOB，不把该块计入完成前缀；服务器可能已截断或把 CONCAT 结果置为 NULL，调用方应回滚现有事务。不能把非严格 SQL 模式下带 warning 的 OK 当作完整写入成功。
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

type ColumnChange struct { Name string; Value any }
func (c *Cursor) Update(ctx context.Context, key map[string]any, changes ...ColumnChange) error
func (c *Cursor) Delete(ctx context.Context, key map[string]any) error
func (c *Cursor) Insert(ctx context.Context, changes ...ColumnChange) error
func (c *Cursor) Refresh(ctx context.Context, key map[string]any) (*sql.Rows, error)
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

D19 当前实现边界：

- `CursorOptions.Mode` 为 CursorForward（默认）、CursorStatic 或 CursorSensitive；`Updatable` 显式开放修改/Refresh。`MaxCacheBytes` 只控制 static 快照，0 使用 MaxMessageBytes。没有第二个连接池或公开资源管理器；所有新文件仍属于根包。
- `CursorPosition{Origin, Offset}`：Next 要求 offset=0；Absolute 从 1 起，0 返回空批次并回到首行前，负数拒绝；Relative 相对于上一批最后获取的行，提前关闭未扫描完的批次也按已获取行定位。计数要求正数且可用 uint32 表示。越过 static 末尾返回空批次；再 Next 不从头开始。位置加减检查 int64 溢出。
- Forward 使用原生 COM_STMT_EXECUTE cursor flag=1 / COM_STMT_FETCH；只接受 Next。Oracle 版本已知 >=2.2.5 时使用 19 字节 offset 扩展并消费尾 OK；更旧或未知版本保留 9 字节向前 fetch，不猜测 offset 支持。MySQL fetch 使用保留列描述；Oracle fetch 重新读取列描述。
- Static 在打开期间通过实际原生 fetch 建立有界快照，缓存包含列、行、值开销；超限报错并释放未发布资源。快照不因 Update/Delete/Insert/Refresh 改变，后续修改可通过 Refresh 显式读取。
- Sensitive 仅开放 Oracle 且已知 OceanBase >=2.2.76；使用真实合并命令161、cursor flag=1、scrollable executeMode=8，完整消费初次响应。每个非零定位均向服务端发 ABSOLUTE32 fetch，位置受 int32 限制，尾 OK 提供末行位置；重复定位也重新请求服务器。MySQL、未知/旧版本直接 ErrUnsupported。是否看见其他事务的变更仍受服务器游标与隔离语义约束，待真实环境验证。
- Fetch/Refresh 将一个批次完整接收、解码及物化后返回标准 sql.Rows；整批、单 LOB、复杂值与描述均沿用既有限制。复杂描述和 LOB 辅助命令必须在完整响应之后、原连接 gate 内执行。新批次不继承 Open 的 context；每次公开命令清空描述缓存。
- Updatable 当前要求调用方可写 `*sql.Tx`、单基表简单投影、元数据中一致的 schema/original table/original column、完整主键均在投影中；MySQL 另要求字典 ENGINE=InnoDB。使用 PK 方案，未增加仅 ROWID 方案；缺少完整主键时拒绝。join、子查询、DISTINCT/聚合/集合运算、重复或派生列不猜测目标，生成列可以读取但不能修改。支持范围由可靠行身份决定，不按 SQL 字符串猜字段。
- `key` 是完整主键值；`ColumnChange.Name` 使用投影基列的字典名，不接受别名或任意 SQL 表达式。标识符按实际模式引用；DML 与 Refresh 使用 COM_STMT_PREPARE/EXECUTE 的真实参数。Insert 必须显式提供全部非 NULL 主键，不隐式取生成键。更新前的同事务 SELECT 1 ... FOR UPDATE 复用既有内部类型化查询路径，完整收完后才执行 DML；Update=0 可成功，Delete/Insert 要求1，超过1或 warning 均返回错误。缺行通过 errors.Is(err, sql.ErrNoRows) 识别。
- Refresh 按完整主键读取当前基表投影，不重新应用最初 WHERE/ORDER，也不改变 static 快照或游标位置；当前随 Updatable 开放，因此同样要求可写 Tx。修改失败不替调用方提交或回滚；warning/未知执行结果后调用方应回滚。业务执行失败释放游标，位置/参数等发送前校验错误保留句柄。prepare、存在性锁与发送 DML 之间复核资源代次，事务结束后不能继续修改；Refresh 缺行只关闭游标，保留调用方事务以便回滚。
- 前一批次未关闭时，Fetch/Refresh/修改/Close 立即 ErrCursorBusy，避免进入 database/sql 后自锁。Close 幂等；事务结束、会话 reset 或物理关闭按既有代次和父 statement 租约释放。普通查询 Rows 的向前读取行为不变。

以上为本地协议实现，不代表真实 OceanBase/OBProxy 游标、锁与敏感性已验收。`TestCursorModes`、`TestCursorUpdates` 已提供四目标源码，本阶段只编译，不连接数据库。

这是真正需要的扩展对象：标准库没有相同的滚动操作面。其查询数据仍通过标准 Rows.Scan 读取。[J10]

## 16. 元数据、XA 和会话辅助功能

### 16.1 元数据

`Rows.ColumnTypes()` 提供结果列信息；数据库结构通过函数及明确的 Filter 返回 Go 结构体或普通 Rows。

覆盖范围包括：catalog/schema、表/视图/同义词、列、主键/外键、索引、权限、过程/函数及参数、类型定义、UDT、数组元素类型、支持能力和数据库版本。与 Java 相同，服务端本身没有的信息返回“未知/不支持”，不伪造完整元数据。

模式 SQL 分文件维护，过滤值全部参数化；确需动态标识符时使用模式专用引用函数。按固定 sql.Conn 识别模式后执行相应查询，不能从一个自动模式池获取模式、再随机在另一连接上查询。

复杂类型中的 schema、同义词、嵌套对象描述属于此模块与 codec 的共享数据，不重复建两套字典缓存。

公开 `Metadata(ctx, session, filter)` 在入口检查固定 *sql.Conn/*sql.Tx，经包私有请求进入标准 QueryContext 内核，返回普通 *sql.Rows。内部类型描述解析器和 LOB 物化器则直接调用根包内的字典/命令执行器，只能在 §14.3 的合法响应边界使用当前物理会话；不能调用公开 Metadata 或标准库会话方法，也不能另开连接读取原事务字典。两条入口复用参数化 SQL 计划、描述类型及按会话权限/模式/schema/类型版本隔离的缓存，不复用高层调用栈。

D13 固定内外查询边界；D16/D18 必须用真实 database/sql 入口配合协议故障对端验证缓存未命中路径：在 QueryContext 与 Rows.Next 内需要字典/LOB 读取时均能在限定时间结束，业务和内部命令落在同一物理会话，未发生锁重入或未读响应插入命令。真实服务端另验证未提交事务可见性；模拟对端不能替代该证据。

### 16.1.1 D13 已实现的 Go 元数据契约

公开入口只有 `Metadata(ctx context.Context, session Session, filter MetadataFilter) (*sql.Rows, error)`，接受非 nil 的 `*sql.Conn` / `*sql.Tx`；`*sql.DB`、包装会话及 typed nil 拒绝。`MetadataKind` 是封闭枚举。普通业务结果仍使用 `Rows.ColumnTypes()`，不引入 JDBC 风格的 DatabaseMetaData 对象。

```go
pinned, err := db.Conn(ctx)
if err != nil {
    return err
}
defer pinned.Close()
rows, err := oceanbase.Metadata(ctx, pinned, oceanbase.MetadataFilter{
    Kind:          oceanbase.MetadataColumns,
    SchemaPattern: "APP",
    NamePattern:   "ORDER/_%", // '/' 转义 '_'，末尾 '%' 是通配符
})
if err != nil {
    return err
}
defer rows.Close()
// 按下表的固定列顺序 Scan；可空值使用 sql.Null[T]。
```

`MetadataFilter` 字段为 `Kind`、`Catalog`、`SchemaPattern`、`NamePattern`、`ColumnPattern`、`ReferencedSchemaPattern`、`ReferencedNamePattern`、`TableTypes []string`、`Query`。Catalog 为精确匹配；其他 Pattern 保留调用方大小写，使用 LIKE，`/` 为转义符，空值选择所有可访问对象。下划线等普通名字需要调用方按 LIKE 规则转义。SchemaPattern 过滤 schema/owner；NamePattern 过滤表、例程或类型名；ColumnPattern 过滤列、参数或属性名。Referenced 两字段只用于外键，TableTypes 只用于表查询，值限定 `TABLE` / `VIEW` / `SYNONYM` / `MATERIALIZED VIEW`；不适用的非空字段明确拒绝。Query 只供 StatementParameters 使用。

下表列顺序固定。以 `#` 标记的列为 `int64`，其余为 `string`；`#` 不是列名的一部分。所有列允许 SQL NULL，缺失数据保留 NULL。字典 Rows 的 DatabaseTypeName 统一为 `BIGINT` / `VARCHAR`，ScanType 对应 `int64` / `string`，不暴露两种字典内部 NUMBER/VARCHAR2 的差异。

| MetadataKind 后缀 | 返回列（按顺序） |
| --- | --- |
| Catalogs | catalog |
| Schemas | catalog, schema |
| Tables | catalog, schema, name, type, remarks |
| Synonyms | catalog, schema, name, target_schema, target_name, database_link |
| Columns | catalog, schema, table_name, name, #ordinal_position, data_type, #length, #precision, #scale, #nullable, default_value, remarks, type_owner |
| PrimaryKeys | catalog, schema, table_name, name, column_name, #ordinal_position |
| ForeignKeys | catalog, schema, table_name, name, column_name, #ordinal_position, referenced_catalog, referenced_schema, referenced_table, referenced_column, update_rule, delete_rule, deferrable, initially_deferred |
| Indexes | catalog, schema, table_name, name, #non_unique, column_name, #ordinal_position, ordering, #cardinality, type |
| TablePrivileges | catalog, schema, table_name, grantor, grantee, privilege, #grantable |
| ColumnPrivileges | catalog, schema, table_name, column_name, grantor, grantee, privilege, #grantable |
| Routines | catalog, schema, name, specific_name, package_name, type, return_type |
| RoutineParameters | catalog, schema, routine_name, specific_name, package_name, name, #ordinal_position, direction, data_type, #length, #precision, #scale, type_owner, type_name, #data_level |
| Types | catalog, schema, name, type, #attribute_count, #final, #instantiable |
| TypeAttributes | catalog, schema, type_name, name, #ordinal_position, data_type, type_owner, #length, #precision, #scale |
| CollectionElements | catalog, schema, name, collection_type, #upper_bound, element_type, element_owner, #length, #precision, #scale |
| ServerInfo | oceanbase_version, compatibility_mode, #parameter_dictionary, #type_dictionary |
| StatementParameters | #physical_ordinal, #logical_ordinal, name, direction, data_type, #nullable, #length, #precision, #scale, #protocol_type, #protocol_direction |

- MySQL 兼容模式使用 information_schema，catalog 为 `def`，database 对应 schema；Synonyms、Types、TypeAttributes、CollectionElements 返回 ErrUnsupported，不合成服务端类型字典。Oracle 使用 ALL_*，catalog 查询为空行集，其余查询的 catalog 为 NULL。视图/物化视图经 Tables 的 TableTypes 选择，同义词另有目标描述；Columns 不隐式展开同义词。
- 例程与参数的 specific_name 是同一字典身份；Oracle 由 object_id/subprogram_id 组成，保留 package_name、data_level 和返回位置 0。direction 统一 `IN` / `OUT` / `INOUT` / `RETURN`。未知 update_rule、默认属性、grantor 等保持 NULL。主外键及索引的列位置从 1 开始；索引 ordering 为 ASC / DESC 或 NULL。
- MySQL PARAMETERS 只在建连读取的 `ob_version()` 确认 OceanBase >= 4.1 时启用，不能使用 greeting 中的 MySQL 5.7/8.0 版本。未知或旧版本明确 ErrUnsupported，权限及字典 SQL 错误保留服务端 Error。ServerInfo 的字典能力位描述当前驱动开放的查询路径，不证明当前用户拥有字典权限。
- StatementParameters 对 Query 执行一次服务端 prepare，保存参数列描述，关闭临时 statement 后返回独立的内存 Rows，绝不执行 Query；即使 Config.PrepareMode=PrepareClient 也遵循此规则。物理位置与逻辑位置均从 1 开始，Oracle 重复命名参数共享逻辑位置，name 为规范化绑定名。protocol_type/protocol_direction 保留协议字节，不能把未取证的 direction 字节猜成 IN/OUT；该入口的 direction 为 NULL，例程方向请查询 RoutineParameters。MySQL 参数可空性未知，参数类型和尺寸仅为 prepare 回报的信息，不承诺推断出的业务列定义。
- Columns.length：MySQL 为 CHARACTER_MAXIMUM_LENGTH；Oracle 字符语义为 CHAR_LENGTH，其余为 DATA_LENGTH（字节），需结合 data_type 使用。RoutineParameters/TypeAttributes/CollectionElements 的 length 保留字典原始单位；未知不补默认长度。普通结果 ColumnTypes 的长度按协议及已知字符集报告字符数/二进制字节数；未知字符集或不可解释的尺寸返回 ok=false。DECIMAL 的精度去掉符号和小数点，Oracle NUMBER 使用显式 precision 和带符号 scale；未指定/超出合法范围返回 ok=false。Oracle FLOAT 的二进制 precision 与 -127 浮动 scale 不作为十进制精度报告。
- `metadataLocked` 在已拥有连接 gate 且响应已完整结束时执行，复用同一参数化计划和现有字符集/安全字面量编码；返回 Rows 只关闭自身取消回调，不释放外层 gate。未读响应直接拒绝；内部查询不会经 Metadata/Session 或另开物理连接。公开入口把锁所有权交给返回 Rows。D18 已实现单业务命令内的类型描述缓存，D13 不缓存任意字典行集。
- UDT 属性与集合元素投影使用 OceanBase 的 NUMBER_PRECISION 字段，输出别名仍为 precision；依据固定 [v4.3.5_CE 属性视图](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/share/inner_table/ob_inner_table_schema.25001_25050.cpp) 与 [集合视图](https://github.com/oceanbase/oceanbase/blob/v4.3.5_CE/src/share/inner_table/ob_inner_table_schema.25051_25100.cpp)，不套用不一致的字典字段。

以上路径已由协议对端覆盖，真实数据库字典字段、版本差异及权限仍需集成验证；D16/D18 的 Query/Next 缓存未命中场景仍由各自任务验证。

### 16.2 XA

提供 `XID` 值和 Start/End/Prepare/Commit/Rollback/Recover/Forget 等显式操作，参数包含固定 `*sql.Conn`、XID 和标准 XA flags。XA 入口不接受 `*sql.Tx`；开启 XA 的会话不得同时 BeginTx。

- MySQL 兼容模式走对应 XA 命令；Oracle 兼容模式走 DBMS_XA 和其复杂 XID 表示。[J11]
- Start 到 End/Prepare 阶段保持物理会话约束；两阶段事务的外部协调和持久化日志由调用方负责。
- Recover 返回真实 XID 列表；准备后的分支不因一个 Go 对象 Close 就被误当成本地未提交事务回滚。
- 分支状态与连接可复用条件明确；异常或无法确认脱离分支的连接不得回池。
- 一阶段/两阶段提交、join/resume/suspend、服务端支持的超时及返回码逐项适配；不支持的 flags 明确拒绝。
- XA 失败没有透明业务重放；提交结果不确定时由协调方查询恢复。

### 16.2.1 D31 已实现契约与边界

公共根包提供 `XID`、`XAFlags`、`XAVote` 和上述七个显式函数，固定 `*sql.Conn`，复用包私有请求及连接 gate；不新增包、依赖、连接池或事务管理器。XID 的 FormatID 为 int32（-1 保留），GlobalID 为 1–64 字节、BranchID 为 0–64 字节，输入及恢复结果复制切片，二进制身份不经字符集转换。

- MySQL 使用十六进制二进制字面量及 32 位 FormatID 位表示，精确传递 JOIN/RESUME；不套用 Java `pinGlobalTxToPhysicalConnection` 将 JOIN 暗改 RESUME。服务端拒绝时保留错误，支持条件由实际版本验收。
- Oracle 使用固定匿名 PL/SQL 调用 `SYS.DBMS_XA`，首个原生 OUT 返回函数整数码，XID 按 Java `useLocalXID` 路径固定为 SYS.DBMS_XA_XID 的 NUMBER/RAW/RAW；避免逐次字典查找和 SQL 字面量拼接。使用原生 PrepareExecute，实际 ob_version 必须满足既有 >=2.2.76 门禁；旧版/未知版明确 ErrUnsupported。该 PL/SQL 选择的服务端适用性仍待联调。
- Start 接受 NoFlags/Join/Resume；Oracle 另接受 XAOracleReadOnly(256)、XAOracleSerializable(1024)、XAOracleLoose(65536)。End 接受 Success/Fail/Suspend；Oracle 另接受 Migrate 或 Suspend|Migrate，其余组合发送前拒绝。Oracle Start 先关闭 autocommit，确认 End 后恢复；恢复失败返回 ErrTxCleanup 并淘汰会话。
- 本地只跟踪本连接已确认的分支阶段；同时最多关联一个活动分支，已知挂起分支用 Resume，已结束分支用 Join，prepare 要求 ended，1PC 要求已知 ended，2PC 要求已知 prepared。新连接可以向服务端 prepare/2PC commit/rollback 未知分支，用于外部恢复；不把本地映射当作 RM 全局真相。映射受 MaxResultItems 限制。
- End(Fail) 标记本地 rollback-only，随后 prepare/commit 拒绝，必须 rollback；Oracle 发送其支持的 Success 等价解除关联。此标记不跨进程/连接持久化，协调方必须持久记录失败决定并保证不从新连接提交 rollback-only 分支。
- Prepare 的 0/3 分别为 prepared/read-only；MySQL 成功只给 prepared，不编造 read-only。Oracle 负码以及正的回滚/启发式码均作为 XAError 返回，不把任意正数视为成功。XAError 保留整数 XA Code；MySQL 已取证的 1397–1402 映射对应标准码，其他错误保留原 Error 并归 RMERR，不猜测未取证映射。
- Recover 读取真实四列/原生 OUT 集合。严格校验长度、NULL、FormatID 及元素 SYS/类型/顺序；Oracle 支持命名集合或元数据明确为 SYS.DBMS_XA_XID 元素的匿名集合，NULL BQUAL 视为合法空限定符，NULL GlobalID 拒绝。行数和累计内容受 MaxResultItems/MaxMessageBytes 限制；畸形、超限或额外结果不返回部分列表并淘汰连接。
- 所有公开 XA 错误均可用 errors.Is(err, ErrXA) 分类，底层服务端/context 错误保留。可能写出的变更命令遇到 IO/取消/畸形或已输出后尾部失败时返回 ErrXAOutcomeUnknown，提交另带 ErrCommitUnknown，淘汰原会话且不返回可重放 ErrBadConn。完整服务端拒绝保留其 XA/服务器码，不推断分支已完成；恢复查询失败不代表业务已回滚。
- 首次 XA 尝试后会话永久专用；禁止 sql.Tx、ChangeUser 及会话 setter，关闭后退出原池。XA 边界先失效已有 LOB/Cursor；prepared Close 仅关闭 statement/物理会话，不发送本地 ROLLBACK 或重置 autocommit。禁止混入裸 XA/COMMIT/ROLLBACK/改变 autocommit 的 SQL，驱动不解析其全局事务语义。配置的只读服务仅允许恢复查询，XA 变更须使用读写入口。
- Forget 返回 ErrUnsupported，RM 事务超时设置未提供；Java 相应实现本身也是未支持，不能沿用其无操作成功。调用 context 仅限制客户端 IO，不能替代 RM 超时。XARecover、持久协调日志、决策和未知结果处理归调用方；列表缺少某 XID 不能单独证明其已提交或回滚。

`xa_test.go` 覆盖两模式、标准/压缩/OB20、1PC/2PC、挂起/恢复、阶段与 flags、rollback-only、原生 XID/OUT、返回码、恢复数据/边界及部分写/丢确认/尾部失败。`TestXATransactions` 和 `TestXARecoveryAfterDisconnect` 已加入四目标手动 CI，验证 DML 可见性及准备后关闭、新物理连接恢复；当前只编译，未连接数据库。示例在 `examples/xa/`，明确不包含持久协调器。D30 的 D28 平台依赖未闭环，D31 本地实现与整体依赖闭环分别记录。

### 16.3 其他会话功能

schema/catalog、用户切换、客户端信息、warning、会话变量及版本能力通过固定 sql.Conn 上的函数承载。ChangeUser 必须重新认证、重建基线并使所有旧 statement/locator/cursor 失效；不在普通池连接上隐式切用户。显式 ChangeUser 后将该物理连接标记为不可归还原池，调用方关闭 sql.Conn 时淘汰它，防止另一个调用方取得错误身份的会话。

warnings 作为结构化结果诊断暴露，不能把成功带 warning 的操作改成随意失败；也不能仅因日志未启用就丢失所有可查询 warning 信息。

### 16.3.1 D30 会话辅助与 warning 数量快照（首批实现）

以下函数只接受非 nil 的 `*sql.Conn`，调用方应先消费/关闭 Rows 并串行操作同一连接；没有新的会话包装对象或公开任意协议命令入口：

```go
func CurrentSchema(context.Context, *sql.Conn) (string, error)
func SetSchema(context.Context, *sql.Conn, string) error
func CurrentCatalog(context.Context, *sql.Conn) (string, error)
func SetCatalog(context.Context, *sql.Conn, string) error
func GetClientInfo(context.Context, *sql.Conn) (ClientInfo, error)
func SetClientInfo(context.Context, *sql.Conn, ClientInfo) error
func SessionVariable(context.Context, *sql.Conn, string) (sql.NullString, error)
func SetSessionVariable(context.Context, *sql.Conn, string, string) error
func Warnings(context.Context, *sql.Conn) (WarningSummary, error)
```

- CurrentSchema 在 Oracle 兼容模式查询 SYS_CONTEXT 当前 schema，在 MySQL 兼容模式查询当前数据库；没有选定数据库时返回空字符串。SetSchema 接受确切名称，不自动大小写折叠：Oracle 标识符双引号转义，MySQL 使用 COM_INIT_DB；名称按连接字符集编码，不拼接为多条 SQL。catalog 只适用于 MySQL 兼容模式，Oracle 明确返回 ErrUnsupported。
- ClientInfo 是 ApplicationName、ClientUser、ClientHostname 三个 string 字段，映射同名会话用户变量；SetClientInfo 一次替换全部字段，空字段发送 NULL，读取 NULL 转为空字符串。变量值复用当前模式和 NO_BACKSLASH_ESCAPES 状态的字符串转义与严格 SQL 编码，不写入全局变量。
- SessionVariable 只接受 ASCII 系统变量标识符，保留 SQL NULL；SetSessionVariable 的值是字符串，实际变量可写性/值域由服务端校验。time_zone、ncharacter_set_connection、character_set_*、collation_* 由 Config.Session 管理，运行时 setter 拒绝修改，防止解码配置与服务端状态不一致。两模式实际 SQL 支持仍需联调。
- setter 只在无托管/服务端事务且 autocommit 开启时执行。发送修改前标记物理会话不可归还原池，成功或完整服务端错误后仍可在原 pinned 连接继续使用，关闭 sql.Conn 后淘汰；校验失败不标记。所有 setter 在发送前清理自动预处理缓存及类型描述缓存，覆盖 sql_mode 等解析语义变更；调用方显式持有的 sql.Stmt 仍遵循服务端原生绑定语义。此策略以重建连接代替不完整的变量恢复，不更改其他普通 SQL 的既有 reset 契约。
- WarningSummary{Count uint16, Known bool} 保存协议 OK/EOF 数量；Known=false 表示尚无完整响应提供计数。Warnings 只读取最新普通 SQL 命令的最近完整结果快照，新的 SQL 清空并替换它，Ping/本节 helper/内部清理不覆盖；归还池后 reset 清空。Execute 的每项 ResultInfo.Warnings 独立保存各完整结果的数量，未完成结果保持未知。成功带 warning 仍是成功，nil logger 不影响快照。
- 本节数量快照不编造警告码、SQLSTATE 或消息；详情/清除见 §16.3.3，ChangeUser 见 §16.3.2，显式取消见 §16.3.4。各子项与平台认证/传输的完成状态分别记录。

实现继续使用根包现有的封闭请求、操作锁、Rows/错误状态机和 wire 叶包，无新增包或依赖。四目标集成源码 TestSessionOperations 已提供，本阶段仅编译，尚未验证真实模式、代理或版本。

### 16.3.2 D30 ChangeUser 与会话身份代次

```go
func ChangeUser(ctx context.Context, conn *sql.Conn, credentials Credentials, database string) error
```

ChangeUser 在原物理连接、原 TLS 与原标准/压缩/OB20 包流上重新认证，不拨号、不重放业务、不修改原 Connector。调用方传入本次凭据与确切数据库名；ProxyUser 每次显式给出，空值清除旧目标（§6.3）。空数据库表示 MySQL 不选择数据库，Oracle 使用新认证用户名的 @ 前部分作为默认 schema（没有 @ 时使用完整用户名）。不自动折叠显式名称大小写，用户名和数据库严格按连接字符集编码；服务端校验权限/名称。旧 Config.Credentials 回调不参与显式切换，切换凭据密码不保存在连接配置中。

- 必须先消费/关闭 Rows，串行使用固定 sql.Conn。活动托管事务、服务端事务或 autocommit 关闭时拒绝切换；不会隐式提交或回滚业务事务。
- 复用 D28 的插件工厂、初始/多轮/切换认证、密码编码、每轮 TLS 门禁及 io.Closer 生命周期。会话保留最终协商插件和 nonce，下一次切换创建新插件实例；不复用已完成的插件状态。认证全过程仍受调用 context 和 ConnectTimeout 中较早的期限限制，回调和平台同步调用仍须配合取消。
- wire 使用 OceanBase Connector/J 的 COM_CHANGE_USER 布局：命令17、用户名终止串、按能力编码的认证数据、始终存在的数据库终止串，以及已协商的插件/连接属性；与通用 MySQL 布局不同，不附加两字节 charset。[源码](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L195)
- 本地校验/认证响应准备通过后，先在旧身份下清理固定资源及自动预处理缓存，再发送切换。发送切换前更新 statement 身份代次并将物理会话标记为不可回原池。旧显式 server/client statement、LOB 和 Cursor 不能跨代次使用；旧 statement.Close 只关闭本地对象，不向新会话发送可能已被复用的旧 ID。
- 服务端成功后，兼容模式必须保持原模式；重建 Config.Session 的字符集、autocommit、版本、时区、变量和 InitSQL 基线，并清空旧类型/自动 statement 缓存及 warning 快照。初始化共用持锁内核，不通过 database/sql 重入，也不重新协商已启用包流。
- 发送切换后认证拒绝、超时/取消、畸形响应、模式变化、插件关闭错误或初始化失败，一律淘汰物理会话；成功时原 pinned 连接继续可用，sql.Conn.Close 时退出原池。原连接池之后重新建连仍使用原 Connector 身份。本地校验或初始 TLS 门禁拒绝不更改身份。

TestChangeUserIsolation/Failure/Validation/Encoding/Database/PluginLifecycle/Cancel/VerifiedTLS/Framing 提供本地协议与真实 TLS 对端证据；TestChangeUserSessions 四目标源码检查实际身份、旧 statement 失效及原池身份隔离。集成测试需各 profile 的 CHANGE_USER_DSN 提供同服务上的另一个合法身份，仅使用其用户名/密码/数据库，TLS 和认证策略继承原连接；本阶段仅编译，未连接数据库。真实插件、LOB/游标清理、代理、权限及版本组合仍待联调。

### 16.3.3 D30 warning 详情与本地清除

```go
type Warning struct {
    Level string
    Code uint32
    Message string
}
func ReadWarnings(context.Context, *sql.Conn) ([]Warning, error)
func ClearWarnings(context.Context, *sql.Conn) error
```

ReadWarnings 在固定连接上发送 MySQL 模式的 SHOW WARNINGS，按列字符集解码级别/码/消息；该 SQL 不提供 SQLSTATE，驱动不推测映射。消息可能包含 SQL 或敏感值，仅按请求返回，不自动写日志。Oracle 模式继续提供协议数量快照，ReadWarnings 明确返回 ErrUnsupported，不猜测 Oracle 详情 SQL。此边界与 [Connector/J getWarnings](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/OceanBaseStatement.java#L1263) 一致：MySQL 查询详情，Oracle 仅给出通用带 warning 提示；Go 以 WarningSummary.Count 表达后者。

- 这是请求时读取的**当前服务端诊断区**，不是历史结果详情快照。应在消费/关闭 Rows 后立即调用，并串行使用 pinned 连接；其他服务端命令（包含 helper 和驱动内部 SQL）可能已替换诊断区。Warnings/ResultInfo 的本地数量快照仍独立保留，不能据此推断当前详情必与数量对应。
- 不因数量为0而跳过 MySQL 查询；服务端诊断区和计数保留规则可能不同。max_error_count 等服务端限制可能使详情少于数量，驱动不补造缺项。读取不自动查询其他连接、不修改事务、不改变原 SQL 成功/失败语义，也不要求 logger。
- Limits.MaxResultItems 同时限制一次详情的行数，MaxMessageBytes 限制解码后级别/消息/码的累计内容；超限返回 wire.ErrMessageTooLarge，不返回部分列表并淘汰连接。空结果有效；NULL/非法码/畸形列数/额外结果拒绝并淘汰，完整服务端 ERR 可复用，发送后取消淘汰。标准/压缩/OB20 共用既有包流和 Rows 内核。
- ClearWarnings 只清除本地数量（Known=true、Count=0），在下一个普通 SQL 命令发送前抑制 MySQL ReadWarnings；不发送 SQL、不清除服务端自身诊断区。helper/Ping 不解除抑制，新普通 SQL（含执行失败的命令）解除；新建、reset 和 ChangeUser 后抑制旧详情。Oracle 可清数量，但详情 API 始终返回 ErrUnsupported。历史 Execution.Snapshot 不被清除函数修改。

TestWarningDetails/Response/Clear/Modes/Validation/Cancel/Framing 和 TestSessionOperationNoRowset 覆盖本地行为。四目标 TestWarningDetails 集成源码需要对应 WARNING_SQL（成功且产生非零警告，MySQL 需实际保留详情）；仅编译，尚未连接真实模式或代理。显式取消见下一节。

### 16.3.4 D30 显式取消与单次目标

```go
type CancelTarget struct { /* 不透明，不允许指定连接 ID */ }
func QueryTarget(context.Context, *sql.Conn) (CancelTarget, error)
func CancelQuery(context.Context, driver.Connector, CancelTarget) error
```

先在空闲的固定连接上调用 QueryTarget，再启动业务 SQL；在另一 goroutine 中调用 CancelQuery。取目标本身不发送 SQL，不能在原查询/Rows 占用固定连接后再取身份。再次 QueryTarget 会替换旧目标。目标只属于其后的第一个普通 SQL 发送/响应周期；复制目标不增加使用次数，每个目标只接受一次取消尝试（包括拨号失败），不自动重试。尚未发送、已完成、已尝试或已过期时返回 ErrResource，不能取消同一连接的下一操作。Prepare、Ping 和会话 helper 不进入这个业务周期；批量逐项模式仅覆盖首个 SQL，multi-send 仅覆盖首个发送分块，不能据此宣称整批停止或未执行。

- 目标保存原物理会话、实际兼容模式、用户/租户身份、实际入口和 TLS 状态，不持有新凭据。reset、切用户、关闭及首个 SQL 结束使其失效；独立于协议 gate 的取消状态负责协调。SQL 结束在内部清理/LOB 物化和释放 gate 前等待已发送的 KILL 得到确定结果；尚未发送的辅助拨号会被取消。默认 context 取消仍关闭原连接，不偷偷换成 KILL。
- Connector 必须来自本驱动 NewConnector。复用其凭据提供方、认证插件、TLS、会话基线及限额，但只向目标记录的同一入口拨一次辅助连接，不重新选址/发现/切换。Network 和模式必须匹配；认证后的用户/租户必须与目标相同，辅助连接 ID 不得等于目标 ID。ChangeUser 后需传入对应新身份的 Connector。原连接使用已验证 TLS 时辅助连接也必须已验证 TLS，不能降级；辅助连接私有，不进入 database/sql 池，各路径均关闭。
- 辅助拨号、认证、初始化和 KILL 共用调用 context 与 Connector.ConnectTimeout 的较早期限，关闭使用 CleanupTimeout。自定义 DialContext、Credentials 和认证回调必须响应 context，且不得重入正被取消的连接；驱动不创建可遗弃的后台拨号。私有辅助通道不触发 Logger 或 Diagnostics 回调，避免同步观察者重入正在等 KILL 的原会话；原业务连接的日志配置不变。失败完整保留服务端错误（如权限不足），原查询独立继续；KILL 部分写、丢确认、畸形/额外结果均使原连接失效，并禁止业务重放。KILL 写出后即使读取前直接取消，也属于结果未知。
- 成功只代表服务端确认 KILL，不证明 SQL 未执行，也不替代原操作的最终结果。协议只接受连接 ID，没有服务端会话代次/CAS；本地生命周期协调不能排除远端主动断开后 ID 被重用的窗口，尤其需真实 OBProxy/版本取证，不能宣称跨服务端的绝对原子保证。

TestCancelQueryIdentity/Failure/CompletionBeforeSend/AckCancellation/Guards/AuxIdentity/FixedEntry/WaitsForAck/VerifiedTLS/ResetAndClose 与 ChangeUserIsolation 覆盖两模式、固定入口、TLS、权限错误、单次令牌、响应竞态和本地生命周期。TestCancelQuerySessions 四目标源码及手动 CI 已补，只编译未联调；每个目标配置 CANCEL_SQL（足够长的只读阻塞查询）和 CANCEL_CODE（已取证的服务端中断码），验证实际中断及原会话复用，不把任意 SQL 错误当取消成功。

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

### 17.1.1 D26 静态多地址实现契约

当前公开 API 为根包 `Config.Routing RoutingOptions`：

```go
type Endpoint struct {
    Addr string
    Role EndpointRole
    Group, Weight uint16
}
type RoutingOptions struct {
    Endpoints []Endpoint
    Role EndpointRole
    Strategy BalanceStrategy
    MaxAttempts int
    AttemptTimeout time.Duration
    RetryDelay time.Duration
    BlacklistTimeout time.Duration
    ValidationInterval time.Duration
}
```

| 配置 | 语义、默认值和边界 |
| --- | --- |
| Endpoints | 非空时替代单个 Addr 作为候选；否则由 Addr 生成一个带当前 Role 的候选。最多1024项，按 DNS 小写、IPv6 和十进制端口规范化后拒绝重复/空地址；省略端口为2881。Config.Addr 仍须合法。列表在 NewConnector 时复制 |
| Role | RoleAny 为零值；RoleReadWrite / RoleReadOnly 只选相同显式标签，不回退到未标记或相反角色；无候选时配置失败。标签是调用方声明的逻辑服务，不能证明实际主从状态或 SQL 权限 |
| Group / Strategy | Group 从小到大尝试；组内 BalanceOrdered 保持输入顺序，BalanceRandom 随机排列，BalanceRoundRobin 在共享 Connector 内轮转，BalanceAffinity 按 Weight 加权排列且不重复。Weight=0 按1处理；不会在业务 SQL 中重新选址 |
| MaxAttempts | 零值为可用角色候选数，最多4096；只计实际拨号次数，冷却等待不消耗次数；默认单地址仍只拨一次 |
| AttemptTimeout | 覆盖一次拨号、TLS、认证和初始化，零值为 ConnectTimeout / min(候选数, MaxAttempts)，至少1ns。每次及重试等待都受同一 ConnectTimeout/context 更早期限约束；初始化超时终止，不能重放 |
| RetryDelay | 零值50ms；暂时失败后可取消的固定退避，不重新开始总计时；MaxAttempts 限制后不再等待 |
| BlacklistTimeout | 零值1秒；暂时连接失败才进入冷却，成功完整初始化后清除；全部冷却时等待最早到期或 context 取消。状态只含配置地址，按 Connector 隔离，不使用全局租户黑名单 |
| ValidationInterval | 零值关闭；从上次完成操作（含 Rows 消费）起，空闲达到间隔才在 ResetSession 持锁发送同 Socket 的 PING14；仍受 CleanupTimeout/context 约束。失败返回 ErrBadConn，由标准连接池申请新会话，驱动不就地换线 |

仅新建会话中的暂时拨号错误、握手断线/超时允许切换；不可恢复 DNS 错误、自定义拨号器普通错误、认证拒绝、TLS 证书/协议拒绝、模式不符、配置/凭据回调及初始化失败立即返回。错误保留原 errors.Is/As 链。TLS 使用实际选中地址推导 ServerName，显式 ServerName 保持调用方值；即使 TLS alert 被包装进 net.OpError/wire.IOError，也不能按网络故障重试。只收到 TCP 断线而未收到协议拒绝时，驱动只能按传输故障判断。

初始化和模式锁定均在当前候选和单次预算内完成；任何初始化失败都不回到候选循环，因为 InitSQL 可能产生副作用。已建立会话的查询、事务、statement、LOB、游标失败不调用选址器，也不重放业务。

RoleReadOnly 的 Connector 在发送前拒绝 ReadOnly=false 的 BeginTx；ReadOnly=true 使用现有 Socket。普通 SELECT 不分析/路由，普通写语句权限由服务端负责。读写服务分别使用两个标准 sql.DB，同一个 Connector 固定角色；RoleReadWrite 的会话也能按标准事务接口开启只读事务。

DSN 支持 `addresses`（逗号分隔 DNS/IPv4/带括号 IPv6，端口可省略）、`role=any/readwrite/readonly`、`balance=ordered/random/roundrobin/affinity`、`maxAttempts` 及四个同名 duration 参数 `attemptTimeout/retryDelay/blacklistTimeout/validationInterval`。addresses 中所有条目统一标记 role，参数遍历顺序不影响结果；混合角色、Group 和 Weight 使用 typed Config。未知/重复参数和非法值报错，不透传 Java 参数。

例如 `oceanbase://app@fallback/?addresses=entry-a:2881,entry-b:2881&role=readwrite&balance=roundrobin`；地址/权重由调用方配置，TNS/OCP 刷新和拓扑证明仍归 D27。Java 的多组 retry/maxReconnects 选项合并成有界 MaxAttempts/总预算，黑名单默认值不照搬 Java 的全局状态和长重试次数；allowMasterDownConnection 不允许跨角色降级；galeraAllowedState 属于外部产品能力，不伪造 OceanBase 状态 SQL。

根包 routing.go/failover.go 组合配置、拨号、认证及初始化；internal/routing 只处理自有 Address/Options、排序和健康，依赖标准库，不导入根包或 wire/auth/charset；无新增公共子包、池或后台 goroutine。

### 17.1.2 D27 TNS、OCP 与拓扑刷新契约

公开 API 仍在唯一根包，发现设施不另建连接池：

```go
type Topology struct {
    Service string
    Endpoints []Endpoint
}
type TopologySource func(context.Context) (Topology, error)
type DiscoveryOptions struct {
    Source TopologySource
    Service string
    RefreshInterval, RequestTimeout, MaxAge time.Duration
}
```

`Config.Discovery` 的 Source 非 nil 时启用；必须显式配置 Service，且不能同时配置 Routing.Endpoints。Addr 仍校验，但不作发现失败的降级地址。未配置发现时没有发现 goroutine；NewConnector 只做校验和快照，首次 Connect 才启动一个串行刷新任务。Source 必须返回独占的 Topology/Endpoints 并遵守 context；同一 Connector 串行调用，同一个函数被多个 Connector 共享时由调用方保证并发安全。

| 规则 | 当前实现 |
| --- | --- |
| 默认时间 | RefreshInterval=30秒、RequestTimeout=5秒、MaxAge=5分钟；禁止负值，MaxAge 不小于 RefreshInterval |
| 预算 | Connect 等待发现与拨号/认证/初始化共用 ConnectTimeout；发现 HTTP 请求由 Connector 生命周期和 RequestTimeout 约束。取消一个等待者不取消其他等待者共用的刷新请求；等待者立即按自己的 context 返回 |
| 缓存与发布 | 最多1024个规范化唯一地址；Service 必须精确匹配配置值，Role 严格过滤。失败/非法快照保留仍在 MaxAge 内的上次成功结果；过期后关闭降级通路，已有刷新失败立即报错，否则等待后续受限刷新；失败不触发调用方驱动的请求风暴。刷新不改 User、Database、Credentials、兼容模式或业务会话 |
| 选址 | 每次 Connect 固定一个成功发现的候选代次；刷新只影响之后取得快照的新 Connect。动态 MaxAttempts=0 按该次符合角色的候选数计算一次遍历；AttemptTimeout=0 按该候选数与总预算计算。各代共享保留地址的冷却状态和轮转计数，旧代迟到的拨号结果仍反馈到新代；共享健康表按最新成员裁剪，删除地址不会无限积累 |
| 关闭 | Connector 实现 io.Closer，sql.DB.Close 自动调用；Close 幂等、取消发现并最多等待 RequestTimeout。违规不响应取消的自定义 Source 会导致 Close 返回超时，不能强杀用户函数。关闭后 Connect 返回 ErrConnectorClosed，已交付的物理连接不绑定发现 context，可继续执行/关闭。driver.Open 创建的私有 Connector 在成功或失败后立即关闭发现设施 |
| 错误/隐私 | 发现错误仅返回固定类型/类别，不保留 URL、文件路径、HTTP 内容及 Source 原始错误；业务认证/拨号错误仍按原契约保留 errors.Is/As。无隐式日志、磁盘缓存、全局服务注册表 |

内置入口和格式：

- `ParseAddressDescription(description)`：无 I/O，返回 Topology；接受独立 `(DESCRIPTION=...)`。`NewTNSFileSource(path, alias)`：每次读取显式的本地常规文件，按别名精确匹配（不区分大小写），支持 `alias=(DESCRIPTION=...)`、Java 的 `(alias=(DESCRIPTION=...))`、多个别名和 `#` 注释；文件源不会用无别名 DESCRIPTION 顶替缺失别名。无环境搜索路径、默认文件或全局 watcher。跟随调用方配置的符号链接，目标必须是常规文件；拒绝设备/目录等非常规目标，打开前后检查类型与尺寸；配置文件应由调用方信任和管理，本地文件系统 syscall 无法由 Go context 中断，文件系统卡顿及并发替换不构成可强制中止的 I/O 保证。
- `NewOCPSource(endpoint, client)`：调用显式 HTTP(S) URL；client 可 nil，构造时复制 Client、共享其调用方管理的 transport/callback。标准 context 覆盖连接/响应读取；可在注入 transport 配置拨号阶段的更短超时。拒绝 URL 内嵌凭据、fragment 和重定向；最多读取1MiB，不落盘。兼容 Java 实际 HTTP 包装 `data.NET_SERVICE_NAME` + `data.DESCRIPTION`，以及直接 DESCRIPTION JSON；包装内显式失败状态被拒绝，元数据有界解析。包装的 NET_SERVICE_NAME 是逻辑服务身份，CONNECT_DATA.SERVICE_NAME 不是自动切库指令；直接 DESCRIPTION 以 SERVICE_NAME 作为逻辑身份。服务端变体需按真实响应联调。
- TNS/OCP 共同支持 ADDRESS_LIST/ADDRESS、TCP HOST/PORT（默认2881）、WEIGHT、CONNECT_DATA/SERVICE_NAME，以及显式 TYPE=master/slave/readwrite/readonly；组按出现顺序映射 Endpoint.Group。拒绝重复/未知字段、非法端口/角色、重复地址、尾随垃圾、损坏 UTF-8；深度最多16、解析节点有界。JSON 重复键按不区分大小写拒绝。TNS 引号/转义值和非 TCP 描述不猜测执行。
- Java 的 OBLB/LOAD_BALANCE、策略、重试和黑名单描述字段属于来源提示：识别并校验其格式，**执行策略统一由 Go RoutingOptions 决定，不从服务端覆盖本地重试和安全配置**。组内/组间策略及黑名单附加算法不逐项照搬 Java；需要这些策略时在调用方配置 RoutingOptions/Endpoint.Group/Weight，未知描述明确拒绝。外层包装元数据不参与选址。

使用方式：

```go
source, err := oceanbase.NewTNSFileSource("tnsnames.ob", "orders")
if err != nil { return err }
connector, err := oceanbase.NewConnector(oceanbase.Config{
    User: "app", Mode: oceanbase.ModeOracle,
    Discovery: oceanbase.DiscoveryOptions{Service: "orders", Source: source},
})
if err != nil { return err }
db := sql.OpenDB(connector)
defer db.Close()
```

DSN 增加 `tnsFile` + `tnsAlias` 或 `ocpURL`（两类互斥），必须搭配 `discoveryService`；可选 `discoveryRefreshInterval/discoveryTimeout/discoveryMaxAge`。`addressDescription` 为 URL 编码的静态 DESCRIPTION，与上述发现源和 addresses 互斥。动态源、混合角色和复杂分组优先 typed Config。静态 addresses 的显式 role 统一标记行为保持 D26；DESCRIPTION/TNS/OCP 中未标记的地址不自动继承 role，受限 Role 必须由实际来源标记，或调用方在有真实服务依据时显式包装 TopologySource 赋予服务角色。Service 标识不是租户认证证明，租户仍由凭据与服务端身份取证保证。

Java replication 的主/从地址映射为显式角色快照及两个标准 sql.DB；Java Aurora 的 replica_host_status/innodb_read_only、域名规则属于 Aurora 产品契约，不向 OceanBase 发出这些猜测 SQL。自有控制面、复制服务或 Aurora 服务通过 TopologySource 提供地址、角色和稳定 Service，沿用同一校验/缓存/关闭链路；驱动不内置一个冒充 OceanBase 原生拓扑的 Aurora 探测器。真实产品的查询、权限、角色变化和租户证明逐适配器取证，回调及本地夹具通过不代表服务端兼容通过。

依赖仍为 root → internal/routing → 标准库；内部 routing 拥有发现快照、解析、HTTP 和生命周期，不导入根包、wire/auth/charset。Connector 仅组合公开配置与内部类型，不把发现依赖接入单会话命令路径。

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

#### 17.2.1 D28 认证扩展的当前实现

`AuthOptions.AllowOldPassword` 默认为 false，控制 `mysql_old_password`，包括同名自定义工厂；显式开启才允许八字节旧挑战应答及单字节旧式切换。它不降低 TLS 门禁。`client_ed25519` 使用严格 32 字节挑战和密码派生签名；采用纯 Go `filippo.io/edwards25519 v1.1.0` 的标量/曲线运算，标准 `crypto/ed25519.Sign` 的种子再哈希不适用于此协议。算法向量和本地协议对端通过不代表 OceanBase 服务端已安装相应插件。

`AuthOptions.PasswordCharset` 默认 `utf8mb4`，通过现有严格字符集编码器转换内置密码及 PAM 回答；DSN 对应 `passwordCharset`。旧密码按编码后的字节计算，不复制 Java 默认系统编码/UTF-16 字符迭代的隐式行为。自定义插件得到原始 Go 字符串，自行决定编码。DSN `allowOldPassword` 只接受 true/false。

PAM `dialog` 的工厂每次创建独立轮次状态。`AuthOptions.PAM func(context.Context, PAMChallenge) (string, error)` 得到从 1 开始的 Round、UTF-8 Prompt、Echo 和 Last；2/3 表示回显，4/5 表示隐藏，低位为最后问题提示。空初始插件数据按 Java 的首密码流程处理，Prompt 为空；非空提示必须有合法问题类型，提示终止符只剥离一次。原始对话包与普通 auth-more-data 区分，所有回答统一先验证 TLS；回调错误、取消、畸形提示或 NUL 回答均不发送回答。nil 回调只使用一次 Credentials.Password，后续提示明确拒绝，不能把同一个密码自动复用为 OTP。Last 不能代替最终 OK。回调须响应 context，并支持多连接并发；驱动不启动脱离调用的认证回调 goroutine。

`auth_gssapi_client` 在 Windows 使用原生 SSPI 的当前 Windows 身份，不使用数据库密码。`AuthOptions.ServicePrincipalName`（DSN `servicePrincipalName`）可固定目标 SPN，覆盖服务器提供的 SPN；两者均按有界 UTF-8 校验，平台调用转换为 UTF-16。服务端数据为 NUL 结尾的 SPN、机制名称，另允许一个协议终止符；空机制默认为 Kerberos。只接受 Kerberos、Negotiate、NTLM，不把任意名称交给 Windows 选择其他安全包。

Kerberos 请求双向认证，并检查 SSPI 最终实际返回的 mutual 标志；未建立双向身份则失败。NTLM 和可能回退 NTLM 的 Negotiate 在每轮 token 生成前要求驱动验证的 TLS，不能把挑战应答等同于服务端身份已验证。凭据、上下文按每次插件使用独立创建并释放；只导入 `github.com/alexbrainman/sspi` 基础包，避免高级 Kerberos 包在全局初始化时查询平台并 panic。Windows 适配留在 internal/auth 的平台文件，不反向依赖根包或 wire。

多轮原始 token 插件可选择实现 `AuthConversation`（AuthPlugin 加 Complete() bool）。在 Complete 为 false 时，完整服务器包（包括 0/1/fe/ff 首字节）交给 Respond；仍逐轮执行 Security 门禁，最多32轮，不把 token 误作 OK、auth-more、切换或 ERR。Complete 为 true 后仅接受最终 OK/ERR；nil 续轮输出不发包，非 nil 空切片显式发空包。初始握手响应仍按协议包含认证字段。自定义工厂的认证策略、复制挑战与 io.Closer 契约继续适用。

Unix 提供方通过 `-tags=gssapi` 显式启用，在 Linux amd64/arm64/386、macOS amd64/arm64 使用系统 GSSAPI，机制限定为与 Java 标准路径一致的 Kerberos。采用 `github.com/ebitengine/purego v0.11.1` 调用 RFC2744 的名称、凭据、上下文和缓冲区 API；构建无需 C 编译器或 GSS 头文件，启用后运行环境须提供原生动态加载器和 C 运行库，GSS 系统库在使用时按固定名称加载。默认构建不导入 purego，保留 CGO_ENABLED=0 的静态部署能力。Linux 尝试 MIT 的 libgssapi_krb5.so.2、Heimdal 的 libgssapi.so.3/libgssapi.so；macOS 使用系统 GSS.framework；不接受服务端提供的库路径。未启用标签、未安装 GSS 系统库、缺失符号及未适配平台明确返回 ErrUnsupported，缺少票据/凭据作为认证错误返回。Windows 构建仍默认使用 SSPI。

Unix 每次认证以 GSS_C_INITIATE 获取系统默认缓存身份，按 NT_USER_NAME 导入 SPN，指定 Kerberos OID 和 MUTUAL 标志；不使用数据库密码，不设置进程环境、不写票据、不请求委派。KRB5_CONFIG/KRB5CCNAME 等由部署环境在启动前配置；身份隔离由独立进程/凭据缓存负责，不能用每连接 os.Setenv 切换身份。原始令牌上限16MiB，输入内存只在原生调用期间固定，输出复制后立即释放；即使原生调用报错仍清理已返回的句柄与缓冲区。最终检查实际 mutual 标志；关闭依次删除上下文、释放凭据/名称及系统库引用，删除上下文不发送额外网络令牌。

本机 SSPI NTLM 的原生初始 token 和经过驱动验证 TLS 的完整本机对端交换已运行；沙箱缺失 SSPI 凭据时只跳过对应原生测试，使用当前 Windows 用户进程补取正向证据。Unix 状态/清理单测和独立 C 库 ABI 用例已有 Windows amd64 本机运行证据；Linux/macOS 系统 GSSAPI 的运行及真实票据双向交换仍待验证，交叉编译不替代运行。SSPI/GSSAPI 系统调用同步执行，系统票据/KDC 查询可能超出 context 的严格墙钟预算；调用前后检查取消，返回后释放上下文并拒绝交付过期连接，不启动脱离调用的 goroutine。GSS 状态错误仅报告操作和 major/minor 状态码，不拼接可能含身份、路径或票据细节的系统错误字符串。

实例级 AuthFactory 禁止返回 nil 或 typed nil。插件可选择实现 `io.Closer`，驱动在切换、成功和失败路径恰好关闭每个已创建实例一次；Close 需及时释放本地上下文，不做网络操作。关闭错误阻止交付连接并与原错误合并，原始连接随后关闭。工厂须返回独立实例，不得复用含可变状态的全局对象。D30 ChangeUser 已复用共享门禁和实例持有机制，见 §16.3.2。

当前 D28 已落地上述认证子项、Windows SSPI、上述目标的 Unix GSSAPI 及 §9.0.1 的 Unix/Named Pipe；Shared Memory/OBProxy VC 的实现及各能力真实验收仍需继续。TCP 与基础 TLS 复用既有实现，IPC 不获得明文凭据豁免。

### 17.2.1 D32 TLS 配置的 Go 原生承载

Java 的12项 TLS 配置通过现有 `Config.TLSConfig` 与 `crypto/tls`、`crypto/x509` 完成适配；驱动不增加证书文件、store 密码、JVM 类型或 TLS 工厂配置层。两种 OceanBase 兼容模式采用相同的 SSLRequest、驱动内 TLS 握手和认证门禁。TLSConfig=nil 不主动使用 TLS；非 nil 时必须完成标准链/名称验证，服务端不支持、验证失败、版本/套件不匹配或回调报错均终止，不回退明文。

| Java option | Go 承载与差异 |
| --- | --- |
| useSsl | 非 nil TLSConfig 显式要求 TLS；仅程序化配置，没有自动安全降级 |
| serverSslCert / trustStore / trustStoreType | 应用用 x509.NewCertPool/AppendCertsFromPEM 或 AddCert 构建 RootCAs；nil 使用 Go/OS 系统信任源。自定义池替换默认根，若要追加，应先取得 x509.SystemCertPool 并检查错误。DER 先 ParseCertificate；没有 classpath、JVM 系统属性或 Java store 类型解释 |
| keyStore / keyStoreType | 应用用 tls.LoadX509KeyPair/X509KeyPair 加载 PEM 证书链与私钥，传 Certificates；动态证书使用 GetClientCertificate。证书链按叶证书在前排列；硬件私钥可由实现 crypto.Signer 的对象提供，具体实现由应用负责 |
| trustStorePassword / keyStorePassword / keyPassword | Java store 的解锁/格式转换由应用或部署侧完成；驱动接收已加载的池/证书/签名器，不获取这些密码。Go 标准加载器不直接解码 JKS/PKCS12 或加密 PEM 私钥；不得将忽略密码、加载失败后空证书连接当作等价支持 |
| enabledSslProtocolSuites | MinVersion/MaxVersion 控制连续版本范围；Go 1.25/1.26 默认最低 TLS1.2、最高当前支持的 TLS1.3，建议应用显式设 MinVersion。Java 未配置时沿用其 JSSE 运行时，不能从旧注释推断实际默认。离散版本集合可额外用 VerifyConnection 拒绝不在集合内的结果，不能关闭标准验证 |
| enabledSslCipherSuites | CipherSuites 只控制 TLS1.0–1.2，列表顺序由 Go 忽略；TLS1.3 套件不可通过该字段配置。应用可在 VerifyConnection 检查实际协商结果并拒绝，但不能用它改变 ClientHello 的 TLS1.3 套件列表。TLS1.2 及以下无共同可用套件、或无共同版本时由标准 TLS 握手失败；未知套件 ID 不会增加支持能力，TLS1.3 仍忽略该字段 |
| tlsSocketType | 固定 Java 注册的默认 TLS provider 对应 Go crypto/tls；额外验证及客户端证书选择由 tls.Config 回调承载，不迁移类名/SPI 加载，也不允许 DialContext 声称连接已验证。替换整个 TLS 引擎不属于该回调能力 |

完整[可编译文件加载示例](../tls_example_test.go)在应用侧读取 CA/客户端证书和私钥，遇到读文件、空信任池、证书或私钥错误立即返回；不会在加载失败后改用无 TLS 配置。示例仅编译，不读取项目外文件、不联网；TestTLSExampleFiles 用临时文件实际执行同一加载函数。真实证书、私钥访问权限及轮换由调用方管理，不把敏感材料或完整配置交给日志。

NewConnector 快照 TLS 配置的证书字节、证书池及可变切片/map；回调、私钥、已解析 x509.Certificate（包括 Leaf 和通过 AddCert 加入池的证书）、ClientSessionCache 等引用对象由调用方保证不可变或并发安全。证书池 Clone 隔离后续 AddCert，不深复制其中已解析证书对象。每个物理连接再克隆配置；回调不能改写共享配置。GetClientCertificate 应配合其 CertificateRequestInfo.Context 的取消；额外验证回调应及时返回，驱动不能中断应用中无界阻塞的同步回调。VerifyConnection/VerifyPeerCertificate 只在标准验证后收紧策略，错误保留给调用方；TLS 拒绝时不会先取数据库 Credentials。

TestTLSNativePolicy 覆盖两模式、TLS1.2/1.3、静态/动态客户端证书、服务端验证的完整客户端链及加密认证；TestTLSNativeRejection 覆盖 TLS1.2 的验证/选证书回调错误、无共同版本/套件，均在获取数据库凭据前拒绝。TestTLSNativeSnapshot 经完整初始化/Ping/QUIT 验证原配置后续变更不污染会话；TestTLSNativeCancellation 验证选证书回调的 context 取消。原有 TestTLSVerifiedAuthentication、TestTLSRequired、TestFailoverTLS、TestFailoverClientCertificateReject、TestAuthRejectsForgedTLS 与 ReadTimeout TLS 用例继续覆盖链/名称/降级/路由/缺失客户端身份及读取预算。

12个同名 Java DSN 参数、禁用校验开关及 DefaultOptions 注释列出的5个历史别名均明确拒绝；不通过 URL 读取证书/私钥文件。固定证据为 DefaultOptions（L188、L263、L385–L434、L682–L693、L740）、AbstractConnectProtocol（L350–L404、L673–L716）、DefaultTlsSocketPlugin 和 OceanBaseX509TrustManager；Java provider 注册清单只有默认实现。这里的本地 TLS 证据不代表真实 OceanBase/OBProxy 的证书部署、双向身份映射、系统信任源或 Java store 转换已经验收。

### 17.3 LOCAL INFILE

保留本地导入功能，默认不允许服务端按任意文件名读取本机文件。调用方显式传入 Reader 或按请求名解析 Reader 的回调；采用请求级或 Connector 级配置，不使用全局文件白名单。

导入时有 context、大小/时间约束及明确 Close 所有权；协议拒绝和数据发送中断也要正确结束/丢弃连接。该功能不会因未设计便被删掉，也不会为“全功能”而默认开放任意文件读取。

#### 17.3.1 D29 已实现的受控导入

公开入口是 `Import(ctx, exec, query, input)`，`exec` 接受 `*sql.DB`、`*sql.Conn` 或 `*sql.Tx`，输入为一次性的 `*LocalInfile`。配置与 SQL 分开，SQL 由业务方提供；导入不会开始、提交或重新执行事务。DB 入口先固定物理连接，Conn/Tx 保留调用方原会话；不另建驱动连接池或包。

```go
input := &oceanbase.LocalInfile{
    Name:     "input",
    Reader:   strings.NewReader("1\tvalue\n"),
    MaxBytes: 8 << 20,
}
result, err := oceanbase.Import(ctx, db,
    "LOAD DATA LOCAL INFILE 'input' INTO TABLE target", input)
```

- `Reader` 由调用方持有，驱动从不关闭；必须指定精确匹配的 `Name`。也可改用 `Open func(context.Context, string) (io.ReadCloser, error)`，由回调根据本轮请求名授权并返回输入，返回的非 nil Reader 由驱动恰好关闭一次，包括回调同时返回错误的情况。两种来源互斥；回调模式下非空 `Name` 仍须精确匹配。
- 请求名按会话字符集严格解码为 Go 字符串，校验非空、无 NUL 和长度上限，再匹配/调用回调；文件内容按原始字节发送，不隐式转码。驱动不把名称映射到文件、URL 或全局白名单。
- 握手在服务端提供时协商 `CLIENT_LOCAL_FILES`，只声明协议能力。普通 Exec/Query、建连初始化和其他内部命令均无输入授权；仅 Import 对应的单条命令可接受一个请求，重复/畸形/不匹配请求直接淘汰连接，不发送任何该请求的数据。
- `MaxBytes=0` 使用 `Limits.MaxLOBBytes`，分块不超过64 KiB和单消息上限；复用长参数的有界读取、精确上限探测与取消检查。`Timeout=0` 为30秒，覆盖固定连接、等待、发送及最终确认，并受更早的 context 时限约束。Reader/Open/Close 为同步调用，必须配合取消并返回；不会用脱离调用的 goroutine 隐藏阻塞，也不能递归访问同一受限连接。
- 正常 EOF 后先完成驱动持有 Reader 的关闭，再发送空结束包；随后必须收到最终 OK/ERR，其他响应淘汰连接。延续消息在包序号回绕时仍保持同一请求身份、trace 与诊断记录，复用标准/压缩/OB20 包流。
- 输入错误、取消、超限或关闭错误不发送成功结束包，立即丢弃物理会话；最终完整服务器 ERR 保留结构化错误且允许会话继续使用。错误以 `ErrLocalInfile` 标识，并屏蔽来源返回的 `driver.ErrBadConn` 重试信号；输入实例不能重复或并发消费。失败可能已有数据库效果，驱动不宣称自动回滚，事务效果须按服务端/存储引擎实际验证。

本地对端已覆盖两兼容模式及完整压缩/OB20 往返，不代表真实目标已支持 LOAD DATA LOCAL。`TestLocalImport` 保留四目标源码与手动 CI：MySQL 模式提供默认 SQL，Oracle 模式需配置对应 profile 的 `LOCAL_IMPORT_SQL` 模板，包含 `{table}`，请求名固定为 `input`；强制验收时缺失配置失败。数据库联调暂不执行。

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

### 18.3.1 D25 已实现的诊断与追踪契约

公开入口集中在根包 `DiagnosticOptions`、`RequestDiagnostic`、`PacketDiagnostic`、`TraceInfo` 和 `WithTraceInfo`；wire 叶包只做协议、采样和受限快照，不调用根包或外部 handler。没有新增依赖、全局追踪器、exporter 或后台日志队列。

- `Config.Diagnostics` 的 Request/Packet 回调显式启用元数据诊断；SQL 只提供 SHA-256 指纹与编码后字节数，包只提供方向、字节数、逻辑消息起始序号及错误标记。始终排除认证包、原始 SQL、参数、LOB 和服务端错误文本。Debug 不会扩大数据范围，也不自动执行额外数据库诊断 SQL。
- 普通事件为 Debug；`SlowThreshold > 0` 且达到阈值时为 Warn。零阈值不启用慢请求升级。`MaxRequests`/`MaxPackets` 为每次持锁区间的请求上限和每请求消息上限，零各取64，合法范围0–1024；达到上限只丢诊断，事件携带丢弃计数，不中断业务。无 logger/回调时不收集诊断。
- 请求边界为实际协议命令：prepare、execute/161、fetch、每个长参数片段、LOB 辅助 SQL、批量各项和清理命令独立计时；multi-send 合并各自写/读快照并等待写协程后交付。Rows 持有连接期间包含消费时间；这是连接占用时长，不宣称纯服务端执行时长。建连额外提供一次总耗时事件，覆盖拨号、认证和初始化，认证内容仍不采集。
- `Enabled`/handler/Request/Packet 均在驱动锁释放后同步调用；调用方须自行处理并发，避免递归访问同一受限 sql 连接池。回调内部可观察其他请求，跨回调的全局顺序没有保证。
- `Protocol.FullLinkTrace` / DSN `fullLinkTrace=auto|off|required` 默认 auto；请求 OB20 位8、FLT 位14、新 extra 位15、ShowTrace 位18。FLT 必须同时确认 OB20，缺少 required 或非法回显在业务前拒绝。日志关闭不关闭 FLT；未确认时保持普通行为。
- 新 extra 的 FULL_TRC key=2003；旧 extra 使用 `full_trc` ObObj varchar 对。64KiB 内校验嵌套 TLV、控制字段长度/概率/时间范围，未知新字段按长度跳过；FLT 开启时旧对象仅接受已实现的 null/varchar，其他类型明确拒绝。每命令首个外层帧发送 app/span，续帧不重复，长度预算预留 extra，仍保留 CRC、身份、序号和真实发送字节证据。
- app 字段 ClientIdentifier/Module/Action/ClientInfo 的 UTF-8 字节上限分别为64/64/64/256；拒绝 NUL、非法 UTF-8 和超长值，不截断。信息来自当前 context，每命令发送四个字段（含空值）清除池化残留；不把 app 内容写进日志。TraceID/ParentSpanID 为16字节 OceanBase 原始线序；调用方桥接其他追踪系统时负责转换其 ID 规范。
- 服务端控制按物理连接保存，部分更新原子合并；level、sample、record policy（1全量/2慢请求/3采样及慢请求/4关闭）、print sample、slow threshold、ShowTrace 决定采样/输出。首次控制到达前不擅自采样；ShowTrace 可强制采样。每个被采样命令生成新的 span；有调用方 TraceID 则沿用，否则新建；ParentSpanID 仅用于该 span 的 parent。反馈的 query 起止微秒值与诊断 ID 可由 Request 回调对接外部追踪，下一命令清空 query 时间。
- 完成 span 的 JSON driver log（group1/field1）和 ShowTrace（group2005/field2050）只包含 ID、固定驱动名和时间。每连接最多保留一个完成 span，在下一命令 extra 中发送一次，保留原 ID；不额外发 SQL、不在失败后重放。连接关闭或丢弃前的最后一个完成 span 可能没有后续命令上传，建连认证阶段只有本地总耗时事件，不冒充服务端 span。

协议夹具、nil logger、回调/handler 重入、采样更新/关闭、池化清空、片段/CRC、畸形数据及 fuzz 在本地验证；`TestFullLinkTrace` 集成源码对四目标登记直连 off、代理 required，必须另核对服务端 trace 中的 app 清空及 ID，客户端事件不能替代服务端可见性证据。当前没有数据库/代理联调结果，不计为真实兼容验收。

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
