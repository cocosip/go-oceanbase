# Go 驱动能力矩阵

更新日期：2026-11-02。本文描述 **go-oceanbase 自身的公开能力与支持边界**；依据 OceanBase 官方文档、Connector/J 实现、.NET 驱动已有功能矩阵和本项目源码核对。功能不足与验收不足分别见[功能缺失矩阵](go-driver-completeness.md)。

## 阅读口径

- **有实现**：存在公开入口及对应实现，不代表所有版本、部署和选项组合均已验收。
- **受限**：仅实现表中明确的子集；输入、结果、OUT、文本、binary、流和 locator 不互相替代。
- **未实现**：未提供该项完整契约；可能有通用 SQL 或字符串/字节替代路径。
- **—**：该模式或 Go 接口不适用；**排除**表示不作为当前驱动待开发项。

服务端支持一种 SQL 类型，不等于驱动支持它的专用编码；Java/.NET 存在接口，也不要求 Go 复制类名和配置项。SQL 引擎、索引、分区、向量搜索算法和运维管理能力由数据库提供，不单独计为驱动功能。

### 验证证据

| 标记 | 含义与限制 |
| --- | --- |
| S | 本次源码核对，入口见各行；没有执行该行实库用例 |
| L | 仓库存在本地单测、协议 peer 或故障用例；不是本次重跑结果，也不是服务端兼容性证明 |
| H20 | [历史实库报告](release-validation-2026-10-20.md)：OceanBase 4.4.2.2 双模式直连；仅限报告列明场景 |
| H24 | [浮点专项报告](binary-double-live-validation-2026-10-24.md)：双模式直连 float64 参数及 Oracle 数组绑定子集 |
| V | 尚缺对应真实部署、类型形状或故障验收；可以与 H20/H24 同时存在 |

H20 的独立工具结果为 MySQL **35 pass / 0 fail / 2 skip / 8 不适用**、Oracle **38 pass / 0 fail / 2 skip / 5 不适用**，每模式选择 45 场景。不能由此推导下表每行均通过。报告中的旧测试目录已经调整，当前集成测试分布在各 `internal/*` 包及其子目录；复验使用当前入口，不直接照搬历史路径。

## 连接、会话与安全

