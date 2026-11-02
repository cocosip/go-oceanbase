# Go 驱动能力矩阵

更新日期：2026-11-02。本文描述 **go-oceanbase 自身的公开能力与支持边界**；依据 OceanBase 官方文档、Connector/J 实现、.NET 驱动已有功能矩阵和本项目源码核对。归因与待办见[功能缺失与限制矩阵](go-driver-completeness.md)。已按本次复核拆分客户端、服务端、权限、部署与未知前提。

## 阅读口径

- **模式两列只描述 Go 客户端实现**；“服务端/部署判定”另列。客户端有实现不表示服务器提供该插件/类型/协议，服务器拒绝一个形状也不否定整项能力。
- **有实现**：存在公开入口及对应实现，不代表所有版本、部署和选项组合均已验收。
- **受限**：仅实现表中明确的子集；输入、结果、OUT、文本、binary、流和 locator 不互相替代。
- **缺实现**：仅限已明确可实现而 Go 缺少的子集；**待验证**不作为缺功能，通用 string/bytes 路径不能因没有专用 codec 被否定。
- **—**：该模式或 Go 接口不适用；**排除**表示不作为当前驱动待开发项。

本表的 G/C/S/B/A/ENV/V/U/X 编号指向缺失与限制矩阵的独立分类：G=确认客户端差距，C=客户端范围，S=官方不支持形态，B=指定目标拒绝，A=权限，ENV=环境，V=待验，U=依据不足，X=范围排除。H20/H24 仅为 Go 历史记录；N 为参考 SDK 实测记录，未由 Go 复现。证据列 SRC 表示源码核对，与分类 S（官方不支持）不同；C/X 等能力行编号与缺失表分类编号分别使用。

服务端支持一种 SQL 类型，不等于驱动支持它的专用编码；Java/.NET 存在接口，也不要求 Go 复制类名和配置项。SQL 引擎、索引、分区、向量搜索算法和运维管理能力由数据库提供，不单独计为驱动功能。

Oracle 模式相关能力以具有该模式的产品版本/租户为前提；官方 ANYDATA 页面明确适用于企业版，社区版仅提供 MySQL 模式。不能将 Oracle 列视为所有 OceanBase 部署的通用承诺。

### 验证证据

| 标记 | 含义与限制 |
| --- | --- |
| SRC | 本次源码核对，入口见各行；没有执行该行实库用例 |
| L | 仓库存在本地单测、协议 peer 或故障用例；不是本次重跑结果，也不是服务端兼容性证明 |
| H20 | [历史实库报告](release-validation-2026-10-20.md)：OceanBase 4.4.2.2 双模式直连；仅限报告列明场景 |
| H24 | [浮点专项报告](binary-double-live-validation-2026-10-24.md)：双模式直连 float64 参数及 Oracle 数组绑定子集 |
| V | 尚缺对应真实部署、类型形状或故障验收；可以与 H20/H24 同时存在 |

H20 的独立工具结果为 MySQL **35 pass / 0 fail / 2 skip / 8 不适用**、Oracle **38 pass / 0 fail / 2 skip / 5 不适用**，每模式选择 45 场景。不能由此推导下表每行均通过。报告中的旧测试目录已经调整，当前集成测试分布在各 `internal/*` 包及其子目录；复验使用当前入口，不直接照搬历史路径。

## 连接、会话与安全

