# OceanBase Go 驱动开发计划

> 执行说明：按任务依赖逐项实现；执行时使用 executing-plans 工作流。每项先验证失败用例，再实现、验证并更新进度。本文不要求创建分支、自动提交或引入多代理流程。

目标：将 obconnector-j 的可观察功能完整适配为符合 Go 习惯的 OceanBase 驱动，并为每项功能保留实现与验证证据。

架构：以 `database/sql` 和标准连接池为入口，一个公共 `oceanbase` 包承载配置、标准接口及必要的扩展；共享协议与会话内核，按实际 Oracle/MySQL 兼容模式处理差异。

技术基线：Go 1.25、1.26；标准库 `database/sql`、`net`、`crypto/tls`、`log/slog`；优先纯 Go。必要的字符集和平台认证依赖由对应任务单独评估。

规范：[架构设计](go-driver-architecture-design.md)；功能依据：[Java 结构与功能大纲](obconnector-j-architecture-outline.md)；实时状态：[开发进度](go-driver-development-progress.md)。

## 1. 使用方式与当前基线

计划制定时仓库只有设计文档、忽略配置和 lint 配置，尚无 `go.mod`、Go 源码和测试。下列任务是实施约定；当前实际代码与执行结果以进度表为准。勾选只表示该步骤的证据齐备，不代表所属任务或功能已经验收；本地测试、平台运行和真实服务端验证分别记录。历史 RED→GREEN 依据进度中的当时记录，不以当前成功重跑补造失败证据。用户要求本阶段暂不进行数据库联调，相关验收条目保持未验证，先实现并运行本地/协议测试。

本计划采用已配置仓库对应的模块路径 `github.com/cocosip/go-oceanbase`，公共包名 `oceanbase`。若仓库归属以后改变，应在首个代码任务统一调整模块与示例导入。

开发顺序表示技术依赖，不表示将后面的能力排除出交付范围。每个任务在对应模式和服务端上独立验收；Go 接口、模拟协议和真实服务端三个层次不能相互替代。

技术实现依赖与验收证据依赖分别管理：D28 的 Linux/macOS 原生认证、真实 Linux VC 或 race 尚未运行，不妨碍在现有平台实现并测试 D30/D31。当前没有额外 Linux/CI 环境，本阶段继续可独立进行的本地开发和验证；目标平台运行及真实服务验收保留未完成，不能因此宣称对应组合已支持。开发阻塞必须注明缺少的具体实现或当前功能必需输入，不笼统沿依赖图传递平台环境缺失。

开始某项任务时先读取架构相应章节，再读取本任务及其直接依赖的接口。若证据证明需要改变架构契约，同步修改架构与本计划，不在代码中增加未记录的旁路。

## 2. 全局约束

- OceanBase 是数据库；Oracle、MySQL 是其兼容模式，模式来自认证响应，配置仅作为期望约束。
- `go.mod` 使用 `go 1.25.0`，同时验证 Go 1.26；不得使用 Go 1.27 才提供的接口或标准库内部符号。
- 普通使用保持 `sql.Open` / `sql.OpenDB`、`sql.DB/Conn/Tx/Stmt/Rows`；不增加自有连接池、JDBC 类层次或 ORM。
- `Logger *slog.Logger` 外部传入，nil 静默；禁止默认/全局 logger 和替代输出。
- Connector 保存配置快照。不得在已建立会话中换 Socket、换实际模式或透明重放可能已发送的业务操作。
- 标量数据遵守 `driver.Value`；精确数值不经 float64；连接相关资源不通过任意 struct Scan 或 `Raw` 逃逸。
- 扩展通过包私有请求值进入同一 Exec/Query 内核；参数校验不能改变会话或发送业务数据。
- 资源函数只接受非 nil `*sql.Conn` / `*sql.Tx`，XA 只接受 `*sql.Conn`；所有句柄检查连接、会话和事务代次。
- 上限基线：逻辑消息 64 MiB、LOB 自动物化 16 MiB、fetch 128 行、清理 1 秒、执行摘要 1024 项；均按架构规定提供配置和错误语义。
- 认证敏感阶段在发送前统一检查；明文密码要求驱动完成并验证 TLS，禁止认证降级。
- 文档只写仓库相对路径或固定版本上游 URL；不写开发机器的磁盘路径、凭据或测试服务地址。
- 保留当前工作区的其他修改；不擅自创建/切换分支；提交、推送或发布以用户明确授权为准。

## 3. 文件职责与接口约定

### 3.1 文件归属

| 文件/目录 | 职责 | 主要任务 |
| --- | --- | --- |
| `go.mod`、`doc.go`、`driver.go`、`config.go`、`dsn.go` | 模块、注册、配置及严格 DSN | D01 |
| `connector.go`、`conn.go`、`session.go`、`mode.go`、`capabilities.go` | 建连、实际模式、会话状态、资源代次 | D03–D06、D09、D15 |
| `internal/wire/` | 包流、基础握手消息、命令、压缩及 OB20；不依赖 database/sql | D02、D03、D23 |
| `internal/auth/` | 独立认证算法及算法阶段状态；根包组合握手和安全门禁 | D03、D28 |
| `transport*.go`、`shared_memory*.go`、`obproxy*.go`、`tools/obproxy-host/main.c` | 根包平台拨号及 OBProxy 独立原生宿主；不新增公共或内部包 | D28 |
| `execute.go`、`prepare_execute.go`、`rows.go`、`result.go`、`stmt.go` | 文本/二进制执行、标准接口、混合结果摘要；必要合并协议及通用策略 | D05、D08、D11、D14、D24 |
| `bind.go`、`bind_oracle.go`、`sql_lexer.go`、`client_prepare.go` | 绑定计划、词法、客户端预处理、显式 JDBC escape | D07、D10 |
| `codec*.go`、`types.go`、`internal/charset/` | 两模式基础/扩展类型、编码、时间 | D07、D08、D12、D18 |
| `tx.go`、`savepoint.go`、`xa.go` | 本地事务、保存点、XA 状态与恢复 | D09、D31 |
| `call.go`、`batch.go`、`long_data.go` | OUT/RETURNING、批量及一次性长参数 | D14、D20–D22 |
| `resource.go`、`lob.go`、`lob_mysql.go`、`cursor.go` | 固定会话资源、原生/模拟 LOB、滚动及可更新游标 | D15–D20 |
| `metadata.go`、`metadata_sql_mysql.go`、`metadata_sql_oracle.go` | 结构/参数元数据、共享类型字典 | D13、D18 |
| `internal/routing/`、`trace.go`、`logging.go`、`local_infile.go` | 选址、追踪、注入日志及受控导入 | D01、D25–D30 |
| `internal/testutil/`、各包 `testdata/`、`tests/integration/` | 故障对端、字节夹具、真实服务端验证 | D02 起持续维护 |
| `architecture_test.go` | 根包外部测试，检查生产及测试包依赖边 | D02 起持续维护 |
| `examples/`、`.github/workflows/ci.yml` | 可编译示例、工具链/平台 CI | D01 建立 CI，各能力同步扩展，D32 汇总 |

只在真实实现需要时创建文件；不要先生成大量空壳。不把整个驱动搬入 `internal` 后再通过另一层公共包装转发。

生产依赖仅允许 `oceanbase → internal/{wire,auth,routing,charset}`，四个内部包不反向导入根包，也不互相导入。Config/Mode、公开插件接口和会话执行归根包；wire/auth/routing/charset 各定义自己的窄输入/结果，根包负责转换。公开认证接口不能出现 internal 类型；auth 不直接收发协议包或 Socket，根包负责握手编排、受控发送和统一安全门禁。禁止为了打通依赖再建 common/core/interfaces 包。

`internal/testutil` 只依赖标准库，以 net.Conn/字节/context 模拟对端，不能导入根包或驱动内部包；生产代码不导入 testutil。D02 的 `architecture_test.go` 使用外部测试包，解析 `go list -deps -json -test -tags=integration ./...` 检查生产和测试依赖；真正循环还由编译及 `go list -deps ./...` 直接拒绝。新增包/依赖边同时修改架构 §4、此表及守卫断言。

内部命令和公开 API 的调用方向同样固定：Metadata 等公开函数通过标准 Session 调用进入包私有请求；字典解析/LOB 物化在驱动操作内直接使用当前物理会话的命令执行器，不能递归调用 Metadata 或 sql.DB/Conn/Tx/Session。完整响应边界解决包流顺序，不能解除 database/sql 已持有的连接锁；内部调度持有一次会话操作所有权，不重新获取该锁或自己的操作锁。D13/D16/D18 共享字典 SQL、描述和缓存，不共享公开调用栈，不使用辅助连接替代原事务可见性。

### 3.2 跨任务接口

以下是实施必须对齐的边界。架构已定义的数据类型直接沿用，不在计划中复制另一份字段说明。

```go
// D01 / D04：NewConnector 只校验/快照，Connect 才进行网络操作。
func ParseDSN(dsn string) (Config, error)
func NewConnector(cfg Config) (driver.Connector, error)

// D02：wire 只收发逻辑消息，内部负责分包/序号/长度限制。
// 下列方法位于 internal/wire，PacketIO 为具体类型。
func NewPacketIO(conn net.Conn, maxMessageBytes int64) *PacketIO
func (p *PacketIO) ReadMessage(ctx context.Context) ([]byte, error)
func (p *PacketIO) WriteMessage(ctx context.Context, payload []byte) error
func (p *PacketIO) ResetSequence()

// D07：仅包内使用；计划中的槽位记录名称、位置、IN/OUT 身份。
func buildBindPlan(query string, mode Mode) (bindPlan, error)
func bindArguments(plan bindPlan, args []driver.NamedValue) ([]boundValue, error)

// D11：使用架构 §11.4 的具体类型，不另建 Call API。
func Execute(ctx context.Context, session Session, query string, args ...any) (*Execution, error)
func (e *Execution) Rows() *sql.Rows
func (e *Execution) Snapshot() ExecutionSnapshot
func (e *Execution) Close() error

// D09 / D15–D22：固定会话扩展。
func Savepoint(ctx context.Context, tx *sql.Tx, name string) error
func RollbackTo(ctx context.Context, tx *sql.Tx, name string) error
func ReleaseSavepoint(ctx context.Context, tx *sql.Tx, name string) error
func OpenBlob(ctx context.Context, session Session, query string, args ...any) (*Blob, error)
func OpenClob(ctx context.Context, session Session, query string, args ...any) (*Clob, error)
func OpenTableBlob(ctx context.Context, session Session, target BlobTarget) (*Blob, error)
func OpenCursor(ctx context.Context, session Session, query string, opts CursorOptions, args ...any) (*Cursor, error)
func (c *Cursor) Fetch(ctx context.Context, pos CursorPosition, count int) (*sql.Rows, error)
func (c *Cursor) Close() error
func ExecBatch(ctx context.Context, exec Execer, query string, rows [][]any, opts BatchOptions) (BatchResult, error)
```

`wire` 的 []byte 生命周期、bindPlan 的具体字段由对应任务在包内确定；不得把它们作为公共扩展框架。D02 中逻辑消息已经取得全部内容，D05 不得假定可以读出跨消息的底层缓冲切片。

尚未展开的扩展值字段和接口在拥有它的任务内，先以编译通过的外部调用测试固定，再实现；下游任务使用其已落地声明，不自行发明同义类型。字段、默认值和错误行为需同步记录在对应 API 说明中。

尚未实现的显式功能/配置在使用前返回明确能力错误，不可静默接受后忽略。每个任务完成时移除对应的临时不可用路径，并增加证明实际行为的测试。

## 4. 里程碑与关键依赖

| 里程碑 | 任务 | 退出条件 |
| --- | --- | --- |
| G1 建连与标准查询 | D01–D05 | 两模式真实建连、Ping、文本查询及错误；敏感认证门禁和日志隔离有测试 |
| G2 执行和会话正确性 | D06–D12 | 参数、两类预处理、事务/reset、多结果、精度/时间通过；已发送操作不重放 |
| G3 完整扩展数据能力 | D13–D22 | 元数据、OUT、LOB、复杂类型、游标、批量在各支持组合验收 |
| G4 协议和运行能力 | D23–D30 | OB20/压缩、优化、FLT、HA/发现、平台认证、导入及会话辅助能力验收 |
| G5 分布式事务 | D31 | 两模式 XA 分支、恢复及结果不确定性验证 |
| G6 完整交付 | D32 | 全模块、配置迁移、真实环境、工具链/平台、性能及示例证据齐全 |