| ID / 能力 | MySQL 模式 | Oracle 模式 | Go 入口、条件与边界 | 证据 |
| --- | --- | --- | --- | --- |
| C01 连接、Ping、关闭、模式识别 | 有实现 | 有实现 | [Open / NewConnector](../connector.go)、[建连协调](../internal/connect/connector.go)；认证后识别模式，显式模式不符拒绝；同一连接不切换租户模式 | S/L/H20 |
| C02 服务端版本与能力协商 | 有实现 | 有实现 | [配置](../config/config.go)、[协议](../internal/connect/protocol.go)；使用 `ob_version()`，不拿 MySQL 握手兼容版本代替；未知版本关闭依赖版本的扩展，无服务端版本覆盖开关 | S/L/H20/V |
| C03 DSN、配置快照和校验 | 有实现 | 有实现 | [ParseDSN](../config/dsn.go)；`oceanbase://`，键名不区分大小写，重复/未知键拒绝，值不改写；不直接接受 JDBC URL 或全部 Java 参数 | S/L |
| C04 TCP、自定义拨号、SOCKS5 | 有实现 | 有实现 | [传输](../internal/transport/transport.go)、[传输配置](../config/transport.go)；TCP 调优通过 `DialContext`/标准库，SOCKS5 不替代 TLS | S/L/H20（普通 TCP）/V |
| C05 Unix socket、Named Pipe、Shared Memory、VC | 受限 | 受限 | [传输目录](../internal/transport)、[OBProxy VC](../obproxy/dialer.go)；有 OS/build tag/运行库限制；共享内存分配等待不能保证硬取消；VC 是原生代理嵌入入口，另需真实 Linux 运行验收 | S/L/V |
| C06 TLS、主机名验证、mTLS | 有实现 | 有实现 | `Config.TLSConfig` 使用 Go `crypto/tls`/`x509`；拒绝未验证 TLS 授权敏感认证；JKS/PKCS12 转换由应用/部署负责 | S/L/V；H20 仅真实端点拒绝/未开启边界 |
| C07 native password | 有实现 | 有实现 | [认证流程](../internal/connect/authentication.go)、[native](../internal/auth/native.go)；不因 Oracle SQL 模式改用 Oracle Net/OCI | S/L/H20 |
| C08 SHA256、caching-SHA2、old、clear、Ed25519、PAM、GSSAPI | 受限 | 受限 | [认证配置](../config/authentication.go)、[认证扩展](../internal/authentication)；old 显式开启，clear/PAM 要求已验证 TLS，非 TLS RSA 只用预先固定公钥；GSSAPI 依 OS/tag/票据 | S/L/V；客户端实现不证明 OceanBase 安装相应插件 |
| C09 凭据回调、认证扩展、代理身份 | 有实现 | 受限 | `Credentials`/`Auth.Plugins`；[ProxyUser 配置](../config/proxy_user_test.go)仅 Oracle，MySQL 拒绝；连接器固定逻辑 User/ProxyUser，允许密码变化，不在活动事务中自动换身份 | S/L/V |
| C10 标准连接池与资源隔离 | 有实现 | 有实现 | 使用 `database/sql` 容量/等待/空闲/寿命/统计；[reset](../internal/control/reset.go)及资源代际隔离；不是另建私有连接池，也没有标准库未提供的最小连接数保证 | S/L/H20（有限并发）/V（长稳） |
| C11 初始化、属性、超时 | 有实现 | 有实现 | `Session.InitSQL/Variables`、`ConnectionAttributes`、`Interactive`、`ConnectTimeout/ReadTimeout`；调用总预算使用 context；读超时逐次计算，不保证服务器已停止 SQL | S/L/V |
| C12 database/schema、ChangeUser、会话变量 | 有实现 | 受限 | [会话 API](../session.go)、[会话控制](../internal/control/session.go)；MySQL 切 database、Oracle 切 current schema；Oracle catalog 不适用；更改身份/状态影响池复用和资源代际 | S/L/H20（指定会话场景）/V |
| C13 自动创建缺失数据库 | 受限 | — | `CreateDatabaseIfMissing` 显式启用，仅新建 MySQL 物理连接；先创建/选择再 InitSQL；不是迁移或 schema 管理器 | S/L/H20（指定创建场景）/V |
| C14 多地址、角色、负载分配、连接故障转移 | 有实现 | 有实现 | [路由](../internal/routing/selector.go)；只在建连阶段切候选，角色来自配置；TLS/auth/已发 SQL 不作为业务重试理由，不自动识别 SELECT 并分流 | S/L/V |
| C15 DESCRIPTION/TNS、OCP、拓扑刷新 | 有实现 | 有实现 | [TNS](../internal/routing/tns.go)、[OCP](../internal/routing/ocp.go)、[拓扑](../internal/routing/topology.go)；TNS 是地址描述解析，不是 Oracle Net；同租户/主备身份需部署证明 | S/L/V |
| C16 活动会话透明迁移、自动事务/SQL 重放 | 排除 | 排除 | 发送后断线分类并淘汰连接，应用显式判断和重建；不以重新执行掩盖未知结果 | S/L |
| C17 warning 计数、详情与清除 | 有实现 | 受限 | [会话 API](../session.go)、[warning 控制](../internal/control/warnings.go)；两模式记录 OK/EOF 计数，MySQL SHOW WARNINGS 详情；Oracle 详情明确拒绝；ClearWarnings 本地抑制，不是历史诊断档案 | S/L/H20（指定 warning 场景）/V |

## 执行、参数、结果与批处理