| ID / 能力 | Go实现：MySQL | Go实现：Oracle | Go入口与客户端边界 | 服务端/部署判定 | 证据 |
| --- | --- | --- | --- | --- | --- |
| C01 连接、Ping、关闭、模式识别 | 有实现 | 有实现 | [Open / NewConnector](../connector.go)、[建连协调](../internal/connect/connector.go)；认证后识别模式，显式模式不符拒绝；同一连接不切换租户模式 | H20双模式连接子集；非全部署 | SRC/L/H20 |
| C02 服务端版本与能力协商 | 有实现 | 有实现 | [配置](../config/config.go)、[协议](../internal/connect/protocol.go)；使用 `ob_version()`，不拿 MySQL 握手兼容版本代替；未知版本关闭依赖版本的扩展，无服务端版本覆盖开关 | 实际探测版本/协商，未知关闭扩展 | SRC/L/H20/V |
| C03 DSN、配置快照和校验 | 有实现 | 有实现 | [ParseDSN](../config/dsn.go)；`oceanbase://`，键名不区分大小写，重复/未知键拒绝，值不改写；不直接接受 JDBC URL 或全部 Java 参数 | Go解析契约，数据库无关 | SRC/L |
| C04 TCP、自定义拨号、SOCKS5 | 有实现 | 有实现 | [传输](../internal/transport/transport.go)、[传输配置](../config/transport.go)；TCP 调优通过 `DialContext`/标准库，SOCKS5 不替代 TLS | 普通TCP有H20；SOCKS部署另待验 | SRC/L/H20（普通 TCP）/V |
| C05 Unix socket、Named Pipe、Shared Memory、VC | 受限 | 受限 | [传输目录](../internal/transport)、[OBProxy VC](../obproxy/dialer.go)；有 OS/build tag/运行库限制；共享内存分配等待不能保证硬取消；VC 是原生代理嵌入入口，另需真实 Linux 运行验收 | 真实OS/服务IPC/VC前提ENV05 | SRC/L/V |
| C06 TLS、主机名验证、mTLS | 有实现 | 有实现 | `Config.TLSConfig` 使用 Go `crypto/tls`/`x509`；拒绝未验证 TLS 授权敏感认证；JKS/PKCS12 转换由应用/部署负责 | TLS是部署能力；ENV01/V01，不是官方不支持 | SRC/L/V；H20 仅真实端点拒绝/未开启边界 |
| C07 native password | 有实现 | 有实现 | [认证流程](../internal/connect/authentication.go)、[native](../internal/auth/native.go)；不因 Oracle SQL 模式改用 Oracle Net/OCI | H20指定native账号，不外推插件 | SRC/L/H20 |
| C08 SHA256、caching-SHA2、old、clear、Ed25519、PAM、GSSAPI | 受限 | 受限 | [认证配置](../config/authentication.go)、[认证扩展](../internal/authentication)；old 显式开启，clear/PAM 要求已验证 TLS，非 TLS RSA 只用预先固定公钥；GSSAPI 依 OS/tag/票据 | 逐插件服务端支持未证实，ENV04/U | SRC/L/V；客户端实现不证明 OceanBase 安装相应插件 |
| C09 凭据回调、认证扩展、代理身份 | 有实现 | 受限 | `Credentials`/`Auth.Plugins`；[ProxyUser 配置](../config/proxy_user_test.go)仅 Oracle，MySQL 拒绝；连接器固定逻辑 User/ProxyUser，允许密码变化，不在活动事务中自动换身份 | ProxyUser依Oracle授权A02；凭据回调为Go契约 | SRC/L/V |
| C10 标准连接池与资源隔离 | 有实现 | 有实现 | 使用 `database/sql` 容量/等待/空闲/寿命/统计；[reset](../internal/control/reset.go)及资源代际隔离；不是另建私有连接池，也没有标准库未提供的最小连接数保证 | H20有限并发；长期资源V06 | SRC/L/H20（有限并发）/V（长稳） |
| C11 初始化、属性、超时 | 有实现 | 有实现 | `Session.InitSQL/Variables`、`ConnectionAttributes`、`Interactive`、`ConnectTimeout/ReadTimeout`；调用总预算使用 context；读超时逐次计算，不保证服务器已停止 SQL | 逐选项/服务端参数V10，不推定都已验 | SRC/L/V |
| C12 database/schema、ChangeUser、会话变量 | 有实现 | 受限 | [会话 API](../session.go)、[会话控制](../internal/control/session.go)；MySQL 切 database、Oracle 切 current schema；Oracle catalog 不适用；更改身份/状态影响池复用和资源代际 | MySQL H20子集；Oracle身份权限A02 | SRC/L/H20（指定会话场景）/V |
| C13 自动创建缺失数据库 | 受限 | — | `CreateDatabaseIfMissing` 显式启用，仅新建 MySQL 物理连接；先创建/选择再 InitSQL；不是迁移或 schema 管理器 | MySQL且需CREATE权限；Oracle不适用 | SRC/L/H20（指定创建场景）/V |
| C14 多地址、角色、负载分配、连接故障转移 | 有实现 | 有实现 | [路由](../internal/routing/selector.go)；只在建连阶段切候选，角色来自配置；TLS/auth/已发 SQL 不作为业务重试理由，不自动识别 SELECT 并分流 | 真实拓扑/角色ENV03/V03 | SRC/L/V |
| C15 DESCRIPTION/TNS、OCP、拓扑刷新 | 有实现 | 有实现 | [TNS](../internal/routing/tns.go)、[OCP](../internal/routing/ocp.go)、[拓扑](../internal/routing/topology.go)；TNS 是地址描述解析，不是 Oracle Net；同租户/主备身份需部署证明 | 服务发现/身份ENV03/V03，不是Oracle Net | SRC/L/V |
| C16 活动会话透明迁移、自动事务/SQL 重放 | 排除 | 排除 | 发送后断线分类并淘汰连接，应用显式判断和重建；不以重新执行掩盖未知结果 | X02客户端安全/职责选择 | SRC/L |
| C17 warning 计数、详情与清除 | 有实现 | 受限 | [会话 API](../session.go)、[warning 控制](../internal/control/warnings.go)；两模式记录 OK/EOF 计数，MySQL SHOW WARNINGS 详情；Oracle 详情明确拒绝；ClearWarnings 本地抑制，不是历史诊断档案 | H20指定warning；Oracle详情仅客户端范围C11 | SRC/L/H20（指定 warning 场景）/V |