编号是推荐默认顺序，不是要求所有任务只能串行。具体依赖写在任务标题下；例如 D26 静态选址可以在 G2 后完成，不必等待全部 LOB。基础日志、取消和安全规则随所属能力实现，不等到 G4 才补。

## 5. 验证方法与环境约定

### 5.1 单项开发循环

每项四步使用相同含义：添加列出的测试及断言；执行命令确认功能尚不存在/断言失败；实现交付契约；重复执行直至通过并更新进度。若测试因工具链、权限或服务端不可用失败，应记录为环境问题，不能当作所需的功能失败证据。

每项完成前执行相关包测试和 `go test ./...`；只有影响新路径或发现新问题时再扩展检查。非平凡解析器增加 seed corpus、边界单测及 fuzz；文档修改只做结构、链接和内容检查。

每个 Dxx 的必需验证集见[开发进度 §5.3](go-driver-development-progress.md#53-逐任务必需验证集)。开始任务时展开到能力、模式、版本、路径及平台条目，验收前逐条核对；未配置必需环境只能待联调，不能填“不适用”或用本地通过代替。任务内测试名是最低场景要求，不能替代该验证集。

### 5.2 真实服务端测试

D04 建立 `//go:build integration` 的 `tests/integration` 包，按以下约定接收外部配置，测试输出不得包含 DSN：

| 环境变量 | 含义 |
| --- | --- |
| `OB_MYSQL_DSN` / `OB_ORACLE_DSN` | 两种兼容模式的直连测试入口 |
| `OBPROXY_MYSQL_DSN` / `OBPROXY_ORACLE_DSN` | 对应 OBProxy 测试入口 |
| `OB_TEST_PROFILE` | `mysql`、`oracle`、`proxy-mysql`、`proxy-oracle` 或 `all`；选择实际验证组合 |
| `OB_TEST_REQUIRE` | 设置为 `1` 时，所选组合缺凭据、服务或必要测试对象必须失败，不允许跳过后变绿 |

未显式启用 integration 的 `go test ./...` 不连接外部数据库。普通本地探索可以在环境缺失时跳过；里程碑/发布验收必须设置 OB_TEST_REQUIRE=1。测试用独立 schema/对象前缀，清理自己创建的对象，不用生产对象测试破坏性 DDL/XA。

运行集成测试前配置以上环境变量，然后执行：

```text
go test -tags=integration ./tests/integration -run TestSmoke -count=1
go test -tags=integration ./tests/integration -count=1
```

每次验收记录实际 Go/OS/架构、OceanBase/OBProxy 版本、模式、执行/包流路径、测试命令与结果。Oracle 兼容模式服务不可用时仅记录缺口，不用 MySQL/普通 Oracle 的结果代替。

### 5.3 工具链与故障验证

- D01 建立 `.github/workflows/ci.yml`，分别安装 Go `1.25.x`、`1.26.x`，在 Linux/Windows amd64 执行 `go version`、`go list -deps ./...`、`go test ./...`、`go vet ./...`；不能只改 GOTOOLCHAIN 字符串就声称测试了另一个版本。
- Linux/Windows amd64 验证基本能力；支持的系统执行 `go test -race ./...`；386 重点验证协议长度、索引和整数转换。不能运行的目标只记编译证据。
- 使用仓库现有 `.golangci.yml` 和兼容工具链的 golangci-lint v2，锁定 CI 工具版本，执行 `golangci-lint run`。
- D02 接入架构依赖边守卫、协议 fuzz 和 386 边界；D04 接入 §5.2 的集成 job，验收运行设置 OB_TEST_REQUIRE=1；D06 接入支持平台的 race。缺少外部凭据的普通 PR 可只运行本地 job，但不得把它当作集成验收；后续任务随能力增加 job/场景，D32 只做汇总和缺口收口。
- D02 故障对端支持指定第几次读写中断、短写、截断、延迟响应和调用计数。用实际收到的业务命令数证明没有重复执行。
- 接口断言与模拟包不能证明真实 SQL、目录、事务、锁、LOB 或 XA 语义；这些必须有 integration 测试。

### 5.4 重点失败条件与测试归属

| 输入或失败条件 | 用户应得到的行为 | 必须固定该行为的测试 |
| --- | --- | --- |
| 查询已部分发送后断线 | 保留结果未知错误，业务命令只收到一次 | D06 TestNoReplayAfterSend；D11 TestExecutionPartialError |
| 普通过程结果未读完就 Close | 有界最终化；只在成功时发布输出句柄；失败不泄漏 | D11 TestExecutionClose；D20 TestCursorOutPublish |
| 返回值未变化的 UPDATE、含大整数的生成 ID | 不误报缺行；不把零/未知或溢出混为一谈 | D11 TestExecutionOrder；D17 TestTableBlobTx；D19 TestCursorUpdateIdentity |
| 认证中途改为发送密码的算法 | 在敏感阶段写出前验证 TLS；不因切换而放松要求 | D03 TestAuthSwitch；D28 TestAuthPluginLifecycle |
| reset/事务结束后复用旧 statement/LOB/Cursor | statement 按代次重建；旧资源明确失效 | D09 TestResetGeneration；D15 TestResourceGeneration；D20 TestOutputLease |

## 6. 开发任务

以下复选框是任务内验收步骤；**全局状态只维护在进度表**。未勾选代表本计划没有执行证据，不代表重复创建另一个任务。

### D01 模块、公开配置与日志注入

依赖：无。对应：M01、M02、M15、M16；架构 §5–§7、§18。

文件：新增 `go.mod`、`doc.go`、`driver.go`、`config.go`、`dsn.go`、`connector.go`、`logging.go`、`config_test.go`、`dsn_test.go`、`logging_test.go`、`.github/workflows/ci.yml`、`docs/go-driver-options-mapping.md`。

交付：注册 `oceanbase`；实现 ParseDSN/NewConnector、Mode、配置枚举与既定默认值、配置快照。Config 的常用字段沿用架构，DialContext 使用 `func(context.Context, string, string) (net.Conn, error)`。此任务不宣称可建连；Connect 在 D04 完成，之前调用返回明确尚不可用错误，不返回伪造会话。

- [x] 测试 `TestConfigDefaults`、`TestDSNStrict`、`TestConnectorSnapshot`、`TestLoggerIsolation`：默认值与架构一致；未知/重复敏感参数拒绝；后续修改 map/TLS 不改变快照；nil logger 无输出，两个 Connector 输出互不串扰；错误不含密码或完整 DSN。
- [x] 执行 `go test . -run 'Test(Config|DSN|ConnectorSnapshot|Logger)' -count=1`，确认新断言失败。
- [x] 实现上述配置/注册；NewConnector 拒绝 InsecureSkipVerify；用户提供的日志 handler 不在内部网络锁下调用。建立固定 Java 版本 option 清单，逐项记录所属 Dxx、Go 承载或仅 Java 用途和迁移状态；未落地字段不得填成已支持。
- [ ] 同命令通过；建立 §5.3 的双 Go 版本、Linux/Windows test/vet/list/lint CI，记录真实工具链和执行链接。D01 验收不能写成“驱动已可用”。

### D02 标准包流与故障对端

依赖：D01。对应：M03、M04；架构 §9、§12.2。

文件：新增 `internal/wire/packet.go`、`packet_test.go`、`packet_fuzz_test.go`、`testdata/`、`internal/testutil/peer.go`、`peer_test.go`、`architecture_test.go`；扩展 CI。

交付：§3.2 的 PacketIO，按协议分包/重组、序号重置、最大逻辑消息限制；故障对端可控制读写和统计命令，不使用真实服务端作为单测前提。

- [x] 测试 `TestPacketRoundTrip`、`TestPacketLimit`、`TestPacketShortIO`：空消息、恰好分包边界及终止包、序号回绕、错误序号、超长/截断在分配前拒绝；短写不丢数据。
- [x] 执行 `go test ./internal/wire ./internal/testutil -count=1`，确认相关断言失败。
- [x] 实现包流和故障注入；字节夹具标明来源和协议版本，不保存认证秘密。实现 §3.1 的包依赖守卫，用一条反向导入夹具证明守卫拒绝，并接入已有 CI。
- [x] 同命令通过；执行 `go test ./internal/wire -run '^$' -fuzz FuzzPacket -fuzztime 30s`，无 panic/失控分配；执行 386 边界用例。

### D03 握手、基础认证、TLS 与模式识别

依赖：D02。对应：M03、M04；架构 §8–§9、§17.2。

文件：新增 `internal/wire/handshake.go`、`internal/auth/auth.go`、`native.go`、`sha2.go`、`clear.go`、对应测试；新增 `authentication.go`、`errors.go`，修改配置快照；会话模式锁定在 D04 完成。

交付：解析握手与认证响应、native/SHA/clear 基础路径及切换；共享状态机统一凭据门禁；实际模式/版本/能力分开。ModeAuto 只在首个成功初始化会话后锁定，失败尝试不锁定 Connector。

- [x] 测试 `TestAuthSecurityGate`、`TestAuthSwitch`、`TestTLSRequired`、`TestActualMode`：无 TLS 的 clear/full-auth 阶段发送明文凭据字节数为 0；证书/主机名失败不降级；自定义 Dial 伪造安全状态无效；模式不符关闭连接。
- [x] 执行 `go test ./internal/auth ./internal/wire . -run 'Test(Auth|TLS|ActualMode)' -count=1`，确认失败。
- [x] 实现握手、固定公钥的无 TLS 加密路径和认证阶段检查；根包适配公开认证工厂与 auth 算法输入/结果，工厂按会话创建状态，不泄漏 internal 类型或 Socket，不产生 auth→根包/wire 依赖。
- [x] `TestHandshakeIdentityEncoding`、`TestHandshakeIdentityEncodingRejected` 验证初始握手身份与选库按 Session.Charset 严格编码，覆盖两模式及静态/动态凭据；非法编码在插件创建和认证响应发送前拒绝，逻辑身份仍保留 Go 字符串。
- [x] 同命令通过；建立版本能力证据表 `docs/go-driver-compatibility-matrix.md`，只写已确认的源码条件，其余标记待取证，不猜服务端版本门槛。

### D04 建连、初始化、Ping 与集成测试入口

依赖：D03。对应：M01、M04、M05；架构 §5、§8–§9。

文件：新增 `conn.go`、`session.go`、`connector_test.go`、`tests/integration/env_test.go`、`fixtures_test.go`、`smoke_test.go`，完成 `connector.go`。

交付：Connector.Connect、driver.Conn 基本方法和 Pinger；连接总预算覆盖 Dial/TLS/凭据/初始化，失败路径释放连接；按 §5.2 建立测试环境入口和临时对象清理。

- [x] 测试 `TestConnectBudget`、`TestConnectorModePinning`、`TestPingFailure`：Dial/TLS/凭据/初始化各阶段共用总预算；并发初始化不混模式；初始化失败不锁模式；Ping 失败不伪成功。多地址候选循环在 D26 增加，复用本任务的总预算而非重置计时。
- [x] `TestDialContextNilConnection`、`TestDialContextErrorClosesConnection` 验证自定义拨号 nil/typed-nil 拒绝、错误链和重试次数、有效连接与错误同时返回时的关闭所有权；不调用 typed-nil 方法，不进入凭据回调或连接池。
- [x] 执行 `go test . -run 'Test(Connect|ConnectorModePinning|Ping)' -count=1`，确认失败。
- [x] 实现建连和初始化基线；早期尚未完成的 Exec/Prepare/BeginTx 明确报错，不把空实现当成功。
- [ ] 同命令通过；将 integration job 接入既有 CI，按四个 profile 执行 `TestSmoke`，验收设置 OB_TEST_REQUIRE=1，记录实际认证、模式、版本；缺环境标为待联调，不能通过 G1。

### D05 COM_QUERY、文本结果与标准计数

依赖：D04。对应：M06、M08；架构 §10.1、§11。

文件：新增 `execute.go`、`rows.go`、`result.go`、`error.go`、`codec.go`、`codec_mysql.go`、`codec_oracle.go` 及测试；新增 `tests/integration/query_test.go`。

交付：ExecerContext/QueryerContext、driver.Rows/RowsNextResultSet、基础列信息、EOF/OK/ERR/session tracking；标准结果计数溢出检查，服务端错误可 errors.As；首次行读取后仍正确处理后续响应。LastInsertId 在没有非零 OK insert ID 时返回 ErrUnsupported，与已知的 RowsAffected=0 分开处理；两个字段各自检查 int64 溢出，完整无符号 ID 由 Execute 摘要保留，不能推断主键。

- [x] 测试 `TestTextRows`、`TestResultCounts`、`TestRowsDrain`：NULL/空值不同、[]byte 所有权、EOF/OK 终止、多结果切换、计数为 0/溢出、错误 SQLState、提前 Close 后连接同步。
- [x] 执行 `go test . -run 'Test(TextRows|ResultCounts|RowsDrain)' -count=1`，确认失败。
- [x] 实现两模式标准文本查询与完整响应边界；未知类型明确失败，不能一律当 BLOB。
- [ ] 同命令通过；两模式执行 `TestQueryCRUD`、`TestQueryResults`；提供标准 OpenDB/Query 示例。G1 还须 D03 的安全和 D01 的日志测试通过。

### D06 取消、错误边界与连接淘汰

依赖：D05。对应：M05、M13；架构 §12.1、§12.2、§12.5。

文件：修改 `conn.go`、`session.go`、`execute.go`、`rows.go`；新增 `cancel_test.go`、`retry_test.go`、`validator_test.go`。

交付：Validator、并发关闭协调、context.AfterFunc 停止/等待；发送前失效可 ErrBadConn，发送后保留原错误及不确定性；清理预算统一使用。

- [x] 测试 `TestNoReplayAfterSend`、`TestCancelCompletionRace`、`TestCloseBudget`：部分写/丢响应时业务命令数为 1；旧取消回调不能关闭后续操作；Close 不无限等待；坏连接不能回池。
- [x] 执行 `go test . -run 'Test(NoReplay|CancelCompletionRace|CloseBudget)' -count=1`，确认失败。
- [x] 实现取消与失效状态，覆盖 Query 的完整 Rows 生命周期；不得为丢弃连接而返回可重试 ErrBadConn。
- [ ] 同命令通过；受支持平台执行 `go test -race . -run 'Test(NoReplay|Cancel|Close)' -count=1`，并接入既有 CI；把同类断言复用于后续资源任务。

### D07 词法绑定与参数转换

依赖：D05。对应：M07、M09；架构 §10.2–§10.3、§11.2。

文件：新增 `sql_lexer.go`、`bind.go`、`bind_oracle.go`、`bind_test.go`、`bind_fuzz_test.go`，修改 `codec*.go`。

交付：§3.2 的绑定计划；NamedValueChecker/NumInput；标准值、Valuer、uint64 及必要扩展输入。包私有请求与 SQL 参数分离，不产生校验副作用。

- [x] 测试 `TestBindPlan`、`TestNamedValueValidation`：引号、注释、q-quote、:=、匿名块分号；同名多位置、缺失/多余/混合参数拒绝；MySQL named 拒绝；Valuer 出错时网络写入为 0。
- [x] 执行 `go test . -run 'Test(Bind|NamedValue)' -count=1`，确认失败。
- [x] 实现词法状态机和一次参数转换；拒绝不支持的输入，不靠 fmt 拼接参数。
- [x] 同命令及绑定 fuzz 通过；两模式预处理任务复用同一计划，不各写一套参数计数规则。

### D08 服务端预处理与二进制结果

依赖：D06、D07。对应：M06–M09；架构 §7、§10、§11。

文件：新增 `stmt.go`、`stmt_test.go`、`binary_rows_test.go`；修改 `execute.go`、`codec*.go`；新增 `tests/integration/prepare_test.go`。

交付：ConnPrepareContext、StmtExecContext/StmtQueryContext、二进制参数/结果、statement ID、类型头和 NULL bitmap；服务端 prepare 缓存先保证有界与正确性。

- [x] 测试 `TestServerPrepare`、`TestBinaryRows`、`TestStmtOwnership`：NULL bitmap 位边界、uint64、大数、重复执行换类型、关闭幂等、错误会话 ID 拒绝；执行响应失败不转客户端重试。
- [x] 执行 `go test . -run 'Test(ServerPrepare|BinaryRows|StmtOwnership)' -count=1`，确认失败。
- [x] 实现服务器 statement 的创建/执行/关闭；旧标准方法委托 context 路径，不引入另一种语义。
- [ ] 同命令通过；两模式 `TestPrepareRoundTrip` 比较文本/二进制结果；Statement 可安全被 DB 在不同物理会话重新建立。

### D09 本地事务、保存点与 reset 代次

依赖：D08。对应：M05、M13；架构 §12.3–§12.4。

文件：新增 `tx.go`、`savepoint.go`、`tx_test.go`、`reset_test.go`，修改 `session.go`、`stmt.go`；新增 `tests/integration/tx_test.go`。

交付：ConnBeginTx/driver.Tx、§3.2 保存点函数、Managed/Full reset；Full 不可靠时丢弃；reset 后 statement generation 失效并按需重新 prepare。

- [x] 测试 `TestTxOptions`、`TestCommitUnknown`、`TestResetGeneration`：不支持隔离级别拒绝；丢失 COMMIT 响应不报告已回滚；reset 失败返回安全的 ErrBadConn；固定 Conn 相邻调用不 reset。
- [x] 执行 `go test . -run 'Test(Tx|CommitUnknown|Reset)' -count=1`，确认失败。
- [x] 实现事务状态/恢复、保存点标识符引用及 statement 代次检查；不换物理连接完成事务。
- [ ] 同命令通过；两模式 `TestLocalTx`、`TestSavepoints`、`TestStmtAfterReset`；验证池复用时状态与资源清理。

### D10 客户端预处理与显式 escape 转换

依赖：D07、D08。对应：M06、M07；架构 §10.1–§10.2。

文件：新增 `client_prepare.go`、`escape.go`、`client_prepare_test.go`、`escape_test.go`；修改 `execute.go`。

交付：显式客户端 prepare 策略；公开 `RewriteJDBCEscapes(query string, mode Mode) (string, error)`；mode Auto 不具备足够上下文时拒绝改写，不猜模式。

- [x] 测试 `TestClientPrepareEncoding`、`TestJDBCEscapes`：引号/反斜杠/多字节/二进制、模式与会话转义设置、JDBC 日期/函数/call 语法；普通 SQL 默认无改写；注释中的占位符不替换。
- [ ] 执行 `go test . -run 'Test(ClientPrepare|JDBCEscapes)' -count=1`，确认失败；当前进度未记录历史失败，不能由现有通过结果推定。
- [x] 复用 D07 词法/参数计划，按实际会话编码；只有发送前确认 prepare 不支持时才允许已配置的降级。
- [ ] 同命令通过；`TestPrepareEquivalence` 在两模式比较客户端/服务端的值和错误，不以字符串相似替代执行等价。

SQL/客户端预算补充：TestSQLTextEncodedBudget、TestClientSQLTextEncodedBudget、TestSQLBudgetStrictEncoding、TestSQLExecutionEncodedBudget、TestOutputSQLTextEncodedBudget 覆盖实际编码大小、片段累计、字面量转义、两模式三包流公开文本/server/client 预处理及 Oracle OUT，共58个本地场景。原文解析预检按码点数有界；完整消息按转码后的内容、命令/协议开销及空格限制，超限发送前拒绝后原会话仍可 Ping。GBK/latin1 收缩误拒已复现并修复，真实 SQL 等价与服务端验收仍单列。

### D11 Execute、多结果摘要与计数策略

依赖：D08、D09。对应：M08；架构 §11.4。

文件：新增 `execution.go`、`execution_test.go`，修改 `rows.go`、`result.go`、`capabilities.go`；新增 `tests/integration/multi_result_test.go`。

交付：§3.2 Execute/Execution、架构 ResultInfo/ExecutionSnapshot、MatchedRows/ChangedRows、AllowMultiStatements 和 MaxResultItems；普通 Rows 不插入假行集。

- [x] 测试 `TestExecutionOrder`、`TestExecutionPartialError`、`TestExecutionClose`：UPDATE→SELECT→UPDATE 顺序、RowSetIndex=0、零与未知、完整 uint64 ID、直接 Query 错误仍保留摘要、上限失败、Close/自动关闭的最终状态。
- [ ] 执行 `go test . -run 'TestExecution' -count=1`，确认失败；当前进度未记录历史失败，不能由现有通过结果推定。
- [x] 实现解析器向私有承载对象发布摘要、线程安全快照及统一最终化；服务端没提供的内部过程计数不制造出来。
- [ ] 同命令通过；两模式 `TestMultiResult`；MySQL no-op UPDATE/upsert 计数对照，Oracle 拒绝 ChangedRows。Snapshot 仅观察，不触发网络。

### D12 全类型、字符集和时间语义

依赖：D08、D10。对应：M09；架构 §11.2。

文件：新增 `types.go`、`number.go`、`temporal.go`、`internal/charset/`、对应测试；扩充 `codec*.go` 和 `tests/integration/types_test.go`。

交付：Number、NString、RowID/Raw、TimestampTZ/LTZ、IntervalYM/DS 的无损 Scanner/Valuer；具体类型及 sql.Null[T] 保留绑定身份；字符集/collation、NCHAR 和实际会话时区；Session.ParseTime 默认保留文本，显式解析无时区日期/时间戳时拒绝非法/DST 歧义；普通 Scan 来源保持标准值。

- [x] 测试 `TestTypeBoundaries`、`TestTemporalLossless`、`TestCharsetRoundTrip`：最大/最小/越界数、负/超24小时 TIME、零日期、纳秒/region/DST、月间隔、非法编码、NULL/空字符串/空字节；不把 NUMBER 转成 float64。
- [x] 执行 `go test . ./internal/charset -count=1`，确认新增断言失败。
- [x] 实现类型映射和文本规范；字符集确需依赖时只引入对应编码库，记录版本与用途，不开发通用编码框架。
- [ ] 同命令通过；`TestAllScalarTypes` 交叉模式/文本/二进制/客户端路径验证；支持性差异进入兼容矩阵。

Oracle DATE 元数据补充：协议类型12在 Oracle 报告 DATE、MySQL 报告 DATETIME；ParseTime 只改变 Scan 来源，不改变类型名称或截断时分秒。TestDateColumnTypes/TestDateParameterMetadata 提供本地回归；TestAllScalarTypes 已补 DATE/TIMESTAMP 列名检查与 Oracle DATE 含时间夹具，集成仅编译，真实验证仍未勾选。

二进制文本参数预算补充：普通 string、NString 和复杂类型文本字段按实际转码字节检查内容预算，保留编码前码点数下界、严格编码及长度前缀/命令累计上限。TestParameterEncodedBudget、TestComplexTextEncodedBudget、TestPreparedParameterEncodedBudget 共63个本地场景覆盖收缩/膨胀、精确边界、零/负预算，以及两模式适用组合和三包流公开预处理；超限在发送前拒绝，原会话仍可 Ping。SQL 文本/客户端插值的独立预算见 D10；LongData 源字节和转码后字节双重限制保持。真实数据库验收不据此勾选。

Number 预算补充：TestNumberNormalizedBudget、TestComplexNumberNormalizedBudget、TestNumberSQLNormalizedBudget 共46个本地场景覆盖规范化收缩、定点展开、精度、零/负预算、复杂字段累计前缀及两模式三包流 server/client 公开预处理，另检查正/负极端指数在展开前拒绝。按最终定点字节及平台 MaxInt 检查后才构造结果，避免先复制超限尾数；FuzzScalars 增加收缩种子及0–510字节的变化预算，检查精确值不变。真实精度与服务端往返仍待联调。

LTZ 预处理等价性补充：TestLTZPrepareEquivalence、TestLTZSQLPrepareEquivalence 共56场景覆盖客户端与服务端的跨 region 会话转换、纳秒、同 region 墙上时间保留、源 DST 缺口/重叠及目标重叠拒绝、会话年份边界，以及三种包流上的公开预处理和本地拒绝后的原会话复用。客户端与二进制路径共用会话墙上时间校验，不以偏移 SQL 字面量绕过二进制路径的歧义或范围限制；FuzzScalars 增加 LTZ 跨 region、源缺口及年份边界种子，固定 UTC 会话和充足预算时检查两路径接受/拒绝一致。固定偏移会话即使与源 region 同名，也按初始化实际设置的偏移转换；另覆盖日期进位、源 region 不可解析、DST 歧义及年份越界。真实时区转换及服务端类型语义仍待联调。

NString national charset 补充：[TestNStringPrepareCharsets/TestNStringSQLPrepareCharsets](../national_charset_paths_test.go) 共62个离线场景，覆盖两种预处理的显式 national charset、连接字符集回退、缺省 utf8mb4、空值、非法 UTF-8、不可表示输入，以及 SQL latin1/ASCII 与 national UTF16 的分离。三种包流上的公开 database/sql 执行验证拒绝前后 statement 与原会话复用，不发送无效执行命令。FuzzScalars 在输入最多512源字节、独立8192字节充足预算下检查8种 national 配置（含回退）的接受/拒绝等价性，不把二进制内容和 UNISTR 膨胀预算强行等同。真实 NCHAR 往返仍待联调。

### D13 数据库、结果和参数元数据

依赖：D09、D12。对应：M11；架构 §16.1。

文件：新增 `metadata.go`、`metadata_sql_mysql.go`、`metadata_sql_oracle.go`、`metadata_test.go`；补齐 `rows.go` 五个 RowsColumnType 接口；新增 `tests/integration/metadata_test.go`。

交付：ColumnTypes、参数计划/服务端参数元数据、catalog/schema/表/视图/同义词/列/主外键/索引/权限/过程函数/类型查询。公开入口采用 `Metadata(ctx context.Context, session Session, filter MetadataFilter) (*sql.Rows, error)`；filter 的 Kind 是封闭枚举，每类结果有固定列名/类型。

- [x] 测试 `TestMetadataFilters`、`TestColumnTypes`、`TestMetadataInternalBoundary`：过滤值参数化、模式固定、未知长度/精度 ok=false、标识符引用、schema 大小写与同义词，不伪造不可得数据；公开与内部入口复用查询计划，内部不重入标准库会话。
- [x] 执行 `go test . -run 'Test(Metadata|ColumnTypes)' -count=1`，确认失败。
- [x] 调用示例、MetadataFilter 与 17 类列模式见架构 §16.1.1；两模式 SQL 及同物理会话执行器已实现，不重入公开 Metadata。响应与 gate 所有权已测试；描述缓存随 D18 类型描述建立，共享 D13 查询计划，不缓存任意字典 Rows。
- [ ] 同命令通过；`TestMetadataCatalog` 与已创建对象真实字典比较，记录权限不足和版本不支持的明确错误。

本地完成：两工具链全包 test/vet/386、普通/integration lint、目标重复与 FuzzMetadataFilters 通过。TestMetadataCatalog 仅编译，最后一项真实对照仍未勾选；详见进度 D13。

BIGINT UNSIGNED 的 ScanType 须为 uint64，保证按元数据反射创建的 Scan 目标能接收完整无符号范围；有符号 BIGINT 保持 int64，普通行值来源保持无损 int64/string。TestUnsignedBigIntScanType 覆盖三执行路径/三包流、边界和 NULL；TestAllScalarTypes 的真实边界对照仅编译。

MySQL 的 BINARY/VARBINARY 类型名按字符集63区分，不能将带 BINARY_FLAG 的普通文本列改成二进制类型。TestBinaryStringColumnTypes/TestBinaryStringParameterMetadata 覆盖两模式、三包流及结果的三执行路径，验证原始字节、空值/NULL、ScanType、长度及会话复用；Oracle VARCHAR2/CHAR 保持。TestAllScalarTypes 的固定 BINARY(4)/VARBINARY(100) 对照仅编译。

### D14 标量 OUT、INOUT 与 RETURNING

依赖：D11、D12。对应：M07、M08；架构 §13.1、§13.3。

文件：新增 `call.go`、`call_execution.go`、`call_mysql.go`、`prepare_execute.go`、`out_test.go`、`out_paths_test.go`，修改 `bind*.go`、`execute.go` 和必要的 wire 命令定义；新增 `tests/integration/call_test.go`。

交付：sql.Out、带显式数据库类型/长度/精度的 Out、RETURNING；普通内置输出暂存转换后写回，自定义 Scanner 的可能部分副作用明确返回。Oracle RETURNING 在本任务交付所需的最小 COM_STMT_PREPARE_EXECUTE 编码/响应和版本能力门禁，D24 复用它扩展通用策略；不把必要基础协议推迟为优化，不让 D14 反向依赖 D24。

- [x] 测试 `TestOutValidation`、`TestOutFinalization`、`TestOutReturningProtocol`：nil/不可写目的和不支持类型在发送前拒绝；INOUT 初值；NULL 不能写成非空零值；溢出不部分更新内置目标；Scanner 失败不隐藏先前副作用；RETURNING 必要合并命令、输出顺序及能力不满足时零业务发送。
- [x] 执行 `go test . -run 'TestOut' -count=1`，确认失败。
- [x] 实现两模式实际 OUT 响应及 RETURNING 必需的合并基础路径；变量中转只在固定会话完成并清理；输出在最终响应后生效。未经源码与目标服务验证，不能把普通分离式 prepare/execute 当作 RETURNING 的替代。
- [x] 同命令本地通过；协议对端覆盖多个标量输出、函数、匿名块和错误；连接句柄输出由 D20 实现。
- [ ] `TestScalarCall`、`TestReturning` 在真实两模式/代理中验证上述行为；已有集成源码仅编译，未执行数据库测试。

本地实现与验证已完成；`TestScalarCall`、`TestReturning` 仅完成集成源码编译，按当前约束暂不执行真实数据库联调。真实模式/版本验收不随本地任务勾选完成。

### D15 固定会话资源与生命周期

依赖：D09、D11。对应：M08、M10、M13；架构 §10.3、§13.2、§14.1。

文件：新增 `resource.go`、`resource_test.go`；复用 `execution.go` 的 Session 校验，修改 `conn.go`、`bind.go`、`execute.go`、`stmt.go`、`rows.go`、`tx.go`、`reset.go`、`call_execution.go`。

交付：包内具体资源记录、代次检查、父 statement 租约及私有请求分派；公开 Session 只沿用架构的 Exec/Query 调用面。

- [x] 测试 `TestResourceSession`、`TestResourceGeneration`、`TestResourceLease`：DB/包装器/typed nil 发送前拒绝；不能跨 Conn/Tx/reset 使用；最后一份租约才释放服务器 statement；Close 不结束调用方事务。
- [x] 执行 `go test . -run 'TestResource' -count=1`，确认失败。
- [x] 实现共享身份和清理规则，不保存 Raw 回调中的 driverConn，不增加公共 ResourceManager 或全局句柄注册表。
- [x] 同命令通过；故障、事务结束及 reset 时未发布资源不泄漏；每项扩展后续复用这些断言。

本项交付内部生命周期基础；具体 LOB/游标记录和服务器释放命令随 D16/D19/D20 接入，不据此声明高级句柄可用或真实数据库验收通过。

### D16 原生 BLOB/CLOB/NCLOB

依赖：D12、D13、D15。对应：M10；架构 §14.1–§14.3。

文件：新增根包 `lob*.go`（公开入口、原生调度、字符、修改、有界行集），新增 `internal/wire/lob.go` 与测试；修改 Rows、codec、资源、合并执行及握手；新增 `tests/integration/lob_test.go`。

交付：OpenBlob/OpenClob；先固定以下操作面，再实现原生后端。Reader/Writer 适配器绑定显式 context，不保存打开操作的 context 作为永久寿命。

```go
func (b *Blob) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error)
func (b *Blob) WriteAtContext(ctx context.Context, p []byte, off int64) (int, error)
func (b *Blob) Size(ctx context.Context) (int64, error)
func (b *Blob) Truncate(ctx context.Context, size int64) error
func (b *Blob) Close() error
func (c *Clob) ReadChars(ctx context.Context, off, count int64) (string, error)
func (c *Clob) WriteChars(ctx context.Context, off int64, value string) (int64, error)
func (c *Clob) CharLength(ctx context.Context) (int64, error)
func (c *Clob) TruncateChars(ctx context.Context, count int64) error
func (c *Clob) Close() error
```

Blob 的短读/短写遵守 io.ReaderAt/WriterAt 语义，off 为 0 起点。Clob 的 off/count 以 0 起点 Unicode 码点计，不按 UTF-8 字节计；后端字符单位不一致时明确转换或拒绝，不能静默分裂字符。WriteChars 返回已写码点数。

- [x] 测试 `TestLOBLocator`、`TestLOBInterleave`、`TestLOBNoReentry`、`TestClobOffsets`：V1/V2、NULL/空/临时/持久、内联/外联、超过16MiB、分块短读、字符/字节偏移区分；未读响应内不插入 LOB 命令。用真实 database/sql QueryContext/Rows.Next + 协议对端覆盖原会话物化，限定超时证明不重入公开会话/操作锁，不只断言内部 helper 被调用。
- [x] 执行 `go test . -run 'Test(LOB|Clob)' -count=1`，确认失败。
- [x] 实现 locator 查询完整结束后发布；Clob 字符操作以 rune/字符计数命名并单独定义索引，不能复用 Blob 字节索引；临时 LOB 释放与本地关闭分清。
- [x] 同命令通过；QueryContext/Rows.NextResultSet、标量 OUT 与混合结果的原会话物化已覆盖；无辅助连接。双版本 test/vet/386、lint 及 locator fuzz 本地通过。
- [ ] `TestNativeLOB` 在原事务读取未提交数据、取消/释放/reset；源码与手动 CI 入口已添加且仅编译，数据库联调按当前约束暂缓。

UTF16 覆盖按架构允许的拒绝边界处理：改变已有字符代理对宽度时写入前 ErrUnsupported；相同宽度覆盖和追加可用。具体 NCLOB 服务端字符单位及 collation 仍待真实目标验证。

### D17 MySQL 表内 BLOB 模拟 locator

依赖：D13、D16。对应：M10；架构 §14.4。

文件：新增 `lob_target.go`、`lob_mysql.go` 和相应测试，实现 BlobTarget/OpenTableBlob；新增 `tests/integration/table_blob_test.go`。目标解析与两种后端共处根包，无新依赖边。

交付：显式表/列/完整主键身份、OCTET_LENGTH/SUBSTRING 分块读取、Tx 内锁定后的 WriteAt/Truncate；Oracle 对应目标可解析原生 locator。

- [x] 测试 `TestBlobTarget`、`TestTableBlobBounds`、`TestTableBlobTx`：复合主键、标识符、Valuer 一次、Key 修改隔离；缺行/NULL/空；off+len 溢出、追加/相同值/空写、Conn 修改前拒绝。
- [x] 执行 `go test . -run 'Test(BlobTarget|TableBlob)' -count=1`，确认失败。
- [x] 按架构 §14.4 实现锁定存在性、参数化分块 SQL 和代次；不把 ChangedRows=0 当缺行，不自动扩展稀疏空洞。
- [x] 同命令通过；故障对端补齐跨块、取消/断线前缀、不重放、只读/未知引擎、容量/警告、Oracle 原生事务行锁和最大偏移空操作。
- [ ] `TestTableBlob` 的大于物化上限、并发锁/回滚、Truncate 等真实 SQL/锁证据验收；源码和手动 CI 入口已提供，本阶段按要求仅编译。

本地实现状态与验证证据见开发进度 D17；真实数据库行为不计入本地通过。

### D18 OBJECT、ARRAY 与类型描述

依赖：D12、D13、D16。对应：M09、M10、M11；架构 §11.2、§14、§16.1。

文件：新增 `complex.go`、`complex_codec.go`、`type_descriptor.go`、复杂值普通/故障/fuzz 测试与 `internal/wire/complex.go`，修改 codec/字典/预处理/OUT/Rows；新增 `tests/integration/complex_test.go`。

交付：Object/Array Scanner/Valuer、真实复杂类型绑定协议、自描述版本化 JSON 文本扫描表示；类型描述只在同一物理会话的单业务命令内按模式/schema/名称/协议版本复用，每业务命令/reset 清空，避免跨权限/schema 复用。0 表示服务端当前版本。

- [x] 测试 `TestComplexLossless`、`TestDescriptorRefresh`、`TestDescriptorNoReentry`：嵌套 NULL/空集合、十进制文本、base64、时间和字段顺序、同义词/类型版本、最大嵌套深度；DDL 失配刷新但不重放已发送操作。通过真实 database/sql 的 QueryContext/Rows.Next 和协议对端覆盖描述缓存未命中，限定超时、同一物理会话、先读完响应再查字典，断言不重入公开会话或操作锁。
- [x] 执行 `go test . -run 'Test(Complex|Descriptor)' -count=1`，确认失败。
- [x] 实现显式类型名与字段计划，不反射任意 struct/slice；描述查询只在完整响应边界通过内部执行器完成，不调用 Metadata/Session/sql.Conn 等公开入口。
- [x] 同命令本地通过；补齐复杂 OUT 列数、共享物化预算、后续复杂行集排空、非法集合元素头、关闭最终 OUT 超时及缓冲描述刷新回归。
- [ ] `TestComplexTypes` 已提供对象/集合、NULL 元素、精度/RAW/DATE、嵌套 LOB、原事务和复杂 OUT 的四目标集成源码，本阶段仅编译；真实服务端类型兼容待联调，字节对端不替代验收。

本地支持边界见架构 §11.2.2：仅服务端预处理二进制复杂结果；明确拒绝文本复杂结果、客户端插值、非 NULL TIMESTAMP/LOB 字段输入及无已确认元素头的集合。后续真实环境需补齐对应协议证据与兼容验收，不以本地实现完成宣称全功能验收。

### D19 游标、滚动、刷新与可更新结果

依赖：D13、D15、D16。对应：M08；架构 §15。

文件：新增根包 `cursor.go`、`cursor_protocol.go`、`cursor_test.go`、`cursor_update.go`、`cursor_update_test.go`；新增 `tests/integration/cursor_test.go`；复用 statement 原连接 gate 内的 prepare，不新增子包或反向依赖。

交付：OpenCursor/Fetch/Close、CursorOptions/CursorPosition；position 明确 next/absolute/relative；Update/Delete/Insert/Refresh 用封闭操作面、列变更和可靠行身份，不提供任意操作命令 map。

- [x] 测试 `TestCursorFetch`、`TestCursorUpdateIdentity`：批次先关闭、0/负/越界位置、有界 static 缓存、sensitive 不以缓存冒充；join/聚合/无行标识拒绝更新；no-op 更新不误报并发删除。
- [x] 执行 `go test . -run 'TestCursor' -count=1`，确认失败。
- [x] 实现实际支持的 offset fetch/定位及参数化 DML；ChangedRows 仅在可靠锁定存在性下支持可更新能力，否则发送前拒绝。
- [x] 同命令本地通过；协议对端覆盖两模式支持条件、LOB fetch、更新身份、错误及事务结束失效。
- [ ] 在真实两模式/代理执行 `TestCursorModes`、`TestCursorUpdates`，验证游标与并发变化；集成源码仅编译。

当前本地实现的具体位置、版本、主键/事务及刷新边界见架构 §15；集成场景已提供源码并仅编译，真实环境验收不勾选为通过。

### D20 REF CURSOR 和流式 LOB 输出

依赖：D14、D16、D19。对应：M08、M10；架构 §13.2。

文件：新增 `cursor_out.go`、`cursor_out_test.go`、`cursor_child_test.go`、`tests/integration/cursor_out_test.go`；修改输出最终化、Execution、游标协议、资源租约、二进制行解码及 CI。

交付：CursorOut 与显式 Out 到 **Blob/**Clob；只有 Execute 接受连接句柄输出；普通 REF CURSOR 列继续使用标准子 driver.Rows 寿命。

- [x] 测试 `TestCursorOutPublish`、`TestOutputLease`：目的初始 nil、多个输出、提前 Close 成功发布、读取失败不发布；Execution.Close 后 Cursor 可用；最后句柄释放父 statement；后续 Fetch 不继承旧 context。
- [x] 执行 `go test . -run 'Test(CursorOut|OutputLease)' -count=1`，确认失败。
- [x] 复用 D11 最终化与 D15 租约；输出转换及最终响应成功后再原子发布内置句柄。
- [x] 同命令本地通过；协议对端验证混合普通结果/计数/多个游标/LOB，以及关闭、失败和父 statement 租约释放。
- [ ] 在真实两模式/代理执行 `TestProcedureResources`，验证过程资源与关闭/失败行为；集成源码仅编译。

当前实现的模式、版本、子 Rows 缓冲/深度和发布边界见架构 §13.2；本地实现与协议测试完成，集成源码仅编译，真实环境验收不计通过。

### D21 长参数和分段上传

依赖：D08、D12、D15。对应：M07、M10；架构 §10.1、§13.4、§14.2。

文件：新增 `long_data.go`、`long_data_test.go`；修改 stmt/bind、wire 参数头、OUT/游标及共享 Rows 取消清理；新增 `tests/integration/long_data_test.go`。

交付：显式长参数输入，Reader 的所有权/关闭规则在类型中固定；LONG_DATA/PIECE_DATA 按模式能力选择，长度未知时有总上限，不读取整个内容后再上传。公开 *LongData 的 Length/MaxBytes/Text、借用 Reader 和一次消费规则见架构 §13.5；复用同一 gate，辅助上传失败淘汰连接，上传后 execute 前取消亦清理。普通 PrepareClient 明确拒绝，OUT 的必要原生路径保留。

- [x] 测试 `TestLongDataChunks`、`TestLongDataFailure`：空/未知长度、分块边界、Reader 报错/取消、部分写、限制超出；错误后没有 rewind/第二次执行。
- [x] `TestLongDataNilReader` 拒绝全部可为 nil 的 Reader 具体类型；`TestLongDataNilReaderSession` 验证两模式/三包流在预处理前拒绝、不消费对象，纠正输入后可继续使用，并接受非 nil 的函数/map/slice/channel 空 Reader。
- [x] 执行 `go test . -run 'TestLongData' -count=1`，确认失败。
- [x] 实现一次性长参数编码和失败资源清理；不把上传能力当作 MySQL 表内随机访问的替代。
- [x] 同命令通过；`TestLongParameters` 四目标源码覆盖两模式大对象内容哈希及提交/回滚，仅编译，真实语义待联调；无须日志输出对象内容。

### D22 批量、bulk、array binding 与改写策略

依赖：D11、D14、D18、D21。对应：M07；架构 §13.4。

文件：新增 `batch.go`、`batch_protocol.go`、`batch_array.go`、批量测试、`internal/wire/pipeline.go` / `stmt_array.go` 及测试、`tests/integration/batch_test.go`；修改绑定、执行起点和共享 Rows 最终化。

交付：ExecBatch、BatchOptions/BatchResult/BatchError；逐项状态、已知/未知计数、默认遇错停止，显式继续；实现逐项、多发包、安全改写、bulk、array binding 的能力选择。

- [x] 测试 `TestBatchResults`、`TestBatchAtomicity`：中间失败/后项状态、计数未知、OUT、分块限制；自动提交下不把可部分成功任务改成单语句整体失败；断线不重发已发送项。
- [x] 执行 `go test . -run 'TestBatch' -count=1`，确认失败。
- [x] 逐项路径与受限 multi-send 已实现；原生 Conn/Tx 一次标准库锁定、DB 全批次固定会话。无等价性证据时回退逐项，不改变已有 Tx 或隐式提交；清理错误保留确认成功，取消不丢失先前 SQL 错误。
- [x] 同命令通过；`TestBatchStrategies`、`TestBatchTxBoundary` 及 wire `TestPipelineWritesAhead` 对比内容、顺序、错误、事务和真实提前发包。BatchRewrite 已补受限 MySQL 多语句执行、显式权限及逐块服务端开关门禁；Oracle 原生 array binding 已补固定>=4.2.1、可写事务及显式继续门禁、列式参数/保存异常解析；多 VALUES/bulk 和不满足条件的请求回退逐项，见架构 §13.4。`TestBatchExecution` / `TestBatchRewriteExecution` / `TestBatchArrayBindingExecution` 四目标源码仅编译，真实服务器支持/等价性待验证。

### D23 压缩、OB20、新元数据与校验

依赖：D06、D08。对应：M03、M04、M15；架构 §9。

文件：新增 `internal/wire/compress.go`、`ob20.go`、`checksum.go` 及测试；新增根包 `protocol.go`、session tracking 解码与 fuzz，修改 capability/包流选择、列解释及批量策略门禁。

交付：标准/压缩/OB20 包流共享命令编解码，request ID、序号、扩展字段和校验；auto/off/required 三态以实际协商为准。

- [x] 测试 `TestCompressedPackets`、`TestOB20Frames`：跨包边界、校验失败、未知可跳过字段、错误序号、压缩膨胀上限；required 不支持时建连失败，不能发业务后换协议。
- [x] 执行 `go test ./internal/wire -run 'Test(Compressed|OB20)' -count=1`，确认失败。
- [x] 实现包流变体及新的列元数据解析，保持同一响应状态机；解压/分配前检查大小。
- [x] 同命令和 `FuzzFrames` 本地通过；两模式三包流、逐消息预算、字节证据、ID 回绕、取消与帧批量回退均有本地测试；四目标 `TestPacketModes` 源码仅编译。
- [ ] 固定真实版本后执行四目标 `TestPacketModes`，验证已登记正向及拒绝；当前按用户要求不联调，不能据本地测试勾选真实验收。

D23 已本地实现，验收未完成。独立五项配置及协议边界见架构 §9.1（五项配置均为三态）；代理 checksum 与 OB20 CRC 分别映射。进度和验证证据见开发进度 §5.6 D23。

### D24 PrepareExecute、缓存与执行优化

依赖：D08、D11、D14、D21、D23。对应：M06、M07；架构 §3、§10。

文件：扩展 D14 的 `prepare_execute.go`，修改 `stmt.go`、`execute.go`、`rows.go` 和批量/游标调用点；新增 `execution_options.go`、`stmt_cache.go`、`stmt_types.go` 及测试、编码 benchmark。

交付：复用 D14 的必要合并基础协议，扩展 Java 对应的通用 prepare/execute 策略、参数类型头复用、checksum/cache、按能力选择的优化路径；缓存按物理会话有界 LRU。OUT 回归以已完成的 D14 为显式前置。

- [x] 测试 `TestPrepareExecuteGate`、`TestStatementCache`：边界版本/未知版本保守选择、类型变化/缓存驱逐/Full reset、合并路径错误不双执行；结果与普通服务端执行等价。
- [x] 执行 `go test . -run 'Test(PrepareExecute|StatementCache)' -count=1`，确认失败。
- [x] 按固定源码在兼容矩阵登记各优化条件，接入既有执行计划；只在业务发送前降级。
- [x] 本地命令及参数类型/SQL CRC 黄金包测试通过；完成编码 benchmark，不据此宣称服务端吞吐提升；四目标 `TestExecuteStrategies` 源码仅编译。

- [ ] 固定两模式及代理实际版本后执行 `TestExecuteStrategies`，验证返回值、OUT、错误及资源关闭；当前按用户要求不联调。

D24 已本地实现，真实验收未完成；配置、缓存及句柄边界见架构 §10.1.1，门禁见兼容矩阵 §15，执行证据见开发进度 §5.6 D24。

### D25 slog 诊断、profiling 与 Full Link Trace

依赖：D11、D23。对应：M15；架构 §18。

文件：扩充 `logging.go`，新增 `trace.go`、`trace_test.go`、`diagnostic_test.go`、`tests/integration/trace_test.go`。

交付：结构化慢请求、受限 SQL/包诊断回调、OB20 FLT；请求级 `WithTraceInfo(ctx context.Context, info TraceInfo) context.Context`；可选 OTel 桥接不强制 exporter。

- [x] 测试 `TestDiagnosticRedaction`、`TestTraceIsolation`：nil logger、handler 重入不在网络锁内死锁、凭据/LOB/参数不出日志；复用连接无 trace 串扰；slog 关闭不阻断协议 FLT。
- [x] 执行 `go test . -run 'Test(Diagnostic|Trace)' -count=1`，确认失败。
- [x] 定义 TraceInfo 的 module/action/client 信息字段和长度上限；为 query/fetch/LOB/batch/建连指定独立请求边界。
- [x] 同命令本地通过；客户端协议已实现，nil logger、跨命令隔离及脱敏有本地证据。
- [ ] 执行 `TestFullLinkTrace` 对照真实 OBProxy/服务端可观察信息；集成源码仅编译，没有服务端追踪证据时只记客户端实现完成。

D25 实现契约见架构 §18.3.1：诊断仅元数据/指纹；有界同步交付；每命令 app 清空；server-controlled 采样和一次性延后 span 上报；无强制 exporter。真实服务端可见性不计本地完成。

### D26 静态多地址、负载与建连故障切换

依赖：D06、D09。对应：M12；架构 §17.1。

文件：根包 routing.go/failover.go、配置/DSN/connector/reset/tx；internal/routing/address.go、selector.go、health.go 及测试；集成源码与 CI。

交付：静态候选列表、随机/轮转/亲和/组、共享有界健康/黑名单；角色固定于 Connector；重试只发生在新建连接总预算内。

- [x] 测试 `TestAddressSelection`、`TestFailoverBudget`、`TestRoleIsolation`：退避/过期/并发、跨租户隔离、认证/模式拒绝不无限循环；事务及已发送查询始终不换 Socket。
- [x] 执行 `go test ./internal/routing . -run 'Test(Address|Failover|Role)' -count=1`，确认失败。
- [x] 实现地址选择和建连重试；读写两角色使用两个标准 sql.DB，BeginTx 不换线。
- [x] 本地双工具链/实际386/test/vet/lint、目标重复回归及地址 fuzz 已通过，进度同步到 D26。
- [ ] race/Linux/真实入口保持待验收。TestConnectionFailover 源码与 CI 已加入，本阶段仅编译；每个 profile 至少配置两个同租户/模式真实入口，再验证新连接切换/恢复和活动事务不重拨。业务丢响应不重放先由协议对端验证。

API、默认值、TLS alert 拒绝重试和初始化单次预算见架构 §17.1.1；不允许切换现有 Socket。

### D27 TNS、OCP 与拓扑发现

依赖：D26。对应：M02、M12；架构 §17.1。

文件：根包 `discovery.go`、配置/DSN/Connector 生命周期；新增 `internal/routing/tns.go`、`ocp.go`、`topology.go`、对应测试；更新兼容矩阵、集成入口与 CI。

交付：扩展地址描述/TNS/OCP、replication/aurora 对应的发现适配；按实际服务能力记录映射，不把外部产品名称直接当 OceanBase 支持承诺。D27 公开 API、来源策略映射、OCP 真实包装与外部 TopologySource 边界见架构 §17.1.2。

- [x] 测试 `TestTNSParser`、`TestOCPRefresh`、`TestTopologyChanges`：嵌套/非法描述、超时/无效响应、缓存过期、刷新后仍保持逻辑服务/角色一致；关闭 Connector 终止刷新但不取消活动业务连接。
- [x] 执行 `go test ./internal/routing -run 'Test(TNS|OCP|Topology)' -count=1`，确认失败。
- [x] 用标准库解析/HTTP 和生命周期 context 实现有界刷新；未配置时无背景 goroutine，不注册全局源。
- [ ] 同命令通过；对应可用服务 `TestDiscovery` 验证角色与租户，不具备环境的适配逐项标记待联调。

### D28 完整认证插件与平台传输

依赖：D03、D26。对应：M03、M16；架构 §9、§17.2。

文件：扩充 `internal/auth/` 的 old/Ed25519/GSSAPI/PAM；新增平台适配文件/build tags、对应测试；更新 Config 的实例级工厂说明。

交付：Java 已注册认证能力及 TCP/Unix socket、Named Pipe/Shared Memory/OBProxy VC 的适配或同等能力映射；每种说明目标平台、协商条件和依赖，不用总开关宣称全平台完成。

- [x] 已建立本地 `TestAuthPluginLifecycle`、`TestPlatformTransport*` 算法/协议与 Windows IPC 测试：每会话独立状态、弱算法默认拒绝、初始/切换/ChangeUser 全阶段 TLS 门禁；自定义传输无明文豁免；取消/平台错误清理。
- [x] Windows 本地算法/传输的失败→通过记录见开发进度 D28；`go test ./internal/auth . -run 'Test(AuthPlugin|PlatformTransport)' -count=1` 已通过。Linux/macOS 系统库及目标传输运行仍按下项保留未验证。
- [x] 在协议明确的适配上实现；认证/传输依赖使用平台文件隔离，不让基础包强制加载本地库。维持 §3.1 的单向包依赖，平台认证算法不反向引用根包配置或 wire。
- [ ] 同命令通过；按进度 §5.4 将每个算法及传输单列到目标平台和服务端组合，分别记录正向认证/收发、拒绝、切换和清理证据；全部必需条目满足后才验收，只有交叉编译不能标记该平台验收完成。

D28 分步推进：old password 显式启用、Ed25519/PAM、密码字符集、插件 io.Closer 生命周期和原始 token 会话，Windows SSPI、Unix GSSAPI，以及 Unix socket/Windows Named Pipe/Shared Memory 已有本地实现；对应算法/初始/切换/安全/清理、原生 ABI 和 IPC/SSPI 本机对端收发回归已建立。Unix GSSAPI 的目标范围、库依赖与未运行边界见架构 §17.2。Shared Memory 的无关联分配取消/软预算和单缓冲批量回退见 §9.0.5；OBProxy VC 独立宿主见 §9.0.6，真实平台待验证。D30 公开重新认证已补，真实票据、域 Kerberos 与各服务组合继续验证，总体勾选保持未完成，证据见进度及兼容矩阵。

TestAuthPluginGSSNativeSessions 在当前 Windows 身份下以系统 NTLM、本地 TCP 与已验证 TLS，覆盖两模式的完整 database/sql 初始化、连续两次 ChangeUser、每次基线重建/Ping、最终认证拒绝及原物理连接关闭/池隔离。复用既有原生 SSPI 对端，普通单测在缺少身份凭据时明确跳过；正向证据必须核对原生目标无 skip，不能用沙箱下的包级成功替代。该测试仅覆盖标准包流，不证明域认证、真实数据库授权或原生资源无泄漏。

Named Pipe 地址校验同时覆盖 Config/DSN 的非法 UTF-8 主机/管道名拒绝，以及合法 Unicode 的保持与本机原生管道收发；对应 TestPlatformTransportPipeUTF8、TestPlatformTransportNamedPipeUTF8，避免 Windows UTF-16 转换造成端点别名。

TestPlatformTransportUnixMixedSessions / TestPlatformTransportNamedPipeMixedSessions 复用既有混合会话夹具，在真实系统 IPC 端点核对两模式/三包流、原连接复用、预处理/Rows/事务/错误恢复及 QUIT/关闭；Windows AF_UNIX 的 EOF 边界与原连接关闭断言见架构 §9.0.1。默认单测和现有 CI 自动执行；Unix 目标平台须分别留存实际运行证据，不能用 Windows 的 AF_UNIX 通过代替 Linux/macOS。

D28 VC 子批次按架构 §9.0.6 实施：根包 obproxy.go 管理 DialContext 工厂、固定入口及初始化单例；obproxy_host.go 用标准 os/exec 和管道启动独立 C 宿主 tools/obproxy-host/main.c，按 build tags 限制生产支持平台。宿主绑定官方 init/close，将原生信号/locale/allocator 与 Go 隔离；Go 复用 Unix net.Conn。TestOBProxy* 验证配置拒绝、并发一次初始化/冲突/取消/失败缓存、两模式/三包流及 TLS 门禁；独立 C 共享库和宿主夹具验证 JSON/void**/返回码、临时句柄、坏握手/缺库/缺符号和父进程 EOF（含 init 卡住）。普通构建不要求 C 编译器；obproxytest 用本机 gcc 构建夹具。真实 Linux 库/版本/信号/生命周期及服务仍需运行验收，不能凭 Windows 合成 ABI 或交叉编译勾选 D28。

### D29 LOCAL INFILE 受控导入

依赖：D06、D21。对应：M07 及 LOCAL INFILE；架构 §17.3。

文件：新增 `local_infile.go`、`local_infile_test.go`、`tests/integration/local_infile_test.go`。

交付：显式 Reader/请求解析回调、请求级权限及大小/时间限制；服务端文件名不直接映射本地文件；Reader Close 所有权明确。

- [x] 测试 `TestLocalInfilePolicy`、`TestLocalInfileFailure`：未授权请求无文件读取；Reader 中途失败/取消/超限正确终止响应或淘汰连接；不隐式重传。
- [x] 执行 `go test . -run 'TestLocalInfile' -count=1`，确认失败。
- [x] 通过已有执行状态机发送授权内容，不建全局文件白名单；配置与导入 SQL 分开。
- [ ] 同命令通过；`TestLocalImport` 校验真实入库行数/内容和失败事务行为，支持条件写入矩阵。

D29 本地实现：`Import` + 一次性 `LocalInfile`，精确名称 Reader/授权 Open 二选一；Reader 调用方关闭，Open 返回值驱动关闭。输入只挂在本次文本命令上，沿用长参数的有界读取和既有 Rows 状态机；wire 叶包新增延续写入，避免序号回绕重启诊断/trace，无新包或依赖。当前不连接数据库，最后一项的本地命令已通过，四目标集成源码及 CI 已补但未运行，不能勾选真实验收。具体契约见架构 §17.3.1。

### D30 会话辅助、warning、ChangeUser 与显式取消

依赖：D09、D11、D15、D26、D28。对应：M05；架构 §12.5、§16.3。

文件：新增 `session_ops.go`、`session_ops_test.go`、`cancel_query.go`、`tests/integration/session_ops_test.go`。

交付：固定 *sql.Conn 的 schema/catalog、客户端信息/变量、结构化 warning、用户切换；显式 CancelQuery 使用已取到的连接身份/代次及已配置 Connector 的辅助通道。

- [x] 测试 `TestChangeUserIsolation`、`TestChangeUserConversation`、`TestWarnings`、`TestCancelQueryIdentity`：旧 statement/LOB/Cursor 失效、换用户物理连接不回原池；多轮重新认证、显式凭据隔离和失败关闭；nil logger 不丢 warning；本地会话/命令代次防止旧令牌命中下一操作，服务端 ID 复用的协议边界见架构 §16.3.4。
- [x] 执行 `go test . -run 'Test(ChangeUser|Warnings|CancelQuery)' -count=1`，公开 API 缺失先失败，各子项再通过。
- [x] 用封闭函数而非任意私有指令接口公开；ChangeUser 重走认证门禁并重建基线，KILL 成功不表示原查询未执行。
- [ ] 同命令通过；`TestSessionOperations` 验证身份、警告、辅助通道 TLS 和池隔离；缺权限返回明确错误。

D30 首批实现：session_ops.go 提供固定 sql.Conn 的 schema/catalog、客户端信息/变量，setter 拒绝活动事务并使物理会话关闭时淘汰，防止状态泄漏；缓存清理、实际模式转义/编码和封闭请求复用既有内核。Warnings 与 ResultInfo.Warnings 提供独立于 logger 的 OK/EOF 数量快照，MySQL 详情/本地清除、ChangeUser、显式 CancelQuery 已分批补齐。TestSessionOperations、TestChangeUserSessions、TestWarningDetails、TestCancelQuerySessions 四目标源码仅编译，真实取消、身份/权限/代理仍待联调；辅助 TLS 已有本地对端测试。D28 Shared Memory/VC 尚未闭环，整项 D30 保持进行中，不能把已支持传输上的子项替代其依赖完成。

D30 ChangeUser 子项：新增 change_user.go 与协议/TLS/分帧/生命周期测试，显式凭据和数据库、同连接重新认证、同模式约束、共用持锁初始化、server/client statement 身份代次与固定资源失效均已实现。旧 statement.Close 不发送可能复用的 ID，发送后失败关闭连接、成功关闭时退出原池；不修改 Connector 或调用旧凭据提供方。四目标 TestChangeUserSessions 源码和 CI 已补，尚未连接数据库；warning 详情/清除见 §16.3.3，取消见 §16.3.4。详见架构 §16.3.2。

D30 warning 子项：warnings.go 提供 Warning、ReadWarnings、ClearWarnings；MySQL 按需读取当前诊断区，Oracle 保留协议数量并拒绝详情，不补造 SQLSTATE。详情有行数/累计字节限额，读取失败不返回部分列表；本地清除抑制至下一个普通 SQL，池 reset 和切换身份不泄漏旧详情。TestWarningDetails 系列覆盖清除、模式、事务、reset、预处理/失败命令再启用、字符集、包流、取消及畸形/超限；TestWarningDetails 四目标集成源码要求非零警告 SQL，只编译未联调。详见架构 §16.3.3。

D30 取消子项：cancel_query.go 提供不透明 CancelTarget、预先 QueryTarget 及 CancelQuery。每个目标只接受一次尝试，绑定首个普通 SQL/首个 pipeline 分块；原实际入口单次辅助连接复用显式 Connector 策略，校验用户/租户、模式、Network 和已验证 TLS。完成、reset、切身份及关闭使目标过期；清理/下一命令前协调在途 KILL，写出后丢确认淘汰原会话，不重放业务。服务端仅有 ID 的不可原子验证窗口明确记录于架构 §16.3.4。本地两模式、TLS、权限/畸形/超时、完成竞态、单次目标及生命周期测试已补；四目标 TestCancelQuerySessions 及 CANCEL_SQL/CANCEL_CODE 手动 CI 输入仅编译，不连接数据库。D28 依赖未闭环，D30 仍进行中。

### D31 XA 分支、状态与恢复

依赖：D09、D14、D18、D30。对应：M14；架构 §16.2。

文件：新增 `xa.go`、`xa_test.go`、`tests/integration/xa_test.go`、`examples/xa/`。

交付：XID{FormatID int32, GlobalID []byte, BranchID []byte}；显式 XAStart/XAEnd/XAPrepare/XACommit/XARollback/XARecover/XAForget，固定 *sql.Conn；flags 用专用值，校验长度及状态。两模式命令/XID 编码分别实现，协调日志归调用方。

```go
func XAStart(ctx context.Context, conn *sql.Conn, xid XID, flags XAFlags) error
func XAEnd(ctx context.Context, conn *sql.Conn, xid XID, flags XAFlags) error
func XAPrepare(ctx context.Context, conn *sql.Conn, xid XID) (XAVote, error)
func XACommit(ctx context.Context, conn *sql.Conn, xid XID, onePhase bool) error
func XARollback(ctx context.Context, conn *sql.Conn, xid XID) error
func XARecover(ctx context.Context, conn *sql.Conn) ([]XID, error)
func XAForget(ctx context.Context, conn *sql.Conn, xid XID) error
```

XAVote 区分 prepared/read-only，错误单独返回；没有 read-only 反馈的路径不能编造该票。XAFlags 只接受该阶段支持的 XA 标准值及已取证的 Oracle 扩展值（架构 §16.2.1）。输入/输出 XID 的字节切片按值复制，避免调用方修改已登记分支身份。

- [x] 测试 `TestXAState`、`TestXARecover`、`TestXAUnknownCommit`：禁止与 sql.Tx 混用、非法 flags/长度、join/resume/suspend、1PC/2PC、准备分支不随本地对象关闭回滚；结果未知不重放。
- [x] 执行 `go test . -run 'TestXA' -count=1`，确认失败。
- [x] 用外部调用测试固定 flags/返回码和 Recover 的 XID 列表，再实现原生 XA/DBMS_XA；不明分支状态的物理连接不能回池。
- [ ] 同命令通过；两模式 `TestXATransactions`、`TestXARecoveryAfterDisconnect` 由新连接恢复；明确 Forget/超时等真实支持条件，不伪造成功。

D31 本地实现：新增 xa.go / xa_protocol.go，原生 OUT 复用内部调用并保留发送阶段证据。两模式本地协议测试及四目标集成源码、示例已补，契约见架构 §16.2.1；Forget/RM 超时明确不支持。D30 的平台依赖未闭环，整体依赖与真实服务端验证继续按进度表保留，不以本地测试代替验收。

### D32 配置逐项迁移、完整验收与交付

依赖：D01–D31。对应：M01–M16、LOCAL INFILE、Java 生态映射；架构 §19–§21。

文件：完善 `docs/go-driver-options-mapping.md`、`docs/go-driver-compatibility-matrix.md`、`README.md`、`examples/`、`.github/workflows/ci.yml`、`tests/integration/` 和 benchmark。

交付：逐项 Java option 的 Go 配置/标准库替代/平台适配/仅 Java API 用途；两种模式完整验证清单、Go 1.25/1.26 与平台 CI、可运行示例、基准与资源稳定性证据。配置表从 D01 起随功能维护；CI 从 D01 建立，本项扩展和核对完整矩阵，不首次引入。按进度 §5 核对 D01–D31 所有必需条目，不能在收口时缩减目标以换取全绿。

- [ ] 从固定 Java 版本 DefaultOptions、注册入口及测试逐项对照：每个选项有归类、拥有任务、实际 Go 字段/替代、默认值差异与测试；只有更名不是功能完成依据。
- [ ] 执行 §5 的工具链/平台检查、全套 integration、故障用例和目标 fuzz；显式 require 环境，不允许 SKIP 冒充全量通过。
- [ ] 编译并运行标准查询/事务、预处理、过程、多结果、批量、LOB/复杂类型、游标、XA 示例；OB20/HA/认证等不适用组合给出明确限制和证据。
- [ ] 统计正确性后的 CRUD/prepare/cache/batch/LOB/fetch 基准及长时间资源曲线；确认 M01–M16 和配置逐项覆盖后才将 G6 标记完成。发布、提交、推送仅在用户另行要求时执行。

D32 首批已补 Config.Interactive、ConnectionAttributes 及握手/ChangeUser 的共同编码与边界校验，详见架构 §6.1。逐项迁移清单同步该子项；D28 平台缺口、其他选项、示例、基准和完整验收继续保留，D32 不提前计完成。

D32 排序规则子项已补 Session.Collation/DSN collation，MySQL 同一 SET NAMES/COLLATE 保持编码基线，Oracle 显式拒绝；初始化恢复与 ChangeUser 共用原路径，契约见架构 §6.2。两种模式的本地正/负向用例及 TestSessionCollation 集成源码已补，集成仅编译。

D32 代理登录子项已补 Config.ProxyUser/Credentials.ProxyUser/DSN proxyUser，不解析括号用户名；严格编码、模式/能力拒绝、ChangeUser 清除/池隔离、CancelQuery 双身份比较及外部离线示例见架构 §6.3。TestProxyUserSessions 已接手动 CI，真实身份/权限待联调；不因此关闭平台依赖或 D32 的其余范围。

D32 交付入口已补 README、[使用指南](go-driver-usage.md)和 `examples/usage`：覆盖标准查询/事务、预处理、过程 OUT/INOUT、多结果、批量、两模式表内 BLOB、Oracle locator/OBJECT/ARRAY、Forward 游标；XA 沿用独立示例。示例只依赖公开根包及标准库，要求显式模式和可清理的测试 schema，当前仅编译，未执行联网步骤；不勾选示例运行、完整基准或整项交付完成。

D32 初始建库子项已补 Config.CreateDatabaseIfMissing / DSN createDatabaseIfMissing，默认关闭；仅 MySQL 新物理连接延后握手选库，认证后创建并选择，再运行 InitSQL。Oracle 显式拒绝，ChangeUser/CancelQuery 清除派生配置中的创建选项；失败不重试且不删除可能已创建的库，契约见架构 §6.4。TestCreateDatabaseSessions 专用 fixture 仅编译，未执行联网步骤；D32 整项继续进行中。

D32 网络读取预算子项已补 Config.ReadTimeout / DSN readTimeout，默认0关闭；两模式复用底层 net.Conn 的独立读取上限，兼容 TLS、三种包流、Rows 与批量流水线。context 较早期限生效，写后超时不重放，具体契约见架构 §12.5.1。TestReadTimeoutSessions 四目标入口仅编译，真实慢 SQL/代理缓冲与断连后的执行状态待联调；D32 整项继续进行中。

D32 TCP 配置已核对七个 Java 选项，使用现有 DialContext 与标准 net.Dialer/net.TCPConn 承载，保持单一公开 Config 和默认行为；不新增重复 TCP 配置或同名 DSN 开关。可编译示例及本地真实 TCP 对端的协议/取消/认证门禁/显式关闭测试已补，默认差异和边界见架构 §9.0.2。远端网络及数据库部署未验证，D32 整项继续进行中。

D32 TLS 配置已核对12个 Java 选项，以 TLSConfig/crypto/tls/crypto/x509 原生承载；证书文件/Java store 的解锁转换由应用管理，不新增驱动配置层。双向认证与文件示例、两模式/TLS1.2/1.3、回调/拒绝/取消和快照本地测试已补，默认和边界见架构 §17.2.1。真实数据库的证书部署/身份映射未验证，D32 整项继续进行中。

D32 本地性能子项已补 `BenchmarkDriver` 的两模式 CRUD、预处理、缓存、批量、LOB 和 fetch 基础16场景，另补 MySQL 批量改写和 Oracle 原生数组绑定，共18场景；普通单测共用结果/协议断言。范围、计时、复现命令及双版本短样本见[本地基准](go-driver-benchmarks.md)；对端开销计入统计，真实数据库性能与长期资源曲线仍待验证，D32 整项不勾选完成。

D32 离线资源子项增加 `TestDriverResourceStability`：复用18场景，固定8轮、每场景可配置迭代数；每次完整清理后检查原会话的租约/事务/响应/资源和缓存界限、池与物理关闭，再记录 GC 后的进程堆/goroutine。普通单测采用小负载，手动 `stability` CI 可独立执行两版本/两平台扩展运行。该串行 net.Pipe 证据不代替真实句柄、多连接/错误组合或数据库长期验收，细节见本地基准 §6。

D32 同会话资源子项增加 `TestDriverSessionStability`：两模式/三包流各持续使用一条物理连接，混合缓存预处理与类型复用、Rows 提前关闭、标准事务交替提交/回滚，以及可恢复 SQL 错误后的句柄释放/Ping；每轮直接检查原会话和池，不用重借连接的 reset 掩盖残留。默认每组合8轮，手动 `stability` 可扩展并采样进程资源，细节见本地基准 §7；完整并发/故障/高级能力及生产长期稳定性仍待验收。

D32 同池并发资源子项增加 `TestDriverConcurrentSessionStability`：两模式/三包流在同一连接池固定三条物理会话，执行上述混合负载，使用不同 ID/值并在每采样段首轮设置协议屏障，三条执行请求均到达后才返回首个响应。逐轮检查原会话，分段核对真实池计数；最终同时重借全部原连接、Ping、关闭池及物理会话。默认每连接8轮，手动 `stability` 另以每连接1024轮执行；范围与复现见本地基准 §8。不增加功能完成计数，不替代完整并发/故障/高级资源、真实平台或数据库验收。

D32 配置入口子项已核对固定 Java 的160个规范字段初值、10个历史别名、DBNAME/port/驱动选择标记及内置插件专用参数，补齐字段源码与初始化联动。TestDSNJava* 覆盖 Go 的配置阶段拒绝/脱敏、标准 sql.Open、单位/零值和标记字面内容；保持既有 Go API，不引入 Java 参数翻译层。具体默认差异见架构 §6.5 和配置映射；逐项运行行为、平台依赖及完整验收仍未收口，不勾选整项完成。

D32 凭据来源/RSA 文件配置已通过现有 Credentials 回调、Auth.RSAKey 和 Go 标准库解析承载，补齐应用示例、输入拒绝、同 Connector 轮换、两模式初始/切换/ChangeUser 解密及快照验证；不新增公开 API/配置层，完整边界见架构 §17.2.2。真实插件、公钥发布与来源服务待验证，D28/D32 总体状态不变。

D32 自动提交/只读四项配置已核对，沿用标准 sql.TxOptions.ReadOnly、固定 sql.Conn 和 Routing.Role；默认差异、Oracle 隔离级别及手动事务边界见架构 §12.3.1。补齐两模式错误传播、只读后读写恢复、初始化拒绝、池复用回滚顺序与失败角色不回退验证，以及外部标准 API 示例。没有新增生产配置、包或依赖；真实权限、快照隔离及 HA 仍待联调，D32 不提前计完成。

D32 结果列来源已通过现有 Execute.Snapshot 承载，补 ResultColumn 及 ResultInfo.Columns，保持标准 Rows 标签；两模式、GBK、二进制/合并执行、多结果、错误前缀、并发独立快照和累计预算均有本地测试。三项 Java 元数据选项按架构 §16.1.2 映射，不新增配置、包或依赖；TestMetadataCatalog 的实际来源检查仅编译，真实版本/代理仍待联调，D32 整项计数不变。

D32 SOCKS5 已通过既有 DialContext 与 x/net/proxy 的 ContextDialer 承载，应用示例和两模式/三包流/远端 DNS/代理认证/取消/路由/TLS 本地测试已补。无新增生产 API 或配置；x/net v0.58.0 仅示例/测试导入，模块选择同步提升 x/sys/x/text，完整契约见架构 §9.0.3。真实代理部署和性能未联调，D28/D32 整体状态保持。

D32 connectProxy 的 Go 适配使用 Session.SkipVersionProbe / skipVersionProbe，仅跳过现有的可选版本探测；必要初始化、模式与未知版本门禁保留，默认探测失败不自动降级。两模式/三包流/复用/ChangeUser/取消辅助连接及能力拒绝测试已补，四目标 TestVersionProbeSessions 集成源码仅编译，架构 §9.0.4 记录语义差异；D28/D32 整体状态不变。

D32 标准连接池子项以 sql.DB 原生 setter/context/Stats 和外部 slog 承载八项 Java 池选项，补齐一次性多连接就绪示例与两模式实际驱动生命周期验证；minPoolSize 无等价保底，staticGlobal 的服务端全局变量缓存不迁移为配置开关，默认差异和共享 Connector 的边界见架构 §5.1。没有新增生产 API、包、依赖或连接池；真实容量、空闲超时、监控及长期负载仍待验收，D28/D32 和 D30/D31 依赖状态保持。

D06/D32 增加 TestPoolCancelIsolation：两模式/三包流下，在同池另一条物理连接的预处理事务执行期间取消流式 Rows，验证错误与物理关闭只影响目标连接，原事务可提交、原会话可继续 Ping，并且无重拨、池泄漏或测试清理代为关闭。此项本地组合验证不替代全部并发/故障、真实数据库与成功 race 验收。

D32 初始化/执行选项按固定 Java 实现补证：usePipelineAuth 的认证后初始化分支在当前版本逐条 getResult，Go 沿用逐条确认/失败即关闭，不声明流水线优化；enableQueryTimeouts 由每次调用 context 承载；encloseParamInParentheses 对应当前版本的空格边界；serverAffinityOrder 的替换列表由 Endpoints/Weight/Strategy 承载，不能仅映射成 Ordered。新增 TestInitializationErrorBoundary、TestClientParameterBoundaries、TestStatementContextDeadline 及同名 Java DSN 拒绝测试，无生产配置/API/依赖变更。数组编码 fuzz 接入30秒 CI，原 FuzzStmt 选择器修复多目标歧义；真实 SQL/HA、平台与远程 CI 验收继续保留。

D32 逐项证据子项为160个规范选项补充 Go 配置、标准库、显式 API、固定语义、安全拒绝、应用责任、平台适配或不适用分类，并关联具体本地测试入口；不将 Example 的编译、旧参数拒绝或查询计划当作完整行为验收。TestDSNJavaCanonicalOptions 独立固定158个拒绝 query key 和2个同名 Go 入口；TestMySQLScalarCompatibility 补齐 TINYINT/YEAR/BIT 的9条执行/包流路径，保留 Go 整数/字节契约。TestMetadataCatalogAndRemarks 在两模式/两字符集/三包流中检查空 Catalog 无隐式 schema 限制、非空精确过滤请求和表/列备注保真、重复查询及同会话边界；固定字典行集不证明真实 SQL 求值。TestMetadataCatalog 的 DDL 备注和 Catalog 过滤集成源码仅编译；平台、远程 CI、成功 race 和真实目标仍待闭环，不勾选逐项完整验证或 D32 整项完成。

## 7. 首个开发批次

下一次进入代码开发从 D01 开始，紧接 D02、D03、D04、D05；每项代码可单独验证。D01 交付可测试配置而非全部接口空文件，D02 交付可故障注入的真实包流，D04 接入真实建连，D05 打通两模式标准文本执行。

这一批次完成后只代表 G1 完成。进度继续保留 G2–G6 的全部功能，不把“已连接/可查询”表述为全功能驱动已经完成。

## 8. 功能覆盖核对与持续维护

D32 提供[独立数据库验收工具](go-driver-test-tool.md)作为集中运行入口；目标和 fixture 由配置管理，程序自动准备/清理数据并记录逐场景报告。该工具补充现有协议回归与 integration 源码；本阶段只做离线验证，实际数据库报告仍为待验收证据。

D11/D32 计数验收入口补充：独立工具 counts-matched/counts-changed 固定计数策略，在自建数据上验证标准 Exec、Execute 摘要和单项 Sequential 批量的 INSERT、无变化/变化/未匹配 UPDATE 及 DELETE/空 DELETE；MySQL 另检查三种 upsert 分支，Oracle 不模拟 ChangedRows。TestRowsAffectedWorkflows 通过公开驱动和内存协议对端检查能力位、已知计数/溢出/错误及清理；不连接数据库，不替代真实 no-op/upsert 验收或已有多项批量/参数绑定用例。

D13/D32 参数描述验收入口补充：独立工具 metadata-parameters 使用自建表，检查位置参数、Oracle 重复命名参数及无参数 Query 的 StatementParameters；每次描述后验证种子原值，完整流程确认行数并清理。固定输出模式、身份和未知方向/描述的契约见架构 §16.1.1；不把服务端参数描述视为业务列定义。TestParameterMetadataWorkflow 替代公开 Metadata 结果以验证工具编排/断言/清理，底层 prepare/close 由既有驱动协议测试覆盖，真实服务端参数推断仍待联调。

D05/D11/D32 生成键验收入口补充：独立工具 generated-keys 仅在 MySQL 兼容模式自建 unsigned BIGINT 自增表，校验普通 LastInsertId、Execute 完整 uint64 摘要、超 int64 错误、后续 UPDATE 无旧 ID 与最终四行/清理；不假定 ID 连续。标准溢出结果无法取得原始完整 ID，不伪造精确比较；此部分由 Execute 覆盖。TestGeneratedKeyWorkflows 通过公开驱动和内存协议对端检查31个流程，含非法数据导致连接淘汰后的重新建连清理；真实自增范围/分配及 SQL 行为待联调，Oracle 返回列沿用 returning 场景。

D05/D22/D24/D32 错误语义补正：独立工具 CRUD、优化执行、普通批量和原生 array binding 对自建主键只接受模式对应的重复键错误码（MySQL 1062 / Oracle 1）；批量同时核对汇总错误、失败输入错误、完成数及 stop/continue 状态。TestDuplicateKeyWorkflows 和 TestBatchDuplicateAssertions 验证工具断言与失败清理；相关集成执行策略/批量断言同步补正，真实 SQL 和原生策略仍按既有目标待联调。

工具 metadata 比较自建表的字典内容，不能只用行数证明正确：核对 catalog/schema/表身份、ID/V 列名/位置/类型、INTEGER 精度和 scale、VARCHAR 长度、NULL、主键及两列 ColumnTypes，保持精确 schema 过滤与 LIKE 转义；错误/缺失/多余内容和读取/关闭错误必须失败并清理对象。90个离线流程只证明断言及编排，不替代服务端字典验收。

工具单行游标批次必须验证完整行数和最终错误：首行内容匹配后继续检查无多余行及 Rows.Err，保持关闭错误传播。前向/静态、刷新、敏感游标和过程 REF CURSOR 输出复用该断言，22个离线流程覆盖两种列形状、错误内容/行数/读取/关闭及失败清理，真实 fetch 语义仍待联调。

工具 routines/routines-client 的失败原子性必须确认自建例程实际触发了主动异常：要求结构化错误码 MySQL1644/Oracle20001 且 OUT 哨兵未变，语法/权限/传输等其他错误不能作为成功证据。68个离线流程验证完整例程编排、两模式/两场景选择、参数方向/初值、包装错误及逆序清理，integration 同步收紧，仅编译不联调；错误码依据见工具说明。 合并错误必须逐分支检查，例程、取消及重复键共享严格码匹配；预期异常伴随清理、超时或其他服务器错误不算通过。integration 同步复用规则，新增84项离线错误分类验证不连接数据库。

工具 mysql-scalars 提供 MySQL 专用标量集中验收：自动准备三组边界值和全 NULL 行，文本与参数化读取校验 TIME/YEAR、完整整数范围、BIT、空/含零二进制与定长补零，同时核对列类型/ScanType 及二进制字节长度；明确区分零/空/NULL，检查缺行/多行并在失败后关闭结果和清理表。离线回归只证明工具编排，真实类型/SQL 行为仍待联调。

工具 oracle-scalars 补齐 Oracle 专用标量的集中入口：自动建表、带值/NULL 行、文本与参数化读取，以及 ROWID 回绑定位。验证 NString、TimestampTZ/LTZ、IntervalYM/DS 的公开绑定、Unicode、原 TZ 偏移/纳秒、LTZ 等价时刻和 typed NULL；使用 server prepare、UTF-16 national charset，保留用户配置 location。该场景仅适用 Oracle 兼容模式，离线回归不代替服务端验收。

工具 scalars 的日期/时间断言比较完整墙上时间及纳秒，不以字符串前缀掩盖 Oracle DATE 时分秒变化或额外非零小数；等价尾零可接受，超九位小数不能经 Go 时间解析截断后算通过，也不能用单数字小时绕过小数上限。TestScalarsTemporalAssertions 提供两模式/两查询路径32个离线流程，并验证失败后的自建表清理；真实数据库往返仍待验收。

工具 complex 的 OBJECT OUT 同时核对字段值、属性顺序和 schema，不能只依据类型名/字段数量判定通过；集合首尾使用不同的 NUMBER/RAW 值，逐位置核对，避免重复种子掩盖乱序。TestComplexOutputAssertions/TestComplexCollectionAssertions 提供20个离线流程，同时验证事务回滚与依赖对象逆序清理；真实类型描述和数据库往返仍待验收。

原 integration 复杂类型用例同步上述内容和位置校验，OBJECT DATE 完整核对整秒，兼容驱动的六位零微秒表示；7项离线断言回归不连接数据库，原数据库流程仅编译。

| Java 模块/能力 | 实现任务 | 必需补充验证 |
| --- | --- | --- |
| M01 入口与统一 API | D01、D04、D05 | D32 外部调用示例/工具链 |
| M02 URL/Options/地址描述 | D01、D26、D27 | D32 option 逐项映射 |
| M03 传输/TLS/全部认证 | D02–D04、D23、D28 | 每阶段凭据门禁、各平台真实传输 |
| M04 模式与能力 | D03、D04、D23、D24 | 同池模式原子锁定、版本/能力组合 |
| M05 会话/本地事务 | D06、D09、D30 | 取消/结果未知/身份与代次 |
| M06 普通/两类预处理 | D05、D08、D10、D24 | 各路径等价、缓存/reset 与无重放 |
| M07 参数/批量/长数据 | D07、D14、D21、D22 | 部分成功/错误/OUT 及事务等价 |
| M08 结果/计数/生成键/游标 | D05、D11、D14、D19、D20 | 混合顺序、错误前缀、输出寿命 |
| M09 类型/字符集/时间 | D07、D08、D12、D18 | 全类型多路径往返、NULL/精度 |
| M10 LOB/复杂类型 | D15–D18、D20、D21 | 原事务可见性/锁/超大对象/失效 |
| M11 全部元数据 | D13、D18 | 实际字典、权限和参数元数据 |
| M12 HA/发现/拓扑 | D26、D27 | 新建连切换、角色与逻辑服务一致 |
| M13 池化/复用 | D06、D09、D15、D30 | 标准池压力、reset、句柄清理 |
| M14 XA | D31 | 两模式 1PC/2PC/flags/恢复 |
| M15 日志/诊断/OB20/FLT | D01、D23–D25 | nil 静默、脱敏、追踪隔离 |
| M16 构建/平台/性能 | D01、D28、D32 | 双工具链与各支持平台 |
| LOCAL INFILE | D29 | 受控 Reader、失败与事务 |
| JDBC scroll/updatable | D19 | static/sensitive/更新定位各自验收 |
| Java OSGi/JMX/序列化/旧类名 | D01、D25、D32 | Go module/DB.Stats/显式值契约映射；不引入 Java 运行时 |

进度文档保存当前任务状态、验证组合和下一步；代码/测试与正常文档保留变更依据，不另行保存走查报告。任务新增或拆分时同时更新本表、依赖及进度分母；不以删除未完成项提高完成率。