| ID / 能力 | MySQL 模式 | Oracle 模式 | Go 入口、条件与边界 | 证据 |
| --- | --- | --- | --- | --- |
| Q01 text SQL、CRUD、DDL、错误 | 有实现 | 有实现 | 标准 `ExecContext/QueryContext`、[执行引擎](../internal/execution/execute.go)；SQL 语义由相应兼容模式决定，保留服务端错误 | S/L/H20 |
| Q02 位置/命名参数、NULL、重复占位 | 受限 | 有实现 | [绑定](../internal/parameters)、[词法](../internal/sqltext)；MySQL 普通 SQL 用位置参数，命名参数只覆盖可解析简单 CALL；Oracle 维护逻辑名与物理槽映射 | S/L/H20/V |
| Q03 server prepare、复用、binary 结果 | 有实现 | 有实现 | [statement](../internal/statement/statement.go)；`PrepareContext` 和默认参数化路径；不是所有服务端语句都可 prepare | S/L/H20 |
| Q04 client prepare 与自动回退 | 受限 | 受限 | [客户端执行](../internal/execution/client.go)、[字面量](../internal/valuecodec/literal.go)；显式 `PrepareClient` 已有；**没有普通 server-prepare 遇 1295 自动切 client 的完整契约** | S/L/H20（显式路径）/V |
| Q05 文本多语句、多结果 | 受限 | 受限 | `AllowMultiStatements`、`Rows.NextResultSet`、[Execute](../execution.go)；多语句显式启用；参数字面量只覆盖已有类型，不支持 Object/Array/LongData 任意组合 | S/L/H20/V |
| Q06 过程、函数、OUT/INOUT、RETURNING | 有实现 | 受限 | [Out / CursorOut](../call.go)、[调用实现](../internal/call)；MySQL 用同会话私有变量；Oracle 原生输出要求真实版本门槛；结果排空/清理后发布输出 | S/L/H20/V |
| Q07 过程/语句参数元数据 | 有实现 | 受限 | [Metadata](../metadata.go)、[目录计划](../internal/catalog/metadata_sql_oracle.go)；语句元数据实际 prepare/close；不提供 .NET 式 DeriveParameters 自动创建参数对象，PL-only RECORD 不等于命名 SQL OBJECT | S/L/H20（目录子集）/V |
| Q08 Rows、Scan、ColumnTypes、计数与自增 ID | 有实现 | 有实现 | [Rows](../internal/execution/rows.go)、[Execute](../execution.go)；扩展结果保留列标签/原始列与表身份；完整 uint64 ID 保留文本；聚合结果不猜逐项 ID；Oracle 不接受 ChangedRows | S/L/H20 |
| Q09 流式 Rows 与大字段 | 受限 | 受限 | 行迭代流式；普通字段/复杂值受消息及物化限额约束；大对象使用 [LOB](../lob.go)/[TableBlob](../internal/lob/table.go)，不能据此宣称任意列无限流式 | S/L/H20/V |
| Q10 显式 cursor、分页、滚动、可更新结果 | 受限 | 受限 | [OpenCursor](../cursor.go)、[游标实现](../internal/cursor)；静态快照有界；敏感游标仅指定 Oracle 版本/单表形状，以 ROWID 重查；更新要求可写事务、完整 PK 等 | S/L/H20（含 Oracle 敏感刷新）/V |
| Q11 REF CURSOR OUT / 子结果 | — | 受限 | [资源输出](../internal/call)、[物化](../internal/materialize/decoder.go)；OUT 与 binary 子 cursor 有实现；文本 cursor 拒绝，选定 `SELECT CURSOR` 实库形状受阻 | S/L/H20（过程 OUT）/V |
| Q12 REF CURSOR IN/INOUT | — | 未实现 | 没有公开输入 writer 契约；不能从 OUT 或 scalar INOUT 推导支持 | S/L（拒绝） |
| Q13 SchemaOnly/KeyInfo、DataAdapter 等生态接口 | — | — | 不复制 ADO.NET CommandBehavior/Disconnected DataSet；使用 Metadata、显式 SQL 和 Go `database/sql` | 接口差异，不计驱动缺失 |
| Q14 顺序批处理、逐项结果 | 有实现 | 有实现 | [ExecBatch](../batch.go)；每项区分未执行/成功/失败/未知，不自动开启或提交事务 | S/L/H20/V |
| Q15 MySQL 多语句 rewrite / multi-VALUES | 受限 | — | [策略选择](../internal/batch/strategies.go)、[multi-VALUES](../internal/batch/values.go)；`BatchRewrite` 和 `BatchMultiValues` 是不同策略；前者还要求多语句许可，后者保留聚合 chunk 结果 | S/L/H20（报告子集）/V |
| Q16 Oracle DML 数组绑定 | — | 受限 | `BatchArrayBinding`；[门槛](../internal/batch/strategies.go)：真实版本 ≥4.2.1、可写事务、server prepare、至少两项、ContinueOnError、限定标量等；不是 collection 参数 | S/L/H20/H24/V |
| Q17 自动语句缓存、参数类型头复用 | 有实现 | 有实现 | `Execution.StatementCacheSize/ReuseParameterTypes`；缓存默认关闭；同物理会话/类型描述/成功状态内复用，身份、session SQL/reset 等触发失效 | S/L/H20/V |
| Q18 LOCAL INFILE / 导入 | 受限 | 受限 | [Import](../local_infile.go)、[导入校验](../internal/execution/import.go)；单次应用流授权，不按服务器文件名读取本地文件；两模式均要求 autocommit、无本地事务且未进入 XA 专用状态 | S/L/H20/V |
| Q19 长参数分片上传 | 有实现 | 受限 | [LongData](../long_data.go)、[上传](../internal/statement/upload.go)；MySQL long-data / Oracle piece-data，Oracle 依版本；单次 Reader、有界且不自动回放；client prepare 不支持流 | S/L/H20/V |
| Q20 multi-send / pipelining | 受限 | 受限 | [批量 native 选择](../internal/batch/native.go)、[pipeline](../internal/batch/pipeline.go)；server prepare、标量 DML、ContinueOnError 等门槛；framed 传输/Shared Memory 或不合资格形状走顺序；不是 SELECT/复杂值/流的通用 pipeline | S/L/H20（指定子集）/V |
| Q21 MariaDB bulk 协议 | 排除 | — | 枚举 `BatchBulk` 不表示已有 bulk writer；当前不合资格策略走顺序；不能把 Java MariaDB 继承选项当 OceanBase 必需协议 | S/L |
| Q22 context、读超时、显式 CancelQuery | 有实现 | 有实现 | [取消控制](../internal/control/cancel.go)；命令预算、单次目标及辅助连接 KILL；权限/代理路由影响实际中断；取消不保证无数据库效果 | S/L/H20（报告子集）/V |
| Q23 活动操作互斥、资源所有权 | 有实现 | 有实现 | [session](../internal/session)、[lifetime](../internal/lifetime)；LOB/cursor/输出绑定固定 Conn/Tx；不承诺同一连接多活动结果并行，跨连接并发交给池 | S/L/H20（有限并发）/V |
| Q24 JDBC escape 转换与 SQL 词法 | 受限 | 受限 | [RewriteJDBCEscapes](../escape.go)、[sqltext](../internal/sqltext)；识别注释/引用/占位并按模式转换已有 escape 子集；不是任意 SQL 方言转换器 | S/L/V |