## 执行、参数、结果与批处理

| ID / 能力 | Go实现：MySQL | Go实现：Oracle | Go入口与客户端边界 | 服务端/部署判定 | 证据 |
| --- | --- | --- | --- | --- | --- |
| Q01 text SQL、CRUD、DDL、错误 | 有实现 | 有实现 | 标准 `ExecContext/QueryContext`、[执行引擎](../internal/execution/execute.go)；SQL 语义由相应兼容模式决定，保留服务端错误 | 只执行对应兼容模式支持的SQL | SRC/L/H20 |
| Q02 位置/命名参数、NULL、重复占位 | 受限 | 有实现 | [绑定](../internal/parameters)、[词法](../internal/sqltext)；MySQL 普通 SQL 用位置参数，命名参数只覆盖可解析简单 CALL；Oracle 维护逻辑名与物理槽映射 | H20限定绑定/CALL；目录权限V07 | SRC/L/H20/V |
| Q03 server prepare、复用、binary 结果 | 有实现 | 有实现 | [statement](../internal/statement/statement.go)；`PrepareContext` 和默认参数化路径；不是所有服务端语句都可 prepare | 服务端prepare语句范围依版本/形状 | SRC/L/H20 |
| Q04 client prepare 与自动回退 | 有实现；自动回退未提供 | 有实现；自动回退未提供 | [客户端执行](../internal/execution/client.go)、[字面量](../internal/valuecodec/literal.go)；显式 PrepareClient 可用；普通 server prepare 拒绝时返回调用方，自动1295回退属可选范围 C02，不是参数化SQL全部缺失 | 自动回退C02；原生1295目标前提未证实 | SRC/L/H20（显式路径）/V |
| Q05 文本多语句、多结果 | 受限 | 受限 | `AllowMultiStatements`、`Rows.NextResultSet`、[Execute](../execution.go)；多语句显式启用；参数字面量只覆盖已有类型，不支持 Object/Array/LongData 任意组合 | 脚本逐形状；构造输入G07b与权限/引擎分开 | SRC/L/H20/V |
| Q06 过程、函数、OUT/INOUT、RETURNING | 有实现 | 受限 | [Out / CursorOut](../call.go)、[调用实现](../internal/call)；MySQL 用同会话私有变量；Oracle 原生输出要求真实版本门槛；结果排空/清理后发布输出 | H20标量/过程子集；复杂方向逐项核实 | SRC/L/H20/V |
| Q07 过程/语句参数元数据 | 有实现 | 受限 | [Metadata](../metadata.go)、[目录计划](../internal/catalog/metadata_sql_oracle.go)；语句元数据实际 prepare/close；不提供 .NET 式 DeriveParameters 自动创建参数对象，PL-only RECORD 不等于命名 SQL OBJECT | 目录V07、缺列参考B12、PL RECORD U02 | SRC/L/H20（目录子集）/V |
| Q08 Rows、Scan、ColumnTypes、计数与自增 ID | 有实现 | 有实现 | [Rows](../internal/execution/rows.go)、[Execute](../execution.go)；扩展结果保留列标签/原始列与表身份；完整 uint64 ID 保留文本；聚合结果不猜逐项 ID；Oracle 不接受 ChangedRows | 模式决定计数/ID语义；历史子集H20 | SRC/L/H20 |
| Q09 流式 Rows 与大字段 | 受限 | 受限 | 行迭代流式；普通字段/复杂值受消息及物化限额约束；大对象使用 [LOB](../lob.go)/[TableBlob](../internal/lob/table.go)，不能据此宣称任意列无限流式 | 客户端预算C10；大值目标能力另验 | SRC/L/H20/V |
| Q10 显式 cursor、分页、滚动、可更新结果 | 受限 | 受限 | [OpenCursor](../cursor.go)、[游标实现](../internal/cursor)；静态快照有界；敏感游标仅指定 Oracle 版本/单表形状，以 ROWID 重查；更新要求可写事务、完整 PK 等 | H20敏感游标子集；锁/PK/版本有前提 | SRC/L/H20（含 Oracle 敏感刷新）/V |
| Q11 REF CURSOR OUT / 子结果 | — | 受限 | [资源输出](../internal/call)、[物化](../internal/materialize/decoder.go)；OUT与binary子cursor有实现；text客户端拒绝；SELECT CURSOR指定服务端失败形状B03单列，不能推导所有cursor表达式不可用 | 过程OUT H20；SELECT CURSOR仅B03形状受限 | SRC/L/H20（过程OUT）/V |
| Q12 REF CURSOR IN/INOUT | — | 当前范围排除 | 无参考输入writer契约，按X01排除；不能从OUT或scalar INOUT推导输入支持，也不能说服务器全面不支持 | X01/U03：无writer依据，不断言服务端无输入 | SRC/L（拒绝） |
| Q13 SchemaOnly/KeyInfo、DataAdapter 等生态接口 | — | — | 不复制 ADO.NET CommandBehavior/Disconnected DataSet；使用 Metadata、显式 SQL 和 Go `database/sql` | Go/ADO.NET生态不同，无服务端结论 | 接口差异，不计驱动缺失 |
| Q14 顺序批处理、逐项结果 | 有实现 | 有实现 | [ExecBatch](../batch.go)；每项区分未执行/成功/失败/未知，不自动开启或提交事务 | H20有限批处理；不保证原子性 | SRC/L/H20/V |
| Q15 MySQL 多语句 rewrite / multi-VALUES | 受限 | — | [策略选择](../internal/batch/strategies.go)、[multi-VALUES](../internal/batch/values.go)；`BatchRewrite` 和 `BatchMultiValues` 是不同策略；前者还要求多语句许可，后者保留聚合 chunk 结果 | 服务端多语句/VALUES支持与实际策略分开 | SRC/L/H20（报告子集）/V |
| Q16 Oracle DML 数组绑定 | — | 受限 | `BatchArrayBinding`；[门槛](../internal/batch/strategies.go)：真实版本 ≥4.2.1、可写事务、server prepare、至少两项、ContinueOnError、限定标量等；不是 collection 参数 | 版本/可写事务门槛；H24验证Oracle native子集 | SRC/L/H20/H24/V |
| Q17 自动语句缓存、参数类型头复用 | 有实现 | 有实现 | `Execution.StatementCacheSize/ReuseParameterTypes`；缓存默认关闭；同物理会话/类型描述/成功状态内复用，身份、session SQL/reset 等触发失效 | 客户端缓存优化；DDL/目录失效V07 | SRC/L/H20/V |
| Q18 LOCAL INFILE / 导入 | 受限 | 受限 | [Import](../local_infile.go)、[导入校验](../internal/execution/import.go)；单次应用流授权，不按服务器文件名读取本地文件；两模式均要求 autocommit、无本地事务且未进入 XA 专用状态 | H20两模式导入成功；事务安全为客户端限制 | SRC/L/H20/V |
| Q19 长参数分片上传 | 有实现 | 受限 | [LongData](../long_data.go)、[上传](../internal/statement/upload.go)；MySQL long-data / Oracle piece-data，Oracle 依版本；单次 Reader、有界且不自动回放；client prepare 不支持流 | MySQL long-data/Oracle逐片ACK；U04不猜pipeline | SRC/L/H20/V |
| Q20 multi-send / pipelining | 受限 | 受限 | [批量 native 选择](../internal/batch/native.go)、[pipeline](../internal/batch/pipeline.go)；server prepare、标量 DML、ContinueOnError 等门槛；framed 传输/Shared Memory 或不合资格形状走顺序；不是 SELECT/复杂值/流的通用 pipeline | 客户端优化范围C09，不是服务器不能执行SELECT | SRC/L/H20（指定子集）/V |
| Q21 MariaDB bulk 协议 | 排除 | — | 枚举 `BatchBulk` 不表示已有 bulk writer；当前不合资格策略走顺序；不能把 Java MariaDB 继承选项当 OceanBase 必需协议 | X03非当前OceanBase协议承诺 | SRC/L |
| Q22 context、读超时、显式 CancelQuery | 有实现 | 有实现 | [取消控制](../internal/control/cancel.go)；命令预算、单次目标及辅助连接 KILL；权限/代理路由影响实际中断；取消不保证无数据库效果 | 实际KILL权限/路由/中断码V10 | SRC/L/H20（报告子集）/V |
| Q23 活动操作互斥、资源所有权 | 有实现 | 有实现 | [session](../internal/session)、[lifetime](../internal/lifetime)；LOB/cursor/输出绑定固定 Conn/Tx；不承诺同一连接多活动结果并行，跨连接并发交给池 | 客户端所有权；真实资源交错V09 | SRC/L/H20（有限并发）/V |
| Q24 JDBC escape 转换与 SQL 词法 | 受限 | 受限 | [RewriteJDBCEscapes](../escape.go)、[sqltext](../internal/sqltext)；识别注释/引用/占位并按模式转换已有 escape 子集；不是任意 SQL 方言转换器 | 客户端转换，目标SQL函数仍需支持 | SRC/L/V |

