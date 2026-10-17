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

## 2. 后续高级协议条件

| 能力 | 固定 Java 条件 | Go 所属任务与状态 |
| --- | --- | --- |
| INFORMATION_SCHEMA.PARAMETERS | [OceanBase 版本 >= 4.1](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1774) | D13 已实现；使用 ob_version 门禁，未知/旧版本拒绝；真实字典待验证 |
| PrepareExecute | [Oracle 模式 + useOraclePrepareExecute + OceanBase >= 2.2.76](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1778) | D14 已实现 OUT/INOUT/RETURNING 必需路径，含带字段标志与尾随响应；D24 通用策略未实现；真实版本待联调 |
| 带 offset 的 fetch | [Oracle 模式 + 服务端预处理 + useCursorOffset + OceanBase >= 2.2.5](https://github.com/oceanbase/obconnector-j/blob/6adc311ba5db8b2f19f98f3e6e4734667fdfdda1/src/main/java/com/oceanbase/jdbc/internal/protocol/AbstractConnectProtocol.java#L1783) | D19 已实现 Oracle 已知 >=2.2.5 的 offset/尾 OK；旧/未知版本向前 fetch，真实版本待联调 |
| LOB locator V2 / array binding / OB20 / reset | LOB 声明 1<<29，协商 CONNECT_ATTRS 后只声明 V2 属性位 1；其他能力分别取证，不外推版本支持 | D16 协商/解析已实现，目标版本未验证；D09 Full 淘汰旧会话；原生 reset 及 D22/D23 待实现 |

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