## 类型、编码与复杂值

| ID / 能力 | MySQL 模式 | Oracle 模式 | Go 映射与具体边界 | 证据 |
| --- | --- | --- | --- | --- |
| T01 整数、unsigned、布尔 | 有实现 | 有实现 | [scalar codec](../internal/valuecodec/binary.go)；结果保持整数/精确文本；无符号超 int64 不经 float64；TINYINT/BIT/YEAR 不照搬 Java 可变返回类型 | S/L/H20/V |
| T02 DECIMAL / NUMBER / FLOAT 精度 | 有实现 | 有实现 | [Number](../types/number.go) 保留精确文本，Scan/元数据分开；不是用 float64 覆盖任意 NUMBER | S/L/H20/V |
| T03 FLOAT/DOUBLE、BINARY_FLOAT/BINARY_DOUBLE | 有实现 | 有实现 | float64 输入 DOUBLE wire 与握手能力配合；[专项](binary-double-live-validation-2026-10-24.md)验证存储/回读；旧服务器协商组合另验 | S/L/H20/H24/V |
| T04 字符串、二进制、RAW、BLOB/TEXT 内容 | 有实现 | 有实现 | string/[]byte/[Raw](../types/scalar.go)，严格编码；Oracle 空串和目标容量由服务端语义决定；内容与 locator 分开 | S/L/H20/V |
| T05 ENUM/SET/GEOMETRY | 受限 | — | [结果类型映射](../internal/valuecodec/text.go)：ENUM/SET 文本、GEOMETRY 原始字节；无几何模型/专用输入身份和跨 SRID 验收 | S/L（通用解码）/V |
| T06 JSON | 受限 | 受限 | MySQL type245 结果校验 UTF-8 并返回 string，输入可交给服务端从字符串转换；没有专用 Json 参数类型或 DOM；Oracle 不承诺原生 JSON codec | S/L/H20（报告子集）/V |
| T07 BIT / YEAR | 受限 | — | BIT 返回原始字节、YEAR 整数；[公开兼容测试](../internal/valuecodec/public_scalar_compatibility_test.go)覆盖三执行路径/三种流；没有独立 BIT/YEAR 输入类型标签 | S/L/V |
| T08 DATE/DATETIME/TIME、零日期 | 受限 | 受限 | [时间编码](../internal/valuecodec/temporal.go)、[time.Time 输入](../internal/valuecodec/binary.go)；MySQL 精度上限为微秒、Oracle DATE 含时间；ParseTime 不能表示的值拒绝；零日期依配置与服务端 SQL_MODE | S/L/H20/V |
| T09 TIMESTAMP、TZ/LTZ | 有实现 | 受限 | [精确时间值](../types/temporal.go)；Oracle 原生时间/时区保留纳秒、偏移/地区信息；LTZ 使用会话墙钟规则；named region 实际目标支持另验 | S/L/H20/V |
| T10 INTERVAL YM/DS | — | 有实现 | [IntervalYM/DS](../types/temporal.go)，原生与 client literal 路径；区分精度、符号和 NULL | S/L/H20/V |
| T11 NCHAR/NVARCHAR2 | — | 受限 | [NString](../types/scalar.go)、`Session.NCharset`，scalar 输入/结果和限定复杂成员；不能覆盖任意 national collection 头 | S/L/H20/V |
| T12 ROWID / UROWID | — | 受限 | type209 结果按 ROWID 文本处理；[RowID](../types/scalar.go)参数用 type253 字符串，client 用 CHARTOROWID；没有独立 UROWID/type209 参数 writer 契约 | S/L/H20（ROWID 子集）/V |
| T13 原生 BLOB/CLOB locator | — | 受限 | [OpenBlob/OpenClob](../lob.go)、[DBMS_LOB](../internal/lob/native.go)；固定 Conn/Tx、版本/locator 门槛；可分块读写/截断/临时释放，字符偏移有明确 Unicode 规则 | S/L/H20/V |
| T14 命名 OBJECT/collection、描述缓存 | — | 受限 | [Object/Array](../types/complex.go)、[复杂绑定](../internal/parameters/complex.go)、[编码](../internal/valuecodec/complex.go)；NUMBER/字符/RAW/DATE/嵌套对象等子集；非 NULL TIMESTAMP/LOB 成员输入拒绝，若干 builtin collection 头缺失 | S/L/H20（指定 binary/PL 子集）/V |
| T15 复杂值文本、LOB/cursor 成员 | — | 受限 | nested LOB binary 结果物化已有；[物化入口](../internal/materialize/decoder.go)拒绝 text type160；无通用 text OBJECT parser、LOB/locator 成员输入或 cursor 成员全链路契约 | S/L/H20（指定结果）/V |
| T16 LONG/LONG RAW/BFILE/NCLOB | — | 排除服务端支持承诺 | 官方 [Oracle 类型表][O4]不支持四者；现有 NCLOB 名称/locator 解码分支不能证明原生类型可用，H20 NCLOB DDL 失败保留 | S/L/H20（负向） |
| T17 XMLTYPE、SDO_GEOMETRY、ANYDATA | — | 未实现专用契约 | XMLTYPE 未按 ownerless type160 String 子集适配；SDO 可研究通用 Object/Array，但未专门验收；ANYDATA 无公开协议/映射；SQL 显式转换可作为应用绕行，不能算原生支持 | S/V |
| T18 VECTOR、SPARSEVECTOR、SQL ARRAY、RoaringBitmap | 未验收通用路径；无专用 codec | — | [O11][O11]/[O12][O12]/[O13][O13]有服务端依据；现有 string/[]byte 及 SQL 转换可能适用，尚无专项类型/metadata/存储往返验收；Go Array 是 Oracle collection，不能据此标 SQL ARRAY 已支持 | S/V |
| T19 SQL/握手字符集 | 受限 | 受限 | [字符集实现](../internal/charset/charset.go)支持 utf8mb4、utf8/mb3、latin1、ASCII、binary、GBK、GB18030、GB18030_2022、HKSCS/HKSCS31；不在白名单的 client 编码拒绝 | S/L/H20（指定编码）/V |
| T20 结果/national 编码 | 受限 | 受限 | [collation 映射](../internal/charset/charset.go)及 UTF16 大端解码；UTF16 不用作 SQL client charset；缺 GB2312/EUCKR/SJIS/Big5/DEC8/TIS620 等映射，未知 collation 拒绝 | S/L/V |
| T21 表 BLOB 随机分块访问 | 受限 | 受限 | [OpenTableBlob](../lob.go)、[表 BLOB 实现](../internal/lob/table.go)；要求可确定 schema/基表/完整 PK，MySQL 用长度/子串读写，Oracle 复用 native Blob；修改要求可写事务及适用锁/引擎条件，普通 Conn 跨块读取不承诺快照 | S/L/H20（指定子集）/V |