## 类型、编码与复杂值

| ID / 能力 | Go实现：MySQL | Go实现：Oracle | Go映射与客户端边界 | 服务端/部署判定 | 证据 |
| --- | --- | --- | --- | --- | --- |
| T01 整数、unsigned、布尔 | 有实现 | 有实现 | [scalar codec](../internal/valuecodec/binary.go)；结果保持整数/精确文本；无符号超 int64 不经 float64；TINYINT/BIT/YEAR 不照搬 Java 可变返回类型 | 模式/列定义决定取值范围 | SRC/L/H20/V |
| T02 DECIMAL / NUMBER / FLOAT 精度 | 有实现 | 有实现 | [Number](../types/number.go) 保留精确文本，Scan/元数据分开；不是用 float64 覆盖任意 NUMBER | 官方支持；实际precision/scale V07 | SRC/L/H20/V |
| T03 FLOAT/DOUBLE、BINARY_FLOAT/BINARY_DOUBLE | 有实现 | 有实现 | float64 输入 DOUBLE wire 与握手能力配合；[专项](binary-double-live-validation-2026-10-24.md)验证存储/回读；旧服务器协商组合另验 | H24双模式float64子集，旧版本另验 | SRC/L/H20/H24/V |
| T04 字符串、二进制、RAW、BLOB/TEXT 内容 | 有实现 | 有实现 | string/[]byte/[Raw](../types/scalar.go)，严格编码；Oracle 空串和目标容量由服务端语义决定；内容与 locator 分开 | 官方支持；空串/容量/编码按模式 | SRC/L/H20/V |
| T05 ENUM/SET/GEOMETRY | 通用映射有实现 | — | [结果映射](../internal/valuecodec/text.go)：ENUM/SET文本、GEOMETRY原始bytes；通用输入与实际列/SRID归V13，无GIS领域模型不单独算缺失 | 官方MySQL类型；通用路径V13/SRID另验 | SRC/L/V |
| T06 JSON | 通用映射有实现 | 专用映射未承诺 | MySQL type245结果校验UTF-8返回string，string输入交给服务端转换；没有Json专用Go类型/DOM不是缺功能；Oracle原生类型/wire不由MySQL通过推导 | MySQL官方类型；Oracle专用wire未证实 | SRC/L/H20（报告子集）/V |
| T07 BIT / YEAR | 通用映射有实现 | — | BIT返回bytes、YEAR整数；[公开测试](../internal/valuecodec/public_scalar_compatibility_test.go)覆盖三执行路径/三种流；是否需要独立native输入身份归U07，先验V13真实存储 | MySQL官方类型；真实列/转换V13 | SRC/L/V |
| T08 DATE/DATETIME/TIME、零日期 | 受限 | 受限 | [时间编码](../internal/valuecodec/temporal.go)、[time.Time 输入](../internal/valuecodec/binary.go)；MySQL 精度上限为微秒、Oracle DATE 含时间；ParseTime 不能表示的值拒绝；零日期依配置与服务端 SQL_MODE | 零日期SQL_MODE；TIME参考形状B13 | SRC/L/H20/V |
| T09 TIMESTAMP、TZ/LTZ | TIMESTAMP有实现 | 受限 | [精确时间值](../types/temporal.go)；Oracle时间保留纳秒/偏移/地区，LTZ用会话墙钟；N指定named-region binary失败B09不否定数字偏移/text/LTZ | 官方支持；指定named-region binary参考B09 | SRC/L/H20/V |
| T10 INTERVAL YM/DS | — | 有实现 | [IntervalYM/DS](../types/temporal.go)，原生与 client literal 路径；区分精度、符号和 NULL | 官方Oracle类型；精度/方向分别验 | SRC/L/H20/V |
| T11 NCHAR/NVARCHAR2 | — | 受限 | [NString](../types/scalar.go)、`Session.NCharset`，scalar 输入/结果和限定复杂成员；不能覆盖任意 national collection 头 | 官方Oracle national类型；collection头U01 | SRC/L/H20/V |
| T12 ROWID / UROWID | — | 有实现；实库待验收 | type209 结果仍按 ROWID 文本读取；`RowID` 保持 type253/CHARTOROWID；`URowID` 显式 type209/CAST，含 typed NULL、OUT/INOUT；G03 本地实现已补，结果元数据不猜 UROWID 名称 | 官方支持；新增长值存储/PL INOUT Go 入口未配置 DSN | SRC/L/H20（仅原 ROWID 子集）/V |
| T13 原生 BLOB/CLOB locator | — | 受限 | [OpenBlob/OpenClob](../lob.go)、[DBMS_LOB](../internal/lob/native.go)；固定 Conn/Tx、版本/locator 门槛；可分块读写/截断/临时释放，字符偏移有明确 Unicode 规则 | BLOB/CLOB支持；locator/版本/资源V09 | SRC/L/H20/V |
| T14 命名 OBJECT/collection、描述缓存 | — | 受限 | [Object/Array](../types/complex.go)、[绑定](../internal/parameters/complex.go)、[codec](../internal/valuecodec/complex.go)有NUMBER/字符/RAW/DATE/嵌套等；PL中LOB内容读取子集缺输入G05a；TIMESTAMP/collection头先查U01；持久nested列B02、BLOB成员写列B06、根NULL输出B08分开 | 指定PL子集可用；持久列B02/写列B06/根NULL B08分开 | SRC/L/H20（binary/PL子集）/V |
| T15 复杂值text、LOB/cursor成员 | — | 受限 | binary nested LOB结果物化已有；[入口](../internal/materialize/decoder.go)拒绝text type160；已证明scalar/nested named text结果缺口G07a与坏payload/LOB成员身份B07分开；locator/cursor专门输入尚缺依据U03 | scalar/nested text可用；坏成员形状B07分开 | SRC/L/H20（指定结果）/V |
| T16 LONG/LONG RAW/BFILE/NCLOB | — | 不承诺原生支持；兼容分支不作支持证据 | 官方[类型表][O4]明确不支持，归S01；现有NCLOB名称/locator分支和GetTypeInfo不表示目标可建列；H20 NCLOB902是负向记录 | S01官方明确不支持四个原生类型 | SRC/L/H20（负向） |
| T17 XMLTYPE String列/函数结果 | — | 有实现；实库待验收 | ownerless/SYS XMLTYPE type160 按严格 UTF-8 string/NULL 解码，支持 text/binary，结果类型名 XMLTYPE；不误识别用户同名 OBJECT；G04 本地适配已补；string 参数仍沿既有路径 | 官方支持；Go 列/函数新入口未配置 DSN；PL OUT/INOUT 指定 B05 保留 | SRC/L/V；N仅参考 |
| T18 VECTOR稠密向量 | 通用string路径已有，专项待验 | — | [官方类型][O11]；N列值/metadata为CHAR/String，Go的string绑定/文本解码可承载，归V14；没有专用向量codec不算已确认缺功能 | 官方支持；N的CHAR/String无需专用codec | SRC/V；N仅参考 |
| T19 SQL/握手字符集 | 受限 | 受限 | [字符集实现](../internal/charset/charset.go)支持 utf8mb4、utf8/mb3、latin1、ASCII、binary、GBK、GB18030、GB18030_2022、HKSCS/HKSCS31、SJIS、Big5、DEC8、TIS620、GB2312、EUCKR；不在白名单的 client 编码拒绝 | G01本地映射已补；新实库待验；Oracle B04分开 | SRC/L/H20（指定编码）/V |
| T20 结果/national编码 | 受限 | 受限 | [collation映射](../internal/charset/charset.go)、UTF16大端/小端及GB2312/EUCKR/SJIS/Big5/DEC8/TIS620严格映射已补（G01）；client、result、national和Oracle失败初始化B04分别判断 | G01已补本地路径，实库逐用途验；B04保留 | SRC/L/V |
| T21 表 BLOB 随机分块访问 | 受限 | 受限 | [OpenTableBlob](../lob.go)、[表 BLOB 实现](../internal/lob/table.go)；要求可确定 schema/基表/完整 PK，MySQL 用长度/子串读写，Oracle 复用 native Blob；修改要求可写事务及适用锁/引擎条件，普通 Conn 跨块读取不承诺快照 | H20子集；基表/PK/锁/引擎为前提 | SRC/L/H20（指定子集）/V |
| T22 SDO_GEOMETRY | — | 通用Object/Array待验 | 官方[复合类型][O9]；N有generic OBJECT/ARRAY整列读取/受限输入，先验Go描述符/成员/NULL/SRID，归V17；无GIS专用模型本身不算缺失 | 官方复合类型，N通用路径可用，Go V17 | SRC/V；N仅参考 |
| T23 ANYDATA | — | 无独立映射；前提待核实 | 官方[ANYDATA][O10]仅PL，不允许持久表列S02；N目标缺SYS.ANYDATA为B10；PL writer/描述归U03，不把全部ANYDATA列为确定缺功能 | S02禁持久列；B10目标缺包；PL U03 | SRC/U；N仅参考 |
| T24 SPARSEVECTOR | 目标语法/wire待核实 | — | 官方[向量文档][O11]示例是无维数SPARSEVECTOR，自V4.3.5 BP2起支持；N的SPARSEVECTOR(3)1064不证明类型不支持，归U05，尚不确认需专用codec | 官方有支持；错误DDL不能证伪，U05 | SRC/U |
| T25 MySQL SQL ARRAY | 通用string路径已有，专项待验 | — | 官方[ARRAY][O12]；N的ARRAY(INT)为CHAR/String，归V15；Go Array是Oracle collection而非此类型；默认值是官方限制S03 | 官方有支持；S03禁默认值；Go V15 | SRC/V；N仅参考 |
| T26 RoaringBitmap | 通用bytes/SQL函数路径已有，专项待验 | — | 官方[位图][O13]；N为BLOB/bytes结果，rb_build_varbinary等函数写入可用，归V16；直接bytes绑定的1235是指定形状B11，不要求codec绕过 | 函数/bytes可用；直接bytes指定绑定B11 | SRC/V；N仅参考 |

