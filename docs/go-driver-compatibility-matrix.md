# OceanBase Go 驱动兼容条件与验证矩阵

本表区分 Java 源码条件、Go 实现和真实环境验证。Java 基线为 obconnector-j 2.4.14，提交 `6adc311ba5db8b2f19f98f3e6e4734667fdfdda1`。源码条件不是服务端支持承诺；握手中的 MySQL 版本字符串不直接当作 OceanBase 版本。

## 1. 握手、认证与模式

| 能力 | 已确认依据 | Go 当前行为 | 验证状态 |
| --- | --- | --- | --- |
| Protocol v10 / 4.1 | [初始握手解析](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/ReadInitialHandShakePacket.java#L85)、[客户端响应](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/SendHandshakeResponsePacket.java#L120) | 严格解析 v10，要求 Protocol41/SecureConnection；仅声明已实现的协议路径 | 合成包流单测；真实版本待联调 |
| Oracle 兼容模式 | [状态位 4](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/util/constant/ServerStatus.java#L57)、[认证 OK 判断](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L823) | 按认证 OK 状态识别；显式模式不符关闭；ChangedRows 在 Oracle 模式拒绝；Connector 在成功初始化后原子锁定模式 | 合成模式正反例；真实版本待联调 |
| CLIENT_SUPPORT_ORACLE | [客户端声明](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L303) | 客户端始终声明，不要求 greeting 回显该位 | 合成握手；真实版本待联调 |
| native / caching SHA2 / SHA256 / clear | [认证编排](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L720) | 共享门禁；初始/切换/多阶段；clear 要求驱动验证的 TLS；SHA 无 TLS 仅用外部固定 RSA 公钥，禁自动取公钥 | 算法向量、对端解密、切换/拒绝路径通过；服务端插件组合未确认 |
| TLS | [SSL 能力位](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/OceanBaseCapabilityFlag.java#L67) | 显式 TLSConfig 启用，SSLRequest 后升级；校验证书与主机名，不降级；自定义 Dial 的安全状态不作为凭据发送依据 | 本地 TLS 服务端正向/错误证书/错误主机名；真实证书环境待联调 |

认证扩展使用 `AuthOptions.Plugins` 的每次认证工厂。`Security` 在 `Respond` 获得凭据前执行；每阶段重新判定，插件声明需 TLS 的阶段在未验证时不会调用 Respond。扩展属于调用方可信代码，需正确声明安全级别，并遵守 context；不暴露原始 Socket。Credentials 回调在每次新认证调用，NewConnector 不调用回调，事务中不刷新。

握手字符集已实现 utf8mb4、utf8/utf8mb3、latin1、GBK、GB18030、ASCII、binary 的协议编号；其他显式客户端字符集返回能力错误。认证阶段保留服务端版本原文，D05 已通过模式对应的 ob_version() 查询保留 OceanBase 版本原文；真实版本仍待联调，会话初始化已在 D04 接通。

标准文本与 COM_STMT_PREPARE/EXECUTE/CLOSE 已实现：沿用 [Java prepare 协议](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComStmtPrepare.java) 的元数据/EOF 边界；MySQL 参数数以服务端元数据为准，Oracle 命名映射后复核物理数量。二进制 NUMBER 协议类型 3 按固定 4 字节整数读取，246 保留精确十进制文本，不从 Java 类型名称猜测字节布局。基础两模式合成对端、EOF 两路径、混合多结果、错误和关闭已本地测试；真实目标尚未验证。D12 已实现标量扩展类型；D18 已提供复杂类型的本地协议实现，支持边界见 §9，客户端预处理已在 D10 实现。

D11 已实现标准 Rows 上的 Execute 摘要、逐项计数及有界最终化；完整 uint64 ID 保留文本。Snapshot 仅观察驱动状态，Close 在停止迭代后协调标准库取消错误。MySQL MatchedRows/ChangedRows 通过 CLIENT_FOUND_ROWS 控制；Oracle 拒绝 ChangedRows。合成包流通过，真实混合结果及无变化 UPDATE/upsert 待联调。

### 1.1 D32 连接声明首批

| 能力 | Go 当前行为 | 本地证据 / 真实验证边界 |
| --- | --- | --- |
| Interactive | Config.Interactive/DSN interactive，默认false；CLIENT_INTERACTIVE 是客户端请求位 | greeting 有/无该位、两模式精确握手通过；真实 interactive_timeout 效果待联调 |
| 应用连接属性 | Config.ConnectionAttributes 显式 map、快照、保留键、严格 Session.Charset、包含长度前缀的累计限额 | UTF-8/GBK/GB18030、长度阈值/收缩/扩展及 TLS 本地通过；服务端属性表可见性待联调 |
| ChangeUser 重发 | 同物理会话和原协商能力，沿用共同属性编码 | 两模式 × UTF-8/GBK × 普通/压缩/OB20 本地通过；真实服务与代理待联调 |
| 默认属性排除 | 不自动采集 PID/线程/主机，nil/空 map 只留内部协议声明 | 不提供会禁用协议能力声明的总开关；自定义属性不能代替 useProxyUser 认证适配 |

源码依据：固定版本 DefaultOptions、AbstractConnectProtocol.initializeClientCapabilities、SendHandshakeResponsePacket.writeConnectAttributes；对应选项及默认差异见[配置清单](go-driver-options-mapping.md)，Go 契约见架构 §6.1。D32 仍进行中，未缩减其余选项、平台与完整交付范围。

### 1.2 D32 排序规则

| 能力 | Go 当前行为 | 本地证据 / 待验证 |
| --- | --- | --- |
| MySQL connectionCollation | Session.Collation/DSN collation；SET NAMES Charset COLLATE Collation，空值保持默认 | 配置/严格标识符、UTF-8/GBK/utf8mb3、普通/压缩/OB20 的初始化恢复及 ChangeUser、Full reset 替换连接本地通过；真实排序/比较/参数往返待联调 |
| 失败与编码 | 服务端验证字符集配对/名称，不回退、不改驱动编码，失败关闭；列按实际 ID 解码 | 未知/不匹配的服务端错误与资源关闭本地覆盖；实际目标支持清单待确认 |
| Oracle 模式 | 显式 ModeOracle 在配置阶段拒绝；ModeAuto 实际 Oracle 在初始化前 ErrUnsupported | 两类拒绝本地覆盖；不复制 Java 的静默忽略，不改变默认 Oracle 流程 |

四目标 TestSessionCollation 已加入手动 CI，仅编译；MySQL 直连/代理需要真实正向验证，Oracle 对应本功能的负向契约，不据此宣称整体兼容。默认差异与生命周期见架构 §6.2。

### 1.3 D32 代理身份

| 能力 | Go 当前行为 | 本地证据 / 待验证 |
| --- | --- | --- |
| useProxyUser | Config.ProxyUser / Credentials.ProxyUser / DSN proxyUser；空值禁用；不解析括号用户名 | 静态/回调完整覆盖及清除、编码和预算、专用属性与 TLS 本地通过；真实授权/目标身份待联调 |
| 模式与能力 | 仅 Oracle；显式 MySQL 预检拒绝，Auto 实际 MySQL 认证后初始化前拒绝；缺少 CONNECT_ATTRS 拒绝 | 不回退普通身份、服务端错误保留本地通过；旧版本与代理支持待确认 |
| 生命周期 | ChangeUser 显式替换目标、空值清除、原池恢复原配置；CancelQuery 比较认证用户和代理目标 | 普通/压缩/OB20、UTF-8/GBK、目标不匹配不发 KILL 本地覆盖；真实 KILL 权限/代理路径待联调 |

TestProxyUserSessions 已接手动 CI，仅编译。Oracle 两目标使用 OB_ORACLE / OBPROXY_ORACLE 前缀的 PROXY_DSN、PROXY_AUTH_USER、PROXY_SESSION_USER 三项 fixture；DSN 必须显式含 proxyUser，两个预期用户名使用实际服务端返回值且应不同。测试读取 SYS_CONTEXT 的 CURRENT_USER/PROXY_USER/SESSION_USER，验证登录、ChangeUser 重发/清除及新池恢复；启用 OB_TEST_REQUIRE 时缺失 fixture 为失败。MySQL 两目标仅验证配置拒绝，不据此声明完整兼容。契约见架构 §6.3。

### 1.4 D32 初始数据库创建

| 能力 | Go 当前行为 | 本地证据 / 待验证 |
| --- | --- | --- |
| MySQL 显式创建 | Config.CreateDatabaseIfMissing/DSN createDatabaseIfMissing，默认 false；认证时不选库，认证后创建并用 COM_INIT_DB 选择，再运行 InitSQL | 握手位与名称、UTF-8/GBK、反引号转义、普通/压缩/OB20、失败关闭及总预算本地覆盖；真实 CREATE 权限/默认属性待联调 |
| 生命周期隔离 | 新物理连接可重复执行 IF NOT EXISTS；ChangeUser/取消辅助连接清除派生配置中的创建选项，不改原 Connector | 两项误建库回归有先失败再通过证据；创建或选库失败不重试、不自动删除可能已创建的库 |
| Oracle/空目标 | 显式 Oracle 或空 Database 在配置阶段拒绝；Auto 实际 Oracle 在初始化 SQL 前拒绝 | 负向契约本地覆盖，不以拒绝代表 MySQL 正向验收 |

TestCreateDatabaseSessions 已加入手动 CI，MySQL 直连/代理需要专用 CREATE/DROP DATABASE fixture，验证缺失库、既有库、InitSQL 和物理替换；Oracle 只验证配置拒绝。本阶段仅编译，没有执行真实 DDL。完整契约与 Java 对照见架构 §6.4。

### 1.5 D32 网络读取超时

| 能力 | Go 当前行为 | 本地证据 / 待验证 |
| --- | --- | --- |
| 两模式读取预算 | Config.ReadTimeout / DSN readTimeout；默认0关闭；每次底层 Read 与 context 取较早期限，写方向保留原预算 | 握手、初始化、TLS、普通/压缩/OB20、响应等待和 Rows 本地覆盖；真实服务延迟/代理缓冲待联调 |
| 生命周期/流水线 | 每次读取重新计时，空闲不消耗下次预算；并发 deadline 刷新不延长阻塞读；超时淘汰，不重放 | context 优先、旧 deadline、逐项/流水线批量未知结果，以及网络错误分类本地覆盖；并发刷新回归先失败再通过 |

TestReadTimeoutSessions 四目标要求显式只读慢 SQL fixture，验证独立网络超时和物理替换；仅编译，未执行。它不保证服务端查询已停止，完整命令时限仍使用调用 context。契约见架构 §12.5.1。

### 1.6 D32 TCP 原生配置

七项 Java 配置（tcpNoDelay、tcpAbortiveClose、localSocketAddress、tcpKeepAlive、tcpRcvBuf、tcpSndBuf、socketFactory）对应现有 DialContext 与标准 net.Dialer/net.TCPConn，不添加重复驱动配置；默认值和错误/资源归属见架构 §9.0.2。可编译 ExampleConfig_tcpOptions 及 TestTCPDialContext* 覆盖本地原生 TCP、源地址、选中入口、协议收发、认证门禁、取消、关闭与 Java DSN 参数拒绝。显式 abortive close 不保证 QUIT 送达或服务端 SQL 回滚；内置默认关闭。

这里只证明应用可通过同一驱动入口使用标准 TCP 能力；远端 OS 调优、真实探测/缓冲数值、网络中间件与数据库部署未验收。不改变 Shared Memory/VC 等 D28 缺口，也不以 API 映射计作整个 D32 完成。

### 1.7 D32 TLS 原生配置

12项 Java TLS 配置已映射到 Config.TLSConfig、crypto/tls 与 crypto/x509，完整边界见架构 §17.2.1；不增加 store/密码/工厂字段。文件加载示例通过本地临时证书验证；两模式、TLS1.2/1.3、静态/动态客户端证书、标准验证后的额外策略、配置快照和取消均有本地 TLS 对端证据，版本/套件/回调拒绝不回退明文。Java 同名 DSN、历史别名及弱化验证开关严格拒绝。

JKS/PKCS12/加密私钥由应用或部署侧转换，不声明 Go 标准加载器可直接读取；替换整个 TLS 引擎不属于 tls.Config 回调能力。真实数据库/代理的双向身份映射、平台系统信任源和证书部署未验收；TLS1.3 套件也不能通过 CipherSuites 限制。D32 整项及 D28 平台缺口保持进行中。

## 2. 后续高级协议条件

| 能力 | 固定 Java 条件 | Go 所属任务与状态 |
| --- | --- | --- |
| INFORMATION_SCHEMA.PARAMETERS | [OceanBase 版本 >= 4.1](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1774) | D13 已实现；使用 ob_version 门禁，未知/旧版本拒绝；真实字典待验证 |
| PrepareExecute | [Oracle 模式 + useOraclePrepareExecute + OceanBase >= 2.2.76](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778) | D14 已实现 OUT/INOUT/RETURNING 必需路径，含带字段标志与尾随响应；D24 已实现显式可选通用策略，见 §15；真实版本待联调 |
| 带 offset 的 fetch | [Oracle 模式 + 服务端预处理 + useCursorOffset + OceanBase >= 2.2.5](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1783) | D19 已实现 Oracle 已知 >=2.2.5 的 offset/尾 OK；旧/未知版本向前 fetch，真实版本待联调 |
| LOB locator V2 / array binding / OB20 / reset | LOB 声明 1<<29，协商 CONNECT_ATTRS 后声明 locator-v2 掩码 1；D23 新元数据掩码 2 另行协商；其他能力分别取证，不外推版本支持 | D16 协商/解析已实现，目标版本未验证；D09 Full 淘汰旧会话；原生 reset、原生 array binding 未实现；D23 OB20 已本地实现，见 §14；D22 当前按 §13 回退逐项 |

## 3. 真实目标

| 目标 | 模式 / 接入 | OceanBase / OBProxy 精确版本 | 当前状态 |
| --- | --- | --- | --- |
| P-MD | MySQL 兼容模式直连 | 未确认 / 不适用 | 暂不联调，未验证 |
| P-OD | Oracle 兼容模式直连 | 未确认 / 不适用 | 暂不联调，未验证 |
| P-MP | MySQL 兼容模式经 OBProxy | 未确认 / 未确认 | 暂不联调，未验证 |
| P-OP | Oracle 兼容模式经 OBProxy | 未确认 / 未确认 | 暂不联调，未验证 |

后续用真实环境执行前先固定目标版本、认证插件、传输和已声明正向/拒绝路径。完整能力验收及证据规则以 [开发进度](go-driver-development-progress.md) V01–V14 为准。本地模拟不能替代真实兼容验收。

## 4. 标量、字符集与时间（D12）

| 能力 | Go 行为 | 验证状态 |
| --- | --- | --- |
| 精确 NUMBER / DECIMAL | Number 保留文本；Oracle 协议 3 按四字节整数，206/246 按精确十进制文本；输入 246，不转 float64 | 两模式边界及三种执行路径本地通过；真实精度待联调 |
| NCHAR/NVARCHAR2、RAW、ROWID | NString/Raw/RowID 保留输入身份；NCHAR 按 NCharset 解码，RAW 返回复制的字节，ROWID 返回文本 | 原生字段体、NULL/空值及参数身份本地通过；真实服务器空值语义待联调 |
| Oracle 时间/间隔 | 类型 200/201/202/204/205 按 Java 原生布局解码，两类行协议保留纳秒/偏移/region；区分 LTZ 会话墙上时间与瞬间 | 原生夹具、DST、零值时间、公元 1 年、非法字段及端到端本地通过 |
| parseTime / Location | 默认文本；启用后按显式会话时区解析无时区日期/时间戳；零日期和 DST 歧义拒绝；TIME/YEAR 不改语义 | 初始化顺序、DSN、列 ScanType 及正反例本地通过；服务端命名时区需目标支持 |
| UTF8MB4 / UTF8MB3 | UTF8MB3 拒绝补充平面字符；两者均严格验证 UTF-8 | SQL/参数/列名/结果及 fuzz 通过 |
| latin1 / ASCII / binary | latin1 按 MySQL Windows-1252，未定义字节拒绝；ASCII 拒绝非 ASCII；二进制字段保持字节 | 正反例和 fuzz 通过 |
| GBK / GB18030 | 使用 x/text 固定映射，拒绝不可表示/非法编码，允许可解码的旧字节别名 | 已知向量、SQL/参数/列名/结果及 fuzz 通过 |
| UTF16 | Big Endian、无 BOM；严格验证代理对，仅 NCHAR/结果使用，SQL 客户端字符集明确拒绝 | BMP/补充字符及无效代理对本地通过 |
| 未知 collation / GB18030-2022 | 明确拒绝；不把 GB18030-2022 冒充为旧 GB18030 表 | 拒绝测试通过；新版本映射须经目标条件与向量补充后才能声明支持 |

现有 collations 按固定映射解析；未取证的新编号不猜测字符集。`TestAllScalarTypes` 已建立四目标、文本/服务端/客户端三路径的真实测试源码与显式 CI 入口，仅完成编译；本阶段没有连接数据库。本地协议对端验证驱动转换和字节布局，不能证明目标服务端 SQL/时区表或字符集组合兼容。

D13 已补齐标准 RowsColumnType 的长度/精度接口及统一 Metadata 查询：MySQL information_schema 与 Oracle ALL_* 分开实现，Oracle catalog 不存在时返回空行集，MySQL 无 UDT/同义词字典时返回 ErrUnsupported；权限错误保留服务端代码。StatementParameters 总是服务端 prepare 后关闭，保留逻辑/物理映射和原始协议方向码。公开与内部字典执行共用计划，内部只在完整响应边界保持原 gate 与物理连接。以上为本地对端验证，真实字典字段及 OBProxy 兼容未验证。

## 5. 标量 OUT、INOUT、RETURNING（D14）

| 能力 | Go 行为 | 验证状态 |
| --- | --- | --- |
| 标量 OUT / INOUT | sql.Out 或 Out；nil/不支持目的发送前拒绝；可空值、完整 unsigned、扩展标量保留输入身份 | 两模式协议对端及预处理复用通过；真实例程待联调 |
| Oracle 输出与 RETURNING | 实际 ob_version >=2.2.76；COM_STMT_PREPARE_EXECUTE；接受 RETURNING 标志 1/3 和 PL OUT；强制读取尾随 OK/ERR；临时 statement 关闭后发布 | 编码、EOF/弃用 EOF、错误与尾部边界本地通过；SQL/OBProxy 行为未验证 |
| MySQL 输出 | 同一会话随机私有变量；初始化、主命令、输出 SELECT、清空；辅助结果不进入摘要；清理失败淘汰连接 | 主命令/SELECT/清理故障、类型尺寸、连接复用本地通过；真实 SQL 待联调 |
| 输出发布 | 普通结果保持流式；最终响应/清理结束并确认未取消后写回；内置转换先暂存，自定义 Scanner 可能部分副作用由 OutError.Partial 报告 | 溢出/NULL/重复冲突/行数异常/关闭失败/最终化期间取消回归通过 |

`TestScalarCall` 与 `TestReturning` 已加入手动集成入口，当前仅编译。真实四目标仍全部未验证；LOB、REF CURSOR/句柄输出分别随 D16/D20 实现。

## 6. 固定资源生命周期（D15）

已实现包内资源记录、物理连接/会话/事务代次、父 statement 租约和标准 Session 私有关闭分派；没有公开 ResourceManager，也不保留 Raw 取得的 driverConn。Close 不结束调用方事务；代次失效返回 ErrResource，标准库可先返回 Conn/Tx 已结束错误。事务与 reset 清理受 CleanupTimeout 约束；分派前取消允许再次关闭。合成对端验证身份、租约、未发布记录、取消、超时与池复用。D16 已接入具体临时 LOB 释放；游标协议归 D19/D20，当前没有真实服务器资源验收结论。
## 7. 原生 LOB（D16）

| 能力 | Go 行为 | 验证状态 |
| --- | --- | --- |
| BLOB/CLOB/NCLOB | Oracle 模式的 OpenBlob/OpenClob，只接受固定 Conn/Tx；NULL 返回 nil，空值保持有效句柄 | locator V1/V2、内联/外联、NULL/空、失效与基数失败清理本地通过 |
| 原生分块操作 | 实际 ob_version >=2.2.76；同会话 DBMS_LOB；字节随机读写、字符读写/长度、缩短和临时释放 | 短读、NUMBER 数量、前缀写入失败、更新 locator、CLOB/NCLOB 偏移本地通过；实际 SQL/版本待联调 |
| 普通查询 / 标量 OUT | 返回受限物化内容；完成主响应及尾部后再插入内部命令；剩余响应累计有界 | QueryContext / Rows.NextResultSet / Oracle 隐藏 OUT、多结果、事务 context 回归本地通过；真实原事务可见性待联调 |
| 大对象 / Reader/Writer | 每块限制与普通 16 MiB 物化限制分开；适配器使用显式 context | 超过 16 MiB 的显式 Reader、打开 context 结束后的独立读取本地通过 |
| 字符边界 | Unicode 码点 API；UTF16 偏移转换；改变已有字符代理对宽度的覆盖在写前 ErrUnsupported | BMP/补充字符、等宽覆盖/截断及非等宽拒绝本地通过；NCLOB 真实字符单位仍待验证 |
| 资源释放 | V2 临时 locator 释放服务端对象；V1 用 ISTEMPORARY；持久对象释放本地记录；无隐式事务结束 | 幂等 Close、基数错误下多个临时对象清理、事务/reset 代次基础已覆盖；实际资源/锁行为待联调 |
| MySQL 模式 | 原生 OpenBlob/OpenClob 返回 ErrUnsupported；OpenTableBlob 表内分块已实现（D17） | 原生入口拒绝已验证；不把 BLOB 内容当 locator |

`TestNativeLOB` 源码已覆盖原事务未提交读写、普通物化、NCLOB、取消、临时释放、事务结束与 Full reset 后复用；本阶段仅编译。没有数据库、Linux、远程 CI 或成功的 race 运行证据。

## 8. 表内 BLOB（D17）

| 能力 | Go 行为 | 验证状态 |
| --- | --- | --- |
| 显式目标 | 标准 Conn/Tx，schema 解析后固定，基表/非生成 BLOB 列/完整主键；复制 Key 和字节值，自定义 Valuer 只求值一次 | 复合主键、特殊标识符、NULL/缺行/空值、非法目标及参数本地通过 |
| MySQL 读取 | OCTET_LENGTH/SUBSTRING；每块有界，句柄内容可超过物化上限；Conn 不承诺跨块快照 | database/sql 对端跨块、重新按键取长度、取消/失效本地通过；服务端 SQL 待联调 |
| MySQL 修改 | 调用方可写 Tx，ENGINE=InnoDB；每次先 FOR UPDATE 确认行/非 NULL/长度；无稀疏扩展，ChangedRows=0 可成功 | 追加/覆盖/缩短/空写、只读/未知引擎拒绝、丢行/NULL/锁错误本地通过；真实锁/隔离待联调 |
| 容量与失败 | 四种 BLOB 容量在首条 UPDATE 前预检；有 warning 的 UPDATE 返回 ErrLOB；失败块不计完成前缀，调用方回滚 | 容量边界/警告 OK/断线/取消回归通过；真实非严格模式与 max_allowed_packet 仍待验证 |
| Oracle 后端 | 相同目标查询复用原生 Blob；可写 Tx 打开时 FOR UPDATE，只读不加锁 | 原生捕获、CHAR/DATE/NUMBER 主键和事务锁 SQL 本地通过；实际 locator/权限待联调 |

`TestTableBlob` 四目标源码及手动 CI 入口已加入，本阶段仅编译。未连接数据库，未把本地协议测试计为真实 SQL/锁/事务验收。

## 9. OBJECT/ARRAY 与描述（D18）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 值与扫描 | 根包 Object/Array/Attribute，format=1 JSON；精确 NUMBER、base64、墙钟时间、嵌套/NULL/空集合；Scan 失败保留原值 | 无损/边界/深度及 fuzz 本地验证；API 详见架构 §11.2.2 |
| 原生参数 | COMPLEX160；字典字段计划；对象与匿名集合元素头分别编码 | database/sql 服务端预处理、具名/匿名数组和复杂 OUT 对端通过；真实目标未验证 |
| 描述与缓存 | 同物理会话/单命令，模式/schema/name/协议版本隔离；实体优先，private/PUBLIC synonym；失配仅刷新描述和解析缓冲字节 | Query/Next 完整响应后内部查询、版本变化、同义词和缓存预算本地通过；DDL/权限/版本联调未完成 |
| 嵌套 LOB / 结果清理 | 完整剩余响应后物化，共享根值预算；普通/后续复杂行集与隐藏 OUT 同路径；关闭时最终解码继承清理 context | 嵌套 BLOB/CLOB、总量限制、OUT 列数不匹配、尾错、Close/Exec 排空和清理超时回归通过 |
| 当前拒绝边界 | MySQL 原生复杂输入、文本复杂结果、客户端复杂插值；非 NULL TIMESTAMP/LOB 字段输入；NCHAR/NVARCHAR2/TIMESTAMP/LOB 内置集合元素头 | 明确报错，非法元素头发送前拒绝，空/全 NULL 集合同样拒绝；待服务端写协议证据补齐 |

协议依据为同一 Java 基线的 [对象参数](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBStructParameter.java)、[集合参数](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/parameters/OBArrayParameter.java) 和 [二进制行解析](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/BinaryRowProtocol.java)。Java 文本复杂读取没有可用表示；Go 明确拒绝该路径。当前实现未证明所有目标版本或所有字段类型兼容。

`TestComplexTypes` 只做 integration 标签编译，未连接数据库。四目标真实对象/集合/嵌套 LOB/事务/复杂 OUT、同义词权限及实际版本仍待联调；不计入验收完成。


## 10. 游标、滚动与可更新结果（D19）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 向前批次 | Conn/Tx 固定会话；COM_STMT_EXECUTE flag1，COM_STMT_FETCH；完整批次返回标准 sql.Rows；有活动批次即 ErrCursorBusy | 两模式原生包、提前关闭、独立 context、空游标、失败失效、本地通过；实际服务器待联调 |
| Static | 原生 fetch 建立有界快照，绝对/相对定位；0 为首行前，负/溢出拒绝；修改不更改原快照 | 滚动/预算/边界测试及位置 fuzz；实际大结果待验证 |
| Sensitive | Oracle 已知 >=2.2.76，命令161 + scrollable mode8 + 绝对 offset fetch；不复用 static 行；其他组合明确拒绝 | 重复定位返回变化、尾 OK 位置与旧版本拒绝本地通过；跨会话敏感性/隔离待联调 |
| 可更新与刷新 | 可写 Tx、单基表/完整投影 PK；MySQL InnoDB；安全标识符/原生绑定 DML；Update/Delete 先同事务 FOR UPDATE；Refresh 按 PK 读取基表投影 | 两模式 SQL/绑定、no-op0、缺行/警告、NULL 主键、prepare/锁后代次复核通过；真实锁/权限/并发待联调 |
| 批次值与寿命 | 完整响应后原 gate 解码复杂值/物化 LOB；批次受 MaxMessageBytes/MaxLOBBytes，Close/事务/reset 释放父 statement 租约 | Oracle LOB fetch、EOF 两路径、旧版9字节及新版19字节、事务/reset 生命周期本地通过 |

具体公开契约和限制见架构 §15。当前没有仅 ROWID 的可更新后端；完整 PK 不可靠时拒绝。Refresh 随 Updatable 开放，返回当前基表行，不重跑原过滤条件。修改 warning/未知结果由调用方回滚；缺行能安全关闭游标并保留事务。`TestCursorModes`、`TestCursorUpdates` 覆盖直连/代理两模式的源码已加入 CI 手动入口，本阶段仅编译，未连接数据库。


## 11. REF CURSOR 与流式输出（D20）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 显式输出 | Execute + CursorOut / Out 到 **Blob/**Clob；Oracle 已知 >=2.2.76；初始 nil、非 INOUT | 正向、NULL、多个/混合资源及普通入口/旧版/模式拒绝本地通过；真实目标未验证 |
| 协议及租约 | CURSOR163 固定 LE4 ID；独立九字节 FETCH/COM_STMT_CLOSE；多个资源共同保留父 statement | 对端逐命令核验；最后资源释放父 statement、Execution.Close 后读取/新 context、事务失效通过 |
| 发布与失败 | 完整响应和所有内置/Scanner 转换成功后统一发布资源；发布前复核 context/代次；失败清理 | 尾错、损坏响应、Scanner 错误、转换中取消及无输出泄漏本地通过 |
| 普通子 Rows | 二进制 REF CURSOR 列在完整父响应后按共享消息预算缓存；标准父子 Rows 寿命，16 层/循环门禁 | 原会话/事务、嵌套/取消/元数据总预算本地通过；static 子快照跨批计量和回看独立性通过 |

源码依据为同一 Java 基线的 [类型编号](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/ColumnType.java)、[REF CURSOR](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/RefCursor.java)、[二进制读取](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/read/resultset/rowprotocol/BinaryRowProtocol.java) 和 [释放命令](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractQueryProtocol.java)。`TestProcedureResources` 与 CI 手动入口已加入，本阶段只编译、不连接数据库；实际过程语法、临时 LOB、版本/权限及 OBProxy 行为仍待验证。


## 12. 长参数与分段上传（D21）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 一次性输入 | *LongData；借用 Reader、精确 Length/-1、MaxBytes；同对象一次上传，具名多位置逐块分发 | 空/长度/边界/超限、无整流缓冲、错误 n+err 与无 rewind 对端测试 |
| 两模式协议 | MySQL24 无逐包响应；Oracle162 逐包 OK、二进制 hex/文本会话编码、STRING254；execute 省略流值体 | 原生包与源码核验；Oracle 已知 >=2.2.76，旧版/未知拒绝；四目标未验证 |
| 文本与入口 | UTF-8 增量严格转换；普通预处理/显式游标、Oracle OUT 和 MySQL OUT 同会话上传 | GB18030/UTF-8 跨块、OUT 发布、游标及命名重复本地覆盖；实际 NCLOB 转换待联调 |
| 失败与取消 | Reader 不由驱动关闭；上传失败/取消淘汰连接；上传后 execute 前取消同样清理；无自动重放 | 部分写、服务端段错误/损坏、Reader 异常及取消回归；阻塞 Reader 需调用方协作返回 |

详见架构 §13.5。普通 PrepareClient 拒绝流输入；OUT 延用其必要的原生协议。`TestLongParameters` 与四目标手动 CI 入口已加入，本阶段仅编译，未连接数据库；max_allowed_packet、空流存储、实际 LOB/事务和 OBProxy 行为仍待联调。

## 13. 批量执行与策略（D22）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 公开接口 | ExecBatch；DB 全批次固定 Conn，Conn/Tx 单次私有分派，不创建/结束事务 | 两模式、自动提交部分成功及原事务回滚对端验证；真实目标未验证 |
| 状态和计数 | 输入顺序，NotExecuted/Succeeded/Failed/Unknown、Started、已知/未知行数；失败不承诺零副作用 | 中间失败/后项、uint64 计数溢出、确认成功后清理错误本地通过 |
| multi-send | Auto/MultiSend + ContinueOnError + server prepare + 简单 DML/标量；独立命令并发写/按序读，按行/物理参数位/总字节分块 | 提前发包、双向背压、16MiB 续包、错误和事务测试通过；服务器/代理缓冲行为待联调 |
| 保守回退 | 默认遇错停止、OUT、一次性长参数、复杂值及自定义 Valuer 采用逐项；复用现有协议和输出规则 | 流一次消费、标量 OUT、全部六种策略偏好本地通过；句柄输出仍限 Execute |
| 改写/bulk/array binding | 偏好已接入，实际均回退逐项；Java bulk 不支持说明、array 特殊错误计数及改写副作用不足以证明等价性 | 回退已验证，三类原生优化未启用；服务端版本/等价性证据待取 |
| 不确定性与取消 | 不重放；保留确认项，只将可能已发出且未确认的项标 Unknown；淘汰损坏连接并等待写协程；保留 SQL/context/清理错误链 | 单次拨号、部分发送、损坏/额外结果、Tx/操作取消及最终响应后取消本地通过 |

具体接口、默认值及 Java 源码证据见架构 §13.4。`TestBatchExecution` 与手动 CI 四目标入口已加入，本阶段仅编译，不连接数据库，不计入验收。

## 14. 包流、校验与新元数据（D23）

| 能力 | Go 当前行为 | 验证状态 |
| --- | --- | --- |
| 协商和策略 | ProtocolOptions 六项独立三态（含 D25 FLT）；标准认证 OK 后固定包流；required 缺失/off 被启用直接拒绝 | 两模式三包流、非法组合及能力回显本地通过；真实版本未验证 |
| 压缩 | CLIENT_COMPRESS，7 字节头，zlib/raw，跨帧重组与内层逐消息预算 | 聚合小消息、跨头/16MiB 续包、膨胀/尾随数据/序号错误本地通过 |
| OB20 | 7+24 字节头、4 字节尾，身份/序号/标志/CRC 校验，新 TLV 与旧 extra 按边界跳过 | 独立 CRC 向量、损坏/长度、ID 回绕、部分写/取消/未读拒绝复用本地通过 |
| 两类 checksum | Checksum 是 OB20 CRC；ProxyChecksum 是 Java useObChecksum 的能力位 3/6，请求和确认独立处理 | 组合策略本地验证；代理到后端的实际校验没有本地证明 |
| 新元数据 | 复用列布局，Oracle 国字符长度直接使用 Length，RAW/ROWID 使用 Precision，LOB 长度未知 | 解码标记与长度测试本地通过；真实列描述未验证 |
| 批量与错误 | framed multi-send 发送前回退逐项；不因协议错误重试业务，错误保留实际网络字节数 | 两模式/两类 framing 批量预处理及清理、取消与损坏测试通过 |

完整策略/位号/源码见架构 §9.1。`TestPacketModes` 四目标集成源码及 CI 已加入，仅完成编译；标准/auto/压缩登记为四目标正向，OB20/OB20 CRC/代理 checksum 登记为两代理目标正向，新元数据登记 Oracle 两目标正向；其他目标允许明确能力拒绝并记录。执行前必须固定支持这些条件的服务端与代理版本，不能以全为拒绝宣告兼容。当前所有真实目标均未验证，FLT 业务字段归 D25。

## 15. D24 执行策略条件与验证边界

| 能力 | 发送前条件 | 当前证据 |
| --- | --- | --- |
| 通用 PrepareExecute | Oracle + Execution.PrepareExecute + 实际 ob_version >= 2.2.76；未知/旧版走标准路径 | 合并查询、显式 statement DML、重复 ID/类型、错误不重放及 Blob/Clob 捕获本地通过；真实服务端待联调 |
| 标准 SQL checksum | Oracle + Execution.StatementChecksum + 实际 ob_version >= 4.1.2 | 边界门禁、独立 CRC 向量及标准包字段本地通过；批量/游标共享门禁 |
| 合并 SQL checksum | 已选择的 Oracle 合并路径 + Execution.StatementChecksum | 尾部字段本地验证；不改 iteration，不等于 OB20 CRC |
| 类型头复用 | Execution.ReuseParameterTypes + 同 statement 上次完整成功 + 类型及描述相同 | NULL、unsigned、复杂描述变化及 SQL 失败后重发本地验证；只保留类型元数据 |
| 有界缓存 | StatementCacheSize > 0、SQL UTF-8 字节数 <= StatementCacheSQLLimit、可缓存普通 SQL | 两模式 LRU、错误驱逐、SQL 长度、会话 SQL 失效和 Full reset 物理淘汰本地通过；显式 statement/过程/连接资源按架构 §10.1.1 管理 |

`TestExecuteStrategies` 对 mysql/oracle/proxy-mysql/proxy-oracle 各定义普通及优化配置，对照返回值、NULL/类型变化、OUT、重复键错误、计数与资源关闭。Oracle 优化目标需固定 >= 4.1.2 的实际服务端以覆盖两项门禁；MySQL 保持标准命令并覆盖缓存及类型头复用。当前仅编译集成源码，没有运行数据库联调，也没有服务端吞吐结论。

## 16. D25 诊断与 Full Link Trace

| 能力 | 本地实现与证据 | 真实验收边界 |
| --- | --- | --- |
| 注入日志/元数据回调 | nil 静默、Debug/Warn、脱敏、回调/handler 解锁重入、请求/消息上限与丢弃计数 | 不采集原始 SQL/包；调用方管理 handler 并发与池重入 |
| FLT 协商和 extra | off/auto/required、OB20/FLT 能力组合、新 TLV 与旧 varchar、CRC/16MiB 分片/畸形字段 | 目标版本和代理配置待确认；直连 off、代理 required 登记正向 |
| 请求隔离与控制 | 每命令清空 app、16字节 ID、服务端采样/打印/慢请求/ShowTrace，query 时间清空；无 logger 仍运行 | TestFullLinkTrace 仅编译；需对照服务端 app 清空及 trace/span ID，不以客户端事件宣称服务端追踪成功 |
| 边界和归因 | Rows 包含消费时间；每 fetch/长数据片段/LOB SQL/批量项独立命令，multi-send 合并真实收发统计；畸形 OK 记录失败 | 建连总时长为本地事件；最后一条完成 span 可能无后续命令上传；不额外发 SQL 或重放 |

详细 API、限制和延后上传策略见架构 §18.3.1。数据库/OBProxy、Linux、远程 CI 与成功 race 仍未验证，不计入验收。

## 17. D26 静态选址、角色与建连故障切换

| 能力 | 本地实现与证据 | 真实验收边界 |
| --- | --- | --- |
| 地址/策略 | DNS/IPv4/IPv6/端口解析、组优先和组内 ordered/random/roundrobin/weighted affinity；Connector 快照与并发健康隔离 | 真实入口同租户、逻辑角色与网络条件待验证；TNS/OCP/拓扑刷新归 D27 |
| 预算/冷却 | 单次覆盖拨号到初始化，所有尝试与固定退避/冷却等待共用总预算；次数、取消、过期和独立 Connector 测试 | 默认值与约束见架构 §17.1.1；调用方 Dial/凭据回调需遵守 context |
| 拒绝与安全 | 认证/模式/畸形握手/初始化不重试，TLS 使用选中主机；TLS1.2/1.3 服务端证书拒绝 alert 不当传输错误 | 仅可识别收到的协议拒绝；未知断线按传输故障处理；真实证书/代理条件待联调 |
| 活动会话 | RoleReadOnly 拒绝写事务，固定角色不降级；业务丢响应不重放，事务不换 Socket；可选 idle PING 失败淘汰 | 普通写权限由服务端负责，不自动路由 SELECT；真实角色标签由调用方保证 |

TestConnectionFailover 四目标源码及手动 CI 已加入；各 profile 的 DSN addresses 至少包含两个同租户、同兼容模式入口。测试在首次拨号注入失败，再实际连接备用入口，验证活动只读事务不重拨、关闭后新会话能选择已恢复入口；它不模拟真实服务器宕机或证明业务未知结果的数据库侧状态。本阶段只编译，不连接数据库；真实 HA、Linux、远程 CI 和 race 不计通过。

## 18. D27 发现源与 Connector 生命周期

| 适配 | 本地实现与证据 | 真实验收边界 |
| --- | --- | --- |
| DESCRIPTION/TNS | 有界嵌套/别名/注释解析、文件前后类型/尺寸校验；角色/分组/权重与来源策略提示映射；无隐式环境搜索/磁盘缓存 | 真实 TNS 文件变更、权限及文件系统阻塞待验证；源文件由调用方管理 |
| OCP | 显式 URL + 注入 HTTP Client；Java 的 data.NET_SERVICE_NAME/DESCRIPTION 包装和直接描述；重复键/大小/深度/失败状态/超时/重定向拒绝 | 实际 OCP 版本/响应变体、TLS/认证/服务权限与变化待联调；不把 Java 内存对象样例当 HTTP 接口 |
| 共享刷新 | 惰性串行、服务身份与角色门禁、最后成功快照/硬过期、新连接代次、同址健康继承及幂等关闭 | 来源标签不能证明真实租户/主从角色；静态/动态源均需服务端身份取证 |
| replication/Aurora | 显式角色快照和 TopologySource 同链路适配；两个标准 sql.DB；不向 OceanBase 猜测外部产品 SQL | 自有复制控制面和 Aurora 实际发现回调、权限、角色变化逐服务待验证；无 OceanBase 原生 Aurora 支持承诺 |
| 关闭/旧入口 | sql.DB.Close 停止刷新；driver.Open 私有 Connector 清理；已获得的连接不随发现 context 取消 | 自定义源必须响应 context；超时只限制等待，不能强杀用户回调/文件系统 syscall |

TestDiscovery 已为四目标提供 TNS/OCP 源码及手动 CI 入口，本阶段仅编译、不连接数据库。每个基础 DSN 环境变量的前缀（移除 `_DSN`）需配置 `_TNS_DSN`、`_OCP_DSN`，以及 `_DISCOVERY_IDENTITY_SQL`、`_DISCOVERY_TENANT`、`_DISCOVERY_ROLE`。身份 SQL 由部署方按实际版本/权限提供，返回租户及 readwrite/readonly 两列；测试只为未标记的整个逻辑服务显式赋予该角色，并在同会话查询验证。该查询必须真实读取服务端身份，常量返回不算验收证据。受控刷新后的角色/地址变化、外部复制/Aurora 发现仍需独立真实服务取证，不能用初次连接或静态回调代替。Linux、远程 CI 与 race 仍不计通过。

## 19. D28 逐项认证与传输

本表先登记支持条件，精确 OceanBase/OBProxy 版本及相应插件部署仍未确认；目标服务缺失只影响验收，不删除开发范围。下列本地协议证据不能证明服务端安装或支持 MariaDB 同名插件。

| 能力 | 本地实现/目标条件 | 实际验收状态 |
| --- | --- | --- |
| old password | Go 可运行平台；必须显式 AllowOldPassword；8 字节应答/旧式切换，字符集按字节，空密码发空响应 | 算法向量、初始/切换和默认拒绝；服务端插件/两模式/代理仍待验证 |
| Ed25519 | Go 可运行平台；client_ed25519 严格32字节挑战、64字节签名；纯 Go edwards25519 依赖 | 互操作向量、初始/切换/尾零本地测试；真实插件目标待验证 |
| PAM dialog | 支持该插件的目标服务；所有回答仅驱动验证 TLS 后发送；回调按轮次独立，可并发且响应取消 | 本地真实 TLS、两轮对话、默认单密码和错误/取消门禁；PAM 服务/平台待验证 |
| 插件生命周期/编码 | AuthFactory 每使用独立实例，可选 io.Closer；内置密码严格 PasswordCharset，默认 UTF-8 | 成功/拒绝/切换/错误清理和字符集本地测试；ChangeUser 公开链路归 D30 |
| Windows GSSAPI/SSPI | 当前 Windows 身份；Kerberos 双向认证，NTLM/Negotiate 强制 verified TLS；SPN 可固定，原生上下文按次关闭 | 本机原生 NTLM 初始与完整 TLS 对端已测；域 Kerberos、Negotiate 和真实服务组合待验证；系统票据查询无法由 context 强制中断 |
| Unix GSSAPI | -tags=gssapi；Linux amd64/arm64/386、macOS amd64/arm64；系统 MIT/Heimdal/Apple GSSAPI，Kerberos/default cache/mutual；purego 动态调用，不要求构建时 cgo，运行需动态加载器/C 库 | 提供方、状态/资源清理及 Windows amd64 的 C ABI 用例已实现/运行；Linux/macOS 系统库及真实票据交换待运行；未启用标签/其余目标明确不支持 |
| TCP | 标准 net.Dialer、多地址/发现和既有 TLS/模式/命令流程 | Windows 本地测试；真实两模式/代理和 Linux 待验证 |
| Unix socket | Config.Network=unix，Go/OS 提供 AF_UNIX；固定 Addr，TLS 时显式 ServerName | 本机 Windows socket 协议收发/关闭、超时和门禁已测；Linux/真实服务 socket 待验证 |
| Named Pipe | Config.Network=pipe；Windows go-winio overlapped I/O；暂时不存在按配置重试，权限错误不重试；非 Windows 内置拨号明确不支持 | 本机 Windows 管道协议收发/关闭、重试和门禁；远端同步 CreateFile 无严格 context 中断保证，见架构 §9.0.1；远程权限及真实服务待验证 |
| Shared Memory/VC | 按原生事件/映射协议及 VC ABI 独立适配 | 待开发/平台运行，整项 D28 未完成 |

## 20. D29 LOCAL INFILE

`Import` 和一次性 `LocalInfile` 已本地实现；仅本次命令授权精确名称 Reader 或 Open 回调，普通命令不允许上传，协议能力位不代表本地文件权限。请求名按会话字符集解码，内容原样发送；复用标准/压缩/OB20、同会话事务和既有清理策略。大小/超时/Reader 所有权及失败语义见架构 §17.3.1。

| 目标/能力 | 当前证据 | 待验证条件 |
| --- | --- | --- |
| MySQL 兼容模式直连/代理 | 本地请求/拒绝/失败/取消/事务协议；完整三种包流；集成源码已提供默认 LOAD DATA LOCAL SQL | 实际版本/能力位、租户导入权限、local_infile 服务配置、入库计数/内容和部分失败事务效果 |
| Oracle 兼容模式直连/代理 | 本地两模式共用实现和字节流；集成入口已提供 | 目标实际支持的 SQL 及 LOCAL 请求协议；对应 profile 的 LOCAL_IMPORT_SQL 模板须包含 {table} 并请求 input；真实支持未确认 |
| 安全与资源 | 无隐式本地文件读取，单次授权/名称拒绝/来源限额/关闭和禁止重放有本地回归 | 同步 Open/Read/Close 必须配合 context 返回；数据库部分效果不能由本地对端证明 |

四目标手动 CI 运行 `TestLocalImport`，对应环境名为 `OB_MYSQL_LOCAL_IMPORT_SQL`、`OB_ORACLE_LOCAL_IMPORT_SQL`、`OBPROXY_MYSQL_LOCAL_IMPORT_SQL`、`OBPROXY_ORACLE_LOCAL_IMPORT_SQL`。Oracle 缺失 SQL 时强制验收失败，普通跳过不计通过。本阶段未连接数据库，整项验收仍未完成。

## 21. D30 会话辅助与 warning（首批）

| 能力 | 本地实现 | 待验证/未完成 |
| --- | --- | --- |
| schema/catalog | 固定 sql.Conn；Oracle current schema、MySQL current database/COM_INIT_DB；Oracle catalog 明确不支持；名称严格编码/转义 | 实际服务版本、权限、代理及显式预处理绑定语义待联调 |
| 客户端信息/会话变量 | 三个 ClientInfo 字段映射会话用户变量；系统变量标识符校验、值转义、保留 SQL NULL；编码/时区 setter 保留给 Config.Session | 两模式与代理的变量 SQL、可写性和值域待联调 |
| 修改后的池隔离 | 活动事务/autocommit 关闭时拒绝；发送修改后不可归还原池，原 pinned 连接可继续使用；所有 setter 发送前清理自动预处理/类型描述缓存 | 物理断连及新借用者无状态泄漏的真实目标验证尚未执行 |
| warning 数量/详情/清除 | OK/EOF 的 WarningSummary、Execute 每项 ResultInfo.Warnings；MySQL ReadWarnings 读取当前 SHOW WARNINGS 的级别/码/消息，ClearWarnings 本地抑制直到下一普通 SQL；不依赖日志 | Oracle 只供数量，详情返回 ErrUnsupported（对齐 Java 通用提示）；SHOW WARNINGS 无 SQLSTATE，不推测。当前诊断区不等于历史快照；非零 warning 真实目标待验证 |
| ChangeUser | 同连接 COM_CHANGE_USER；重新认证/基线/同模式，身份代次失效 server/client statement 与固定资源，失败关闭，成功关闭时退出原池 | 两模式、插件切换/门禁、取消、真实 TLS 及压缩/OB20 本地测试；四目标 TestChangeUserSessions 源码只编译，真实身份/权限/代理待验证 |
| 显式取消 | QueryTarget/CancelTarget/CancelQuery；单次取消首个 SQL/首个 pipeline 分块，原入口私有辅助通道、同用户/租户及已验证 TLS，完成/重置/切身份失效，在途确认协调 | 两模式、TLS、固定入口、权限/未知结果及本地竞态测试；真实取消码、代理行为和远端 ID 复用窗口待验证，不提供协议没有的原子保证 |

TestSessionOperations 和 TestChangeUserSessions 四目标源码已加入手动 CI，本阶段只编译、不连接数据库。D30 仍为进行中；实现总数保持28/32，验收0/32。API 和状态生命周期见架构 §16.3.1。

ChangeUser 集成输入：OB_MYSQL_CHANGE_USER_DSN、OB_ORACLE_CHANGE_USER_DSN、OBPROXY_MYSQL_CHANGE_USER_DSN、OBPROXY_ORACLE_CHANGE_USER_DSN；必须为对应原服务上的另一个身份。强制验收时缺失失败，普通跳过不计通过。行为边界见架构 §16.3.2。

TestWarningDetails 四目标源码和手动 CI 已补，需 OB_MYSQL_WARNING_SQL、OB_ORACLE_WARNING_SQL、OBPROXY_MYSQL_WARNING_SQL、OBPROXY_ORACLE_WARNING_SQL 提供成功且产生非零警告的 SQL（MySQL 须保留实际详情）；强制验收缺失失败，普通跳过不计通过。本阶段仅编译，不执行 SQL。详情的当前诊断区、限额/失败连接及本地清除契约见架构 §16.3.3。

TestCancelQuerySessions 四目标源码/手动 CI 已补，每个 DSN 前缀对应 CANCEL_SQL 和 CANCEL_CODE，例如 OB_MYSQL_CANCEL_SQL/OB_MYSQL_CANCEL_CODE；其余前缀为 OB_ORACLE、OBPROXY_MYSQL、OBPROXY_ORACLE。SQL 必须为足够长的只读阻塞查询，CODE 必须为实际目标已确认的非零中断码；拒绝把语法/权限等任意错误当作取消成功。源码仅编译未连接数据库；本地测试不能验证真实 ID 分配、代理路由和权限。本子项已实现，但 D28 依赖未闭环，D30 继续进行，实现28/32、验收0/32不变，契约见架构 §16.3.4。

## 22. D31 XA 本地契约

| 能力 | Go 当前行为 | 证据与边界 |
| --- | --- | --- |
| XID/flags | 复制二进制 XID、按阶段及模式校验 flags，Oracle 扩展值显式公开 | 原生字节/负 FormatID/空 BranchID/非法长度及阶段本地通过；真实版本待验证 |
| 两模式执行 | MySQL XA SQL；Oracle DBMS_XA 原生 XID/OUT，保留 prepare 票和 XA 错误 | 1PC/2PC、rollback-only、挂起恢复、原生输出和返回码本地通过；Oracle 原生调用要求 >=2.2.76 |
| 恢复与隔离 | 实际四列/集合解析，有界全列表，专用会话退出原池，不本地回滚 prepared | NULL/长度/额外结果/上限、prepared Close、事务/身份/会话 guard 本地通过 |
| 故障 | 写出后结果不明淘汰、不重放；commit 额外 ErrCommitUnknown | 部分写、丢确认及原生输出后尾部错误本地通过；不包含真实故障恢复证据 |
| 明确未支持 | Forget/RM timeout 不伪造成功，协调日志与恢复决策归应用 | Java 本身未实现的服务器契约；context 不是 RM timeout |

TestXATransactions、TestXARecoveryAfterDisconnect 四目标源码已加入手动 CI，包含 DML、1PC/2PC/rollback、join/resume/suspend、准备后断开并用新连接恢复及提交可见性；本阶段只编译。标准/压缩/OB20 两模式本地覆盖不代替服务端 SQL、权限、版本、代理和 RM 恢复语义。D31 保持进行中，因 D30/D28 平台依赖未闭环，实现28/32、验收0/32；细节见架构 §16.2.1。

## 23. D32 本地性能夹具

两模式 CRUD、每次/复用预处理、缓存命中、逐项/multi-send 批量、16KiB BLOB 和32行 fetch 共16场景已有本地协议对端及普通单测正确性检查。Go1.25.0/1.26.0 Windows amd64 短样本与复现方式见[本地基准](go-driver-benchmarks.md)。进程内对端、断言和调度开销计入统计；没有真实 SQL/网络/服务端工作量，也不覆盖全部传输/优化组合。真实数据库性能、LOB 远端流式和长期资源稳定性仍未验证，不构成任何组合的性能验收，D32 保持进行中。