## 事务、协议、元数据与交付

| ID / 能力 | 当前能力与边界 | 入口与证据 |
| --- | --- | --- |
| X01 本地事务、隔离级别、只读、savepoint | 双模式有实现；Oracle 不接受 RepeatableRead、ReadOnly+ReadCommitted 等非法组合；DDL 隐式提交等由服务端决定 | [事务](../internal/control/transaction.go)、[保存点](../internal/control/savepoint.go)；S/L/H20/V |
| X02 环境事务、分布式协调器 | Go 不复制 TransactionScope；不内置持久化全局事务协调器，应用负责决策/日志/恢复 | [XA API](../xa.go)；接口差异/排除，不计普通本地事务缺失 |
| X03 XA start/end/prepare/commit/rollback/recover | 双模式有实现，MySQL XA SQL / Oracle DBMS_XA；固定专用会话；Forget、RM timeout 不伪造支持，join/suspend 受目标限制 | [XA 实现](../internal/xa/manager.go)；S/L/H20/V |
| X04 unknown outcome 与连接淘汰 | 发送后的故障不自动重放；写入/提交未知保留分类，旧资源不能进入新租约；不等于已经证明真实节点崩溃恢复 | [错误](../dberrors/errors.go)、[XA](../internal/xa)；S/L/H20（正常与指定故障子集）/V |
| P01 标准包、EOF、逻辑包边界 | 双模式有实现，校验序号/长度/截断，资源与结果预算有界 | [wire](../internal/wire/packet.go)、[command](../internal/command)；S/L/H20 |
| P02 标准 zlib 压缩 | 依真实协商，auto 可回退；有帧编码/解码及 Java 互操作证据，不等于真实服务已使用压缩 | [compress](../internal/wire/compress.go)、[互操作用例](../internal/wire/compression_java_test.go)；S/L/H20（回退）/V |
| P03 OB20、CRC、proxy checksum、新 metadata | 有实现，各能力分别协商；OB20 CRC、proxy checksum、SQL checksum 是不同能力 | [OB20](../internal/wire/ob20.go)、[协议配置](../config/protocol.go)；S/L/V |
| P04 Oracle PrepareExecute、SQL checksum | 受限：PrepareExecute 依真实版本 ≥2.2.76，普通执行 SQL checksum 依 ≥4.1.2；相关开关显式开启，不绕过版本/模式检查 | [执行选项](../config/execution_options.go)、[native statement](../internal/statement/native.go)；S/L/H20/H24/V |
| P05 Full Link Trace | 有协商/extra-info/采样控制/trace-span 信息子集；需 OB20 与目标支持；客户端事件不是服务器 trace 或 collector 验收 | [trace](../internal/diagnostic/trace.go)、[wire trace](../internal/wire/trace.go)；S/L/V |
| P06 压缩 OB20、proxy reroute | 排除当前支持承诺；不由 capability 常量推定有执行实现 | [兼容性条件](go-driver-compatibility-matrix.md)；S/V（未来协议依据） |
| M01 数据库、列、键、索引、权限、过程、UDT 元数据 | 双模式目录计划有实现，Oracle UDT/同义词与 MySQL 目录差异保留；不存在的 Oracle catalog 返回空；权限错误不伪造为空成功 | [Metadata API](../metadata.go)、[catalog](../internal/catalog)；S/L/H20（子集）/V |
| M02 日志、慢请求、包/请求诊断 | slog 注入和有界 callback；不记录原始 SQL、参数、凭据和包体；Rows 时长包含消费时间 | [诊断配置](../config/diagnostic.go)、[诊断实现](../internal/diagnostic)；S/L/V（负载） |
| M03 池指标、OpenTelemetry 接入 | `sql.DB.Stats` 有池统计，callback 可供应用接入观测；无内置 OTel exporter、.NET Activity/Meter 或 Java JMX 等价承诺 | [池示例](../pool_example_test.go)、[诊断配置](../config/diagnostic.go)；标准库/应用适配，非协议缺失 |
| R01 Go 版本、平台、CI、性能/长稳 | Go 1.26+；已有 Windows 本地及历史 hosted 检查记录，当前所有 OS/tag/真实部署组合尚未全验；本地 peer benchmark 不代表实库吞吐或长期稳定性 | [go.mod](../go.mod)、[历史发布检查](release-validation-2026-10-20.md)、[benchmarks](go-driver-benchmarks.md)；H20/V |
| R02 JDBC/ADO.NET 专属 API 与容器集成 | 不复制 JDBC 类、ADO.NET factory/DI、DataAdapter、CommandBuilder、DbBatchReader；Go 用 sql.DB/Conn/Tx、Rows、ExecBatch 等自身契约 | [用法](go-driver-usage.md)；生态接口差异 |
| R03 原生数据库/运维产品功能 | 不实现 Oracle OCI/Oracle Net、binlog 复制、备份恢复管理器、Aurora/Galera 控制面；可执行目标支持的 SQL 不等于提供专用产品 API | 排除 |

