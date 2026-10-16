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

标准文本与 COM_STMT_PREPARE/EXECUTE/CLOSE 已实现：沿用 [Java prepare 协议](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/com/send/ComStmtPrepare.java) 的元数据/EOF 边界；MySQL 参数数以服务端元数据为准，Oracle 命名映射后复核物理数量。二进制 NUMBER 协议类型 3 按固定 4 字节整数读取，246 保留精确十进制文本，不从 Java 类型名称猜测字节布局。基础两模式合成对端、EOF 两路径、混合多结果、错误和关闭已本地测试；真实目标尚未验证。D12 已实现标量扩展类型；复杂类型继续属于 D18，客户端预处理已在 D10 实现。

D11 已实现标准 Rows 上的 Execute 摘要、逐项计数及有界最终化；完整 uint64 ID 保留文本。Snapshot 仅观察驱动状态，Close 在停止迭代后协调标准库取消错误。MySQL MatchedRows/ChangedRows 通过 CLIENT_FOUND_ROWS 控制；Oracle 拒绝 ChangedRows。合成包流通过，真实混合结果及无变化 UPDATE/upsert 待联调。

## 2. 后续高级协议条件

| 能力 | 固定 Java 条件 | Go 所属任务与状态 |
| --- | --- | --- |
| INFORMATION_SCHEMA.PARAMETERS | [OceanBase 版本 >= 4.1](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1774) | D13；未实现，目标环境待确认 |
| PrepareExecute | [Oracle 模式 + useOraclePrepareExecute + OceanBase >= 2.2.76](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778) | D14 最小 RETURNING 路径、D24 通用策略；未实现 |
| 带 offset 的 fetch | [Oracle 模式 + 服务端预处理 + useCursorOffset + OceanBase >= 2.2.5](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1783) | D19；未实现 |
| LOB locator V2 / array binding / OB20 / reset | 需分别补协商位、连接变量、精确版本及执行条件；不从上述两个版本门槛外推 | D09 Full 已实现淘汰旧物理会话；原生 reset 及 D16/D22/D23 待取证/未实现 |

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