## 事务、协议、元数据与交付

| ID / 能力 | Go客户端能力与边界 | 服务端/部署判定 | 入口与证据 |
| --- | --- | --- | --- |
| X01 本地事务、隔离级别、只读、savepoint | 双模式有实现；Oracle 不接受 RepeatableRead、ReadOnly+ReadCommitted 等非法组合；DDL 隐式提交等由服务端决定 | 模式/隔离级别由数据库语义决定 | [事务](../internal/control/transaction.go)、[保存点](../internal/control/savepoint.go)；SRC/L/H20/V |
| X02 环境事务、分布式协调器 | Go 不复制 TransactionScope；不内置持久化全局事务协调器，应用负责决策/日志/恢复 | X02应用协调器职责，不是服务器无事务 | [XA API](../xa.go)；接口差异/排除，不计普通本地事务缺失 |
| X03 XA start/end/prepare/commit/rollback/recover | 双模式有实现，MySQL XA SQL / Oracle DBMS_XA；固定专用会话；Forget、RM timeout 不伪造支持，join/suspend 受目标限制 | 正常XA H20；JOIN/SUSPEND指定拒绝B01 | [XA 实现](../internal/xa/manager.go)；SRC/L/H20/V |
| X04 unknown outcome 与连接淘汰 | 发送后的故障不自动重放；写入/提交未知保留分类，旧资源不能进入新租约；不等于已经证明真实节点崩溃恢复 | 真实故障恢复V05；客户端不重放 | [错误](../dberrors/errors.go)、[XA](../internal/xa)；SRC/L/H20（正常与指定故障子集）/V |
| P01 标准包、EOF、逻辑包边界 | 双模式有实现，校验序号/长度/截断，资源与结果预算有界 | 标准协议已用；不是所有畸形路径实库验收 | [wire](../internal/wire/packet.go)、[command](../internal/command)；SRC/L/H20 |
| P02 标准 zlib 压缩 | 依真实协商，auto 可回退；有帧编码/解码及 Java 互操作证据，不等于真实服务已使用压缩 | 历史端点未提供压缩ENV02，不是产品不支持 | [compress](../internal/wire/compress.go)、[互操作用例](../internal/wire/compression_java_test.go)；SRC/L/H20（回退）/V |
| P03 OB20、CRC、proxy checksum、新 metadata | 有实现，各能力分别协商；OB20 CRC、proxy checksum、SQL checksum 是不同能力 | 真实协商端点ENV02；常量不等于部署通过 | [OB20](../internal/wire/ob20.go)、[协议配置](../config/protocol.go)；SRC/L/V |
| P04 Oracle PrepareExecute、SQL checksum | 受限：PrepareExecute 依真实版本 ≥2.2.76，普通执行 SQL checksum 依 ≥4.1.2；相关开关显式开启，不绕过版本/模式检查 | 真实版本/能力门槛，不以配置覆盖 | [执行选项](../config/execution_options.go)、[native statement](../internal/statement/native.go)；SRC/L/H20/H24/V |
| P05 Full Link Trace | 有协商/extra-info/采样控制/trace-span 信息子集；需 OB20 与目标支持；客户端事件不是服务器 trace 或 collector 验收 | 服务端/代理FLT与collector前提ENV02 | [trace](../internal/diagnostic/trace.go)、[wire trace](../internal/wire/trace.go)；SRC/L/V |
| P06 压缩OB20、proxy reroute | 当前客户端/协议范围排除X03；Java压缩长度拒绝只证明该Java reader的边界，不能据此断言OceanBase产品整体不支持 | X03当前范围，服务端普遍结论未建立 | [兼容性条件](go-driver-compatibility-matrix.md)；SRC/U |
| M01 数据库、列、键、索引、权限、过程、UDT 元数据 | 双模式目录计划有实现，Oracle UDT/同义词与 MySQL 目录差异保留；不存在的 Oracle catalog 返回空；权限错误不伪造为空成功 | 权限A01/目录参考B12/元数据V07 | [Metadata API](../metadata.go)、[catalog](../internal/catalog)；SRC/L/H20（子集）/V |
| M02 日志、慢请求、包/请求诊断 | slog 注入和有界 callback；不记录原始 SQL、参数、凭据和包体；Rows 时长包含消费时间 | 客户端诊断；服务器trace另见P05 | [诊断配置](../config/diagnostic.go)、[诊断实现](../internal/diagnostic)；SRC/L/V（负载） |
| M03 池指标、OpenTelemetry 接入 | `sql.DB.Stats` 有池统计，callback 可供应用接入观测；无内置 OTel exporter、.NET Activity/Meter 或 Java JMX 等价承诺 | 标准库/应用观测，不是服务器协议能力 | [池示例](../pool_example_test.go)、[诊断配置](../config/diagnostic.go)；标准库/应用适配，非协议缺失 |
| R01 Go 版本、平台、CI、性能/长稳 | Go 1.26+；已有 Windows 本地及历史 hosted 检查记录，当前所有 OS/tag/真实部署组合尚未全验；本地 peer benchmark 不代表实库吞吐或长期稳定性 | 按Go/OS/tag/实际服务端组合验V11 | [go.mod](../go.mod)、[历史发布检查](release-validation-2026-10-20.md)、[benchmarks](go-driver-benchmarks.md)；H20/V |
| R02 JDBC/ADO.NET 专属 API 与容器集成 | 不复制 JDBC 类、ADO.NET factory/DI、DataAdapter、CommandBuilder、DbBatchReader；Go 用 sql.DB/Conn/Tx、Rows、ExecBatch 等自身契约 | 语言生态差异X04 | [用法](go-driver-usage.md)；生态接口差异 |
| R03 原生数据库/运维产品功能 | 不实现 Oracle OCI/Oracle Net、binlog 复制、备份恢复管理器、Aurora/Galera 控制面；可执行目标支持的 SQL 不等于提供专用产品 API | 职责边界X05，不声明数据库无相关SQL | 排除 |

## 依据与维护

能力状态以 Go 源码为准；细粒度版本/模式条件继续由[兼容性矩阵](go-driver-compatibility-matrix.md)维护，配置迁移见[选项映射](go-driver-options-mapping.md)。本表不沿用开发任务数计算“全功能完成率”，也不将范围排除/服务端限制计入客户端缺失数。

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