## 依据与维护

能力状态以 Go 源码为准；细粒度版本/模式条件继续由[兼容性矩阵](go-driver-compatibility-matrix.md)维护，配置迁移见[选项映射](go-driver-options-mapping.md)。本表不沿用开发任务数计算“全功能完成率”。

参考材料：

- Java 本地 `obconnector-j` 的 `OceanBaseConnection`、`JDBC4ServerPreparedStatement`、`JDBC4ServerCallableStatement`、`OceanBaseXaResource`、`DefaultOptions`、`ComplexUtil` 与协议实现；结构导航见[Java 功能分析](obconnector-j-architecture-outline.md)。Java 的 `createSQLXML()` 明确拒绝，复杂成员 reader 与 writer 也有不同范围，不能仅看枚举判断。
- .NET 本地 `obconnector-dotnet/docs/obconnector-dotnet-progress.md`、`obconnector-dotnet-completeness.md`、`obconnector-dotnet-scope-audit.md`：用于检查功能域是否遗漏，尤其字符集、UROWID、XMLTYPE、复杂成员输入、批处理、新类型与验收分类；不将 .NET 的通过结果记为 Go 通过。
- 以下 OceanBase 官方文档已在本次审计中读取。链接固定文档内容以便复核；其中“当前版本”是页面语境，不表示所有历史/未来 OceanBase 版本均有此能力。

| 来源 | 约束本矩阵的事实 |
| --- | --- |
| [O1 兼容模式][O1] / [O5 Connector/J][O5] | 模式属于租户；统一驱动不消除 SQL 方言差异 |
| [O2 MySQL 兼容性][O2] / [O3 Oracle 兼容性][O3] | 服务端 SQL、类型、字符集与目录存在版本相关差异 |
| [O4 Oracle 类型][O4] | LONG、LONG RAW、BFILE、NCLOB 不作为原生支持承诺 |
| [O6 XA][O6] | OceanBase 是 RM，全局决策由 TM/应用负责 |
| [O7 SSL 客户端认证][O7] | TLS/mTLS 依赖服务端配置，不能由客户端开关独立保证 |
| [O8 XMLTYPE][O8] / [O9 SDO_GEOMETRY][O9] / [O10 ANYDATA][O10] | 有服务端专用类型不等于已有 Go codec；ANYDATA 限 PL，不是普通持久列 |
| [O11 向量][O11] / [O12 SQL ARRAY][O12] / [O13 位图][O13] | 需独立核对类型、输入/结果、metadata、版本；SPARSEVECTOR 另有版本门槛 |

[O1]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/100.oceanbase-database-concepts/300.multi-tenant-architecture/200.compatibility-mode.md
[O2]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/100.learn-more-about-oceanbase/500.compatibility-with-mysql.md
[O3]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/100.learn-more-about-oceanbase/400.compatibility-with-oracle/100.overview-of-compatibility-with-oracle.md
[O4]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/100.learn-more-about-oceanbase/400.compatibility-with-oracle/200.sql-data-type.md
[O5]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/1300.drivers/500.oceanbase-connector-j.md
[O6]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/100.oceanbase-database-concepts/800.transaction-management/100.transaction/900.xa-transactions.md
[O7]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/800.configuration-items-and-system-variables/100.system-configuration-items/300.cluster-level-configuration-items/20800.ssl_client_authentication.md
[O8]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/500.sql-reference/100.sql-syntax/300.common-tenant-of-oracle-mode/300.basic-elements-of-oracle-mode/100.built-in-data-types-of-oracle-mode/1100.xml-date-type-of-oracle-mode/100.xml-data-type-overview-of-oracle-mode.md
[O9]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/500.sql-reference/100.sql-syntax/300.common-tenant-of-oracle-mode/300.basic-elements-of-oracle-mode/100.built-in-data-types-of-oracle-mode/1200.spatial-data-type-of-oracle-mode/100.spatial-data-type-overview-of-oracle-mode.md
[O10]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/600.pl-reference/300.pl-oracle/1400.pl-system-package-oracle/27800.anydata-type-oracle/100.anydata-type-overview-oracle.md
[O11]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/640.ob-vector-search/700.ob-vector-search-reference/100.ob-vector-data-type.md
[O12]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/500.sql-reference/100.sql-syntax/200.common-tenant-of-mysql-mode/100.basic-elements-of-mysql-mode/100.data-type-of-mysql-mode/1200.array-data-type-of-mysql-mode/100.array-data-type-overview-of-mysql-mode.md
[O13]: https://github.com/oceanbase/oceanbase-doc/blob/2a5ae3a23a51729d065cec716508eb26cb064106/zh-CN/700.reference/500.sql-reference/100.sql-syntax/200.common-tenant-of-mysql-mode/100.basic-elements-of-mysql-mode/100.data-type-of-mysql-mode/1000.roaring-bitmap-data-type-of-mysql-mode/100.roaring-bitmap-data-type-overview-of-mysql-mode.md
