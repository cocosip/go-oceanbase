# Go 驱动功能缺失与限制矩阵

更新日期：2026-11-04（新增第三套直连目标实库复验：全量集成测试通过、`obtest` 70 场景 108/0/5/27）。已重新核对[能力矩阵](go-driver-capabilities.md)、Go 实现、官方文档及参考 SDK 的成功和失败记录，并使用指定 OceanBase 4.4.2.2 直连实库复验 G 项；第二轮独立直连 4.4.2.2 目标的 79/0/3/14 工具结果、全量集成测试与 Oracle ChangeUser-/代理身份结论见[第二套目标复验](live-validation-2026-11-03-second-target.md)，第三套目标的 108/0/5/27 结果与新场景实库证据见[第三套目标复验](live-validation-2026-11-04-third-target.md)。**客户端缺实现、客户端范围限制、服务端明确不支持、指定目标拒绝、权限不足、环境未具备、尚未验证、依据不足**分别列出，不合并计算“缺失功能数”。

**范围**：本矩阵只描述 **Go 驱动自身**缺失的能力；不包含 Java/JDBC 兼容（JDBC 类层次、Java 选项名映射、JDBC 转义、JDBC 类 API）。**能力定义依据**只有 OceanBase 官方文档与实库行为、Go 标准库契约和本项目源码；JDBC 及其他语言客户端只作失败形状旁证，不能作为能力或协议依据。

## 分类规则

| 分类 | 充分依据 | 处理方式 |
| --- | --- | --- |
| 客户端缺实现 G | Go 无对应路径；官方支持或参考 SDK 对明确形状已有成功依据 | 只列该子集，不能外推所有方向/类型/部署 |
| 客户端范围限制 C | Go 主动拒绝/降级、缺自动化，但已有路径能完成部分工作 | 可选扩展，不把全部限制视为 bug |
| 服务端明确不支持 S | 官方明确排除某类型或使用形态 | 不进入客户端待办；限所引用文档范围 |
| 指定目标拒绝 B | 确切 SQL/方向/wire 到达服务端后有失败或不可辨识结果记录 | 保留版本/模式/形状/来源，不扩大为整个产品不支持 |
| 权限不足 A | 缺 grant、身份或创建权限 | 补测试授权前提，不修改 codec 绕过权限 |
| 环境未具备 ENV | 缺 TLS/ODP/KDC/平台等实际条件 | 不等于服务器产品不支持或客户端未实现 |
| 尚未验证 V | 有实现/通用映射，但缺 Go 专项证据 | 先验收；未失败前不列缺实现 |
| 依据不足 U | 正确语法、成功 wire 或错误归因未确定 | 继续调查，暂不归客户端或服务端 |
| 范围排除 X | 安全/职责边界、协议依据不足或生态差异 | 不进入当前开发积压，不伪称服务端不支持 |

两个维度独立：Go 没有 writer 与目标拒绝某操作可同时成立，但后者不能作为盲目开发理由。同名类型、错误码、单个 SDK 前置拒绝均不能证明全部服务端形态不支持。没有专用 Go 类型/codec，也不等于 string/bytes 通用路径不能完成工作。

**来源口径**：H20/H24 为 Go 历史实库报告；H03 为 [2026-11-03 Go 直连实库复验](live-validation-2026-11-03.md)。N 为其他语言客户端（.NET/Java）文档或用例中的观察记录，仅作旁证，不作为能力或协议依据；仅标 N 的服务端失败形状本次没有用 Go 重跑。N 可证明某个子集可实现或限定服务端前提，不能记作 Go 实测通过/失败。索引见文末。

## 已确认的客户端实现差距

The G01/G03/G04/G05a/G07a/G07b implementation subsets have direct Go database evidence. Java Connector/J option parity, its class hierarchy and JDBC escapes are explicitly out of scope, so they are not counted as missing features. U06 now adds seven strict charset mappings, with the SWE7 identity boundary documented below; U08 now passes finite long-NUL query and storage checks. Native RECORD/ANYDATA binding (U02/U03) and U01 protocol shapes remain open. These results do not imply support for every type, direction, version or deployment. C/U/S/B/A/ENV/V/X retain their separate classifications. A second independently supplied direct 4.4.2.2 target re-ran the full integration suite and the acceptance tool (79 pass / 0 fail / 3 skip / 14 not applicable) on 2026-11-03, re-verifying the same G subsets and the B16/B18/B01/S01 rejection shapes. A third independently supplied direct 4.4.2.2 target re-ran the full integration suite and the 70-scenario acceptance tool on 2026-11-04: 108 pass / 0 fail / 5 skip / 27 not applicable, with the extended-type, geometry, SDO, UROWID, XMLTYPE, negative-scale NUMBER, legacy charset, utf16le, ENUM/SET, generated columns, CHAR padding, empty LOB, dictionary patterns, identity, index/routine/statement dictionaries, type families, view DML, BINARY_FLOAT, synonym and float scenarios passing live for the first time and B16/B17/B11/B01/S01 reproduced.

## 已补实现并完成指定实库验收

| ID / 关联能力 | 本次实现与本地/实库证据 | 服务端/验收边界 |
| --- | --- | --- |
| G01 / T19–T20 字符集 | 已补 SJIS/Big5/DEC8/TIS620/GB2312/EUCKR 严格编码、握手和结果 collation，UTF16LE 结果解码及列长度；`TestMySQLCharsets` 和 `TestMySQLUTF16LEResult` 在 MySQL 4.4.2.2 实库通过 | 证据覆盖列存储和 UTF16LE 结果子集；Oracle 指定会话初始化拒绝仍归 B04，未知新字符集仍归 U06；UTF16LE 不开放 SQL client |
| G03 / T12 显式 UROWID 参数 | 已补 `URowID`/`sql.Null[URowID]`/nil `*URowID`、Oracle type209 typed NULL、client CAST、OUT/INOUT 推导与显式 Type；`TestOracleURowID` 在 Oracle 4.4.2.2 实库通过 | type209 结果仍按 ROWID 元数据读取，未猜测 UROWID 结果名；批处理沿既有资格判定顺序回退 |
| G04 / T17 XMLTYPE String 结果 | ownerless/SYS XMLTYPE string/NULL text/binary 解码已有；`TestOracleXMLType` 六种执行配置的列/函数结果实库通过 | B14 静默丢行已规避：确切 4.4.2.2 关闭通用可选 combined 优化，使用独立 PREPARE/EXECUTE；OUT/RETURNING 必需路径保留，B05 独立 |
| G05a / T14 命名 LOB 内容输入 | 已补命名 flat/nested OBJECT 和 named collection 的 BLOB bytes/Raw、CLOB 严格 UTF-8 内容输入；`TestOracleNamedLOBContent` 的普通/combined、对象/嵌套/collection 路径在 Oracle 实库通过 | 不包含匿名 LOB 元素头、NCLOB、locator 回绑；B06 持久 BLOB 成员写列限制继续保留 |
| G07a / T15 named OBJECT text 结果 | 已补描述符驱动的 NUMBER、普通/national 字符、递归命名 OBJECT 文本解码及 UTF-8、NULL、身份/字段数/深度/节点/字节预算校验；`TestOracleNamedObjectText` 在 Oracle 实库通过 | 只支持已证明可解析的 named OBJECT 子集；RAW/DATE/TIMESTAMP/LOB/cursor/collection 描述符继续拒绝，B07/B08 继续独立保留 |
| G07b / Q05、T14 命名复杂构造输入 | 已补显式 PrepareClient 的命名 OBJECT/递归 OBJECT/named collection 构造器；`TestOracleClientConstructors`、复杂 OBJECT/collection PL 测试在 Oracle 实库通过；参数化 builtin descriptor 解析缺陷已修复 | 匿名类型、LOB/locator/cursor、集合内部 NULL OBJECT 子树仍拒绝；嵌套输入须用确切目录名称；不替代原生 binary TIMESTAMP/collection 头证据 |


## 客户端范围限制与可选扩展

| ID / 原关联 | 已有能力与限制 | 正确处理 |
| --- | --- | --- |
| C02 / 原 G02、Q04 prepare 回退 | 显式 PrepareClient 已有；`EmulateUnsupportedPstmts` 默认关闭，开启后独立 server PREPARE 的 1295 回退到 client 插值（执行路径与显式 `sql.Stmt` 两条入口） | 本地协议回归覆盖两条入口；2026-11-03 在 4.4.2.2 MySQL 实测 `CREATE/DROP PROCEDURE` 返回 1295：关闭时显式 prepare 保留 1295，开启后回退为 client 执行并真实建/删过程（`obtest prepare-fallback`）；其余 44 条语句及占位符变体无 1295（占位符变体为 1064），因此自动 prepare 回退（有参数时）在本目标不可达；不处理 combined、输出、流参数、其他错误或已执行响应，不重放业务 SQL |
| C08 / 原 G08、Q05/Q19 多语句 LongData | MySQL `PrepareClient` + `AllowMultiStatements` 支持有界一次性读入后字面量编码 | peer 及 `TestMySQLMultiStatementLongData` 实库通过：含引号、反斜杠、NUL、中文的长参数和第二结果；不扩展 Oracle PL/其他准备路径 |
| C09 / 原 G09、Q20 pipeline | [TryNative](../internal/batch/native.go)仅选限定 scalar DML；SELECT/复杂值/流/framed 等顺序执行 | 性能/组合范围差距，不是 SQL 不能执行；N 对应子集可供扩展依据，Oracle piece-data 另见 U04 |
| C10 / Q09 字段无限流式读取 | Rows 按行读取，普通字段/复杂值受消息与物化预算限制；大对象另有 LOB/TableBlob 流式 API | 这是 Go `database/sql` Rows 与协议边界，**不是服务端能力、也不是已确认客户端缺实现**：OceanBase wire 协议没有"逐列流式"原语，参考 SDK 的流式接口只针对 LOB locator 与游标按批取行（分别由 LOB/TableBlob 与 cursor API 覆盖）。因此不实现、也不自行定义契约；只有出现明确需求且有参考语义时才重新立项 |
| C11 / C17 Oracle warning 详情 | count 已有；automatic Oracle compilation-warning fixture now verifies positive count/preservation/clear in the acceptance tool; [ReadWarnings](../internal/control/warnings.go) only provides MySQL details | 客户端/参考接口范围；2026-11-03 探测：Oracle 模式 `SHOW WARNINGS`/`SHOW COUNT(*) WARNINGS` 返回 900/42000，`DBA_ERRORS` 返回 942/42S02，本目标没有可读明细入口（见[复验](native-reset-and-probe-validation-2026-11-03.md)）；不新增猜测式实现 |

## 官方明确不支持的形态

| ID / 能力 | 官方依据 | 对 Go 的结论 |
| --- | --- | --- |
| S01 / T16 LONG、LONG RAW、BFILE、NCLOB | [Oracle 类型表](go-driver-capabilities.md#依据与维护)明确不支持；H20 原生 NCLOB DDL 也返回902 | 不列客户端待补类型；现有 NCLOB 分支与元数据类型名不构成原生支持 |
| S02 / T23 ANYDATA 持久表列 | [ANYDATA 文档](go-driver-capabilities.md#依据与维护)适用于企业版 Oracle 模式，限定 PL，不能作表列/永久存储，含内嵌 LOB 的持久化也受限 | 不提出普通表列 codec；不能反推 PL ANYDATA 也被全面排除，见 B10/U03 |
| S03 / T25 SQL ARRAY 默认值 | [ARRAY 文档](go-driver-capabilities.md#依据与维护)不支持数组默认值 | DDL 语义限制，不增加驱动“数组默认值”功能 |

## 指定目标的服务端拒绝或返回形状限制

仅限记录中的 OceanBase **4.4.2.2** 指定模式/形状。H20 为 Go 历史实测；H03 为本次 Go 复验；N 为参考记录，**不是 Go 复现**。不外推其他版本或全部同名能力。

| ID / 能力 | 模式、来源与确切限制 | 必须分开的可用能力/边界 |
| --- | --- | --- |
| B01 / X03 XA JOIN/SUSPEND | MySQL，H20：指定重关联1398，另一客户端同样观察 | 普通XA/恢复独立保留，不是缺XA SQL |
| B02 / T14 nested-table持久列 | Oracle，H20：指定建列600/-4007，存储子句900 | 建类型、OBJECT、collection PL传输不能一起否定 |
| B03 / Q11 SELECT CURSOR列 | Oracle，H20：指定prepared形状902，text Go拒绝，另一客户端解码失败；N另有execute/FETCH 902/1002 | 过程REF CURSOR OUT独立；不扩大到所有cursor表达式 |
| B04 / T19 EUCKR/SJIS/Big5初始化 | Oracle，N：普通会话初始化900，另一客户端同交互失败 | MySQL相应编码、Oracle GB2312和指定national转换另有成功子集；不能所有用途一起归缺实现 |
| B05 / T17 XMLTYPE PL输出 | Oracle，N：指定OUT 600/0A000，INOUT 600/42000，参考客户端同错 | XMLTYPE列/函数String结果为G04；官方PL概述不能覆盖具体方向/版本失败 |
| B06 / T14 BLOB OBJECT成员写列 | Oracle，N：过程将flat/nested BLOB成员INSERT到持久BLOB列932/22000；参考客户端等效 wire replay 同错；其公开 writer 的前置失败本身不算服务端证据 | 同类成员通过PL GETLENGTH/SUBSTR/RAWTOHEX读取成功为G05a；拒绝写列不等于所有PL BLOB输入不可用 |
| B07 / T15 指定复杂text成员 | Oracle，N：RAW/DATE/TIMESTAMP形状重复RAW文本；BLOB/CLOB text缺可靠成员身份，参考客户端同形状无法解码 complex 对象 | NUMBER/字符/递归named OBJECT可解析子集为G07a；binary LOB另有路径 |
| B08 / T14 根NULL OBJECT身份 | Oracle，N：指定OBJECT/嵌套OBJECT外层NULL bit=false，参考客户端不报告外层 NULL | 不能把全NULL属性对象猜成根NULL；collection NULL、输入NULL另验 |
| B09 / T09 named timezone输入 | Oracle，N：指定非空地区名binary输入1882，强制参考客户端 server prepare 同错 | 数字偏移/text/LTZ为其他路径，不是无TZ类型 |
| B10 / T23 ANYDATA SQL 形状 | 历史 N 缺 SYS.ANYDATA 的判断不适用于当前目标；H03 字典存在，PL ConvertNumber/GetNumber 数字回读通过；指定 `SELECT SYS.ANYDATA.ConvertNumber(1) FROM DUAL` 返回 942/42S02 | 只记录此 SQL 形状拒绝；不称 PL 缺包。Go 原生 typed 参数 descriptor/writer 仍归 U03 |
| B11 / T26 Bitmap 直接 bytes 绑定 | MySQL，H03 `TestMySQLRoaringBitmap` 直接写 ROARINGBITMAP 列返回 1235/0A000，明确 skip | rb_from_string/rb_build_varbinary、BLOB/bytes 结果、空/NULL/uint64 最大元素均实库通过；不要求 codec 绕过拒绝 |
| B12 / M01 参数目录字符长度 | Oracle，H03 再次确认 ALL_ARGUMENTS.CHAR_LENGTH/CHAR_USED 查询 904/42S22；其他客户端同 SQL 同错误码（旁证） | 当前 Go 参数计划读 DATA_LENGTH，不查询这两列；不是当前查询故障，也不把字节长度当字符长度 |
| B13 / T08 TIME极限小数 | MySQL，N：指定permissive binary截整秒，strict text1292而binary仍截断成功 | 路径相关服务端语义记录，尚非Go复现缺陷；不静默截断/CAST来制造一致 |
| B14 / T17 + P04 XMLTYPE combined 结果形状 | Oracle 4.4.2.2，H03：原始 combined 响应一个行包后结束，遗漏 NULL 行且无错误 | 驱动已对确切版本禁用通用可选 combined 优化，所有六种配置返回完整两行，删除 B14 skip；必需 OUT/RETURNING 保持并实库通过。版本范围外未推断同故障 |
| B15 / anonymous TIMESTAMP collection INOUT | Oracle 4.4.2.2, current Go live fixture: anonymous element metadata has no accuracy; direct INOUT rounds `.123456789` to `.123457` | Named native collection and independent SQL constructor control retain nine digits; fixed named metadata preserves dictionary precision. Anonymous IN/OUT through a typed local variable is independently verified; see [evidence](complex-temporal-national-validation-2026-11-03.md) |
| B16 / native PL RECORD and ANYDATA OUT | The tested combined calls with NULL output placeholders return 600/0A000 (-4007) before typed output, confirmed by raw wire and public `sql.Out` regressions | PL-local RECORD/ANYDATA and RECORD metadata work. Other output binding contracts and native input shapes remain unverified |
| B17 / spatial reference 4326 | Oracle SDO point insert returns 13029/HY000; MySQL GIS functions return 3548/SR001; both reproduce with pure SQL controls | SRID 0 point/GIS round trips work; no driver conversion or dictionary modification is applied |
| B18 / anonymous LOB collection headers | Raw Oracle codes 210/211 with inline BLOB/CLOB member bodies return 600/HY000 | The same named collection payloads succeed and store independent expected content; rejection is specific to the tested anonymous headers |
| B19 / named-region session timezone | Oracle session initialization and independent `ALTER SESSION SET TIME_ZONE='America/New_York'` both return 1882/HY000 | Explicit -04:00/-05:00 LTZ/TZ storage/readback retains nine digits; positive named-region DST acceptance remains open |
| B20 / package OBJECT parameter owner | Raw `ALL_ARGUMENTS.TYPE_OWNER` for the tested package OBJECT parameter is a padded numeric identifier, confirmed independently of the public metadata API | Metadata preserves the raw field; TYPE_NAME and the actual ALL_TYPES owner are separately checked. The numeric token is not assumed to be a user ID or fabricated into a schema name |
| B21 / C09 D30 Oracle 代理重认证 | Oracle 4.4.2.2（第二套直连目标）：初始 `ProxyUser` 登录正确；同一会话 `ChangeUser` 携带相同 `ProxyUser` 返回成功，但 `CURRENT_USER`/`SESSION_USER` 变为认证代理账号、`PROXY_USER` 仍为代理账号，代理目标未被应用 | 初始代理登录、`ProxyUser` 空值清除与普通 ChangeUser 仍独立可用；客户端确实重传 `__ob_client_proxy_user_name`（代码路径与本地 peer 用例覆盖），不是"未发送"；驱动已对确切 4.4.2.2 在发送前返回 `ErrUnsupported`（`TestChangeUserRejectsProxyTargetOnIgnoringVersion`），`TestProxyUserSessions` 在该目标跳过重认证段；只记录该目标/形状，不推断其他版本 |
| B22 / V07 M01 Oracle 字典引用过滤 | Oracle 4.4.2.2 上带 `ReferencedSchemaPattern`/`ReferencedNamePattern` 的 `ALL_CONSTRAINTS` 四表连接接近或超过默认 10s `ob_query_timeout`（字面量约 9.7s，绑定值超时）；仅 k 侧过滤约 37ms | 驱动仍按契约下发引用过滤，不内联字面量、不改变查询语义；MySQL `information_schema` 的引用过滤已实库通过；Oracle 侧按目标优化器限制记录，测试在 Oracle 跳过该组合并保留正向 k 侧过滤。2026-11-04 恢复后复测：同一四表连接仅加 r 侧 `LIKE` 谓词时 22.8ms 返回，加入 k 侧 `LIKE` 则 4.3s~超时（10s），改为 k 侧等值谓词 5.3–7.4s，提示均为目标优化器计划不稳定，不是驱动可单方面修复的语义问题 |

## 权限、身份与环境不足

| ID / 关联能力 | 已记录的前提不足 | 分类边界 |
| --- | --- | --- |
| A01 / M01 Oracle VIEW/SYNONYM | 历史 H20/N 的权限记录不代表当前账号；H03 CREATE VIEW 成功，指定 CREATE SYNONYM ... FOR SYS.DUAL 返回 900/42000；用户表同义词、视图同义词与同义词链已实库验收：`TestOracleSynonymUsage`/`TestOracleSynonymChain` 创建、经同义词读写、`MetadataSynonyms`/`MetadataTables` 均通过（2026-11-03） | 当前 VIEW 权限阻塞已解除；SYNONYM 是确切语法/目标拒绝，不是 1031 权限证据；其他同义词用法仍待验 |
| A02 / C09/C12 另一身份/代理 | 2026-11-03 经授权用一次性第二身份实库验收：MySQL 成功路径+错误密码 1045、Oracle 成功路径+错误密码 600/42000 均通过，失败均弃用连接且池恢复。第二套目标复验：一次性 MySQL/Oracle 第二身份下 ChangeUser 与 `obtest` change-user 再次通过；经 SYS 授权创建一次性代理账号/`GRANT CONNECT THROUGH` 后，Oracle 初始 ProxyUser 登录通过（`CURRENT_USER`=目标、`PROXY_USER`=代理账号），但 `COM_CHANGE_USER` 未应用代理目标（B21） | MySQL 与 Oracle 的 ChangeUser 已验收；ProxyUser 初始登录已验收，代理重认证是目标侧限制；不修改业务账号权限来制造通过；见[身份验收](change-user-acceptance-2026-11-03.md)与[第二套目标复验](live-validation-2026-11-03-second-target.md) |
| ENV01 / C06 TLS/mTLS | H20及本次指定直连SSL未开 | 缺正向部署，不是OceanBase产品不支持TLS；本地证书peer不是实库通过 |
| ENV02 / P02-P05 protocol/trace | Current direct targets pass standard/auto, OB20/checksum, new metadata and proxy-checksum query subsets in both modes; required compression fails negotiation | Compression needs an offering endpoint; ODP/collector and broader framed workloads remain separate. See [fresh results](remaining-validation-2026-11-03.md) |
| ENV03 / C14–C15 ODP/多节点/OCP/TNS | H20仅直连单端点 | 缺真实代理/控制面/集群，单上游relay不证明HA |
| ENV04 / C08 plugin/KDC | 客户端分支存在，缺真实正负端点和逐插件服务端依据 | 不能列服务器已支持全部，也不能列明确不支持全部；逐项确认 |
| ENV05 / C05 平台原生传输 | 真实Linux VC、系统GSSAPI、服务IPC/ACL前提未全验；这里指**客户端**运行环境（Linux/macOS 客户端的 Unix socket/Shared Memory/OBProxy VC 与系统 GSSAPI），与服务端操作系统无关 | OS/tag/库/服务条件，不是普通SQL缺口 |

A01 与 ENV01 已结合本次部署复核；其他条目保留历史/参考边界，不声明本次已重新部署验证。

2026-11-03 环境确认：服务端目前无法提供上述 ENV 所需环境（TLS/mTLS、required compression、OBProxy/OCP/多节点、Linux/macOS 原生传输、命名时区 B19、托管 CI），也无法提供支持 U01–U03 原生 RECORD/ANYDATA/LOB 协议形状的更新版本目标。相关条目保持“环境未具备/指定目标拒绝”的已记录状态，不视为已验收或已关闭；后续具备环境时再按条目复验。换机续跑所需的环境变量、命令与一次性 fixture 见[交接说明](continuation-handoff.md)。

## 有路径但缺 Go 专项验收

| ID / 关联能力 | 已有路径 | 待确认的Go证据；不应误列的缺失 |
| --- | --- | --- |
| V01 / C06 TLS/mTLS | tls.Config、证书校验、正负peer | 实际CA/主机名/mTLS/轮换，前提ENV01；不是再写TLS |
| V02 / C05/C08 平台/认证 | transport/authentication与ABI/peer | 实际OS/runtime/plugin/KDC，前提ENV04/05；客户端名字不证明服务端支持 |
| V03 / C14–C15 路由/发现 | 地址选择、TNS/OCP、刷新/建连切换 | 实际租户身份/角色/硬过期/恢复，前提ENV03；已发业务不重放 |
| V04 / P02–P05 协议/trace | 帧、CRC、协商/trace子集 | 实际layer/双向帧/控制/collector，前提ENV02，三类checksum分开 |
| V05 / X03–X04 故障/XA | Both modes pass real local COMMIT receipt loss with independent committed effects; MySQL XA PREPARE/COMMIT receipt loss and explicit recovery pass; Oracle combined-PL XA PREPARE/COMMIT receipt loss now passes with a fixture that keeps the OUT row and drops the trailing native OK (`TestOracleXAReceiptLoss`, 2026-11-03) | Node/proxy/process crashes and durable coordinator restart remain open; a real complete receipt is consumed before EOF, not a simulated server acknowledgement |
| V06 / pool/endurance/performance | Four workers × 2000 rounds per mode on the second direct target: 8000 transactions each, 4000 committed, zero retained leases, pool_open=4/in_use=0, goroutines=4, 146s total; the acceptance tool still passes; 2026-11-04 the full 35-package test suite passed under `-race` with zero reports on Windows amd64 (Go 1.27.3, MinGW-w64 gcc 15.1.0, CGO_ENABLED=1) and five concurrency-heavy packages passed 3 consecutive race runs | Multi-minute runs do not prove long-term endurance; server handle trends, throughput and multi-node/proxy load remain unmeasured; the earlier `0xc0000139` startup failure is a host toolchain/DLL condition, not a driver limitation. Offline heap samples grow per scenario/sample but not per iteration (≈+330 objects per sample at both 150 and 1000 iterations), so the observation reflects fixture retention rather than a per-operation leak; it still does not establish zero growth |
| V07 / M01/Q07 元数据 | Live package overload identities, parameter/return directions, named type references and OBJECT/collection precision pass; restricted MySQL and Oracle identities see only their granted tables/columns/primary keys, while an unreadable schema returns zero rows without fabricated success (`TestMetadataRestrictedVisibility`, `OB_*_RESTRICTED_DSN`; Oracle owner-granted table, 2026-11-03); `ColumnPattern`, escaped/wildcard patterns, referenced FK filters (MySQL) and non-applicable/invalid filter rejection pass live (`TestMetadataCatalog`, 2026-11-03); Oracle identity columns are now reported as auto-increment through their `SEQUENCE.NEXTVAL` default while ordinary and defaulted columns stay unset (`oracle-identity`, 2026-11-04) | Oracle referenced-pattern dictionary joins exceed the default query timeout (B22); remaining reference/filter combinations stay open; package parameter TYPE_OWNER has raw numeric server shape B20; A01/B12 remain separate. Privilege dictionaries expose only explicit grants (Oracle `ALL_TAB_PRIVS`/`ALL_COL_PRIVS`; MySQL `information_schema.TABLE_PRIVILEGES`/`COLUMN_PRIVILEGES`): an object owner's implicit privileges and MySQL database-level grants are absent by server semantics, so zero rows for self-owned objects is not a driver defect and the driver does not fabricate rows. The generated SQL is pinned by `TestOraclePrivilegeDictionaryPlan`/`TestMySQLPrivilegeDictionaryPlan` because OceanBase's Oracle-mode `ALL_TAB_PRIVS` exposes `TABLE_SCHEMA` (unlike Oracle's `OWNER`), while `ALL_COL_PRIVS` exposes `OWNER`; the OceanBase `ALL_COL_PRIVS` doc page misspells the privilege column as `PRIVILLEGE` but the `USER_COL_PRIVS`/`DBA_COL_PRIVS` pages and the pinned projection use `PRIVILEGE`. All other projections are pinned by `TestOracleDictionaryShapes`/`TestMySQLDictionaryShapes` after the same doc audit; OceanBase's `ALL_TYPE_ATTRS` and `ALL_COLL_TYPES` expose `NUMBER_PRECISION` (not Oracle's `PRECISION`) and `ALL_COLL_TYPES` exposes `COLL_TYPE`, which the projections already use |
| V08 / T08–T11 时间/编码 | Both prepare modes pass MySQL zero-date SQL_MODE boundaries and Oracle LTZ/TZ nine-digit independent storage around DST dates under numeric offsets | Named-region initialization and pure SQL reject with 1882 (B19), so positive region/DST deployment acceptance remains open; B04/B09 remain separate |
| V09 / Q10–Q11/T13–T15 资源组合 | Cursor/LOB/OBJECT/OUT subsets pass; `TestCursorEarlyCloseTransactions` closes a forward cursor before draining inside a transaction and verifies commit/rollback visibility from another pinned session plus session reuse (both modes, 2026-11-03); `TestCursorCancelOnLockedRow` cancels a cursor blocked on another session's row lock, evicts the session and reborrows a working replacement (2026-11-03); `TestResultBudgetLimits` rejects a 4KB LOB over `MaxLOBBytes=1KB` and an 8KB row over `MaxMessageBytes=4KB`, evicting the session while default limits still read the value (2026-11-03); `TestResultTailLoss` drops the final rowset EOF of a text SELECT, returns an error instead of a value and evicts the session (2026-11-03) | B02/B03/B06/B07/B08 remain per-shape exclusions |
| V10 / C09/C11–C13/Q18/Q22 会话 | Both modes pass the automatic locked-row network-read-timeout fixture, rejected reuse of the old pinned session and replacement connection; `TestBorrowerSessionIsolation` verifies uncommitted/rolled-back invisibility, lock-wait blocking with fresh-connection retry and MySQL session-variable isolation (2026-11-03); `TestTransactionInterruption` cancels a transaction blocked on another session's lock, evicts the session and verifies its earlier insert is rolled back (2026-11-03); `TestChangeUserGuardSessions` rejects ChangeUser inside a transaction and with a canceled context without changing identity (2026-11-03); `TestChangeUserResourceInvalidation` opens a cursor before reauthentication, rejects stale cursor use with `ErrResource`, keeps the new identity usable and restores the original pool identity (2026-11-03); `TestDMLReceiptLoss` drops an autocommit INSERT receipt, reports an error, evicts the session and shows the server-applied row (2026-11-03) | Remaining identity/interruption/partial-effect combinations stay open; A02 is separate |
| V11 / platform/version/CI | Current Windows full suites pass on Go 1.26.0 and 1.27.2; the 2026-11-03 matrix also executed the full Windows/386 suite and cross-built linux/amd64, linux/arm64 and darwin/arm64 with integration and gssapi tags, plus the CI tag vet set; 2026-11-04 re-ran the whole unit suite, the Windows/386 suite, the integration-tag suite without DSNs, the three cross-build targets and the tagged vet set on Go 1.27.3 | Native Linux/macOS runtime and hosted CI acceptance remain open; no WSL/Docker runtime available locally, and the operator confirmed on 2026-11-03 that these environments cannot be provided |
| V13 / 原 G10、T05–T07 JSON/BIT/YEAR/GEOMETRY | `TestMySQLExtendedTypes` server/client、text/参数查询、NULL 实库通过 | JSON string、BIT(8) 整数、YEAR、GEOMETRY WKT 函数输入及完整 SRID/WKB bytes 回读/回绑已验；不声称所有几何类型/转换均通过；U07 指定子集关闭 |
| V14 / VECTOR | Server/client live boundaries cover short/long dimensions, NaN, overflow and extreme finite values in the 42-case extended suite | Exact target normalization/rejection is retained; independent storage and pinned-session recovery pass. No new dedicated codec needed |
| V15 / SQL ARRAY | Server/client live empty/NULL-element arrays, INT extrema/overflow, malformed elements and nested/empty-inner arrays covered | Tested empty outer nested array returns 7602; independent no-effect checks and pinned-session recovery pass; default-value S03 is separate |
| V16 / RoaringBitmap | Existing bytes/functions/NULL/empty/uint64 cases plus duplicate/negative normalization, invalid strings, overflow and malformed byte payloads pass | Direct raw bytes B11 remains a skip; negative function input normalizes to 4294967295 on this target, not a driver conversion |
| V17 / SDO_GEOMETRY | Polygon plus native point X/Y/NULL Z and non-NULL SRID 0 pass binary and independent scalar checks | SRID 4326 fails with 13029 in both native and SQL controls (B17); other spatial functions remain workload-specific |

2026-11-04：验收工具新增 MySQL 场景 `extended-types`（VECTOR/SPARSEVECTOR/ARRAY/ROARINGBITMAP 的规范文本、NULL、元数据、拒绝与归一化）、`geometry`（SRID-0 文本/字节往返与缺 SRS 拒绝）和 Oracle 场景 `oracle-spatial`（SDO_GEOMETRY 字面量、成员访问、OBJECT/集合解码与 SRID 拒绝）。三者均先通过离线脚本化工作流用例，随后在第三套直连 4.4.2.2 目标上**首次实库运行即通过**，因此可作为 V13–V17 的工具回归基线（仍不替代全组合验收）。

## 依据不足，暂不归因

<a id="u-series-status"></a>

This table separates confirmed client boundaries from missing protocol or deployment evidence. U04 is C09; U05/U07 have verified subsets. U06 mappings are implemented, with a SWE7 authentication constraint; U08 has finite-size live evidence. U02/U03 still need native binding implementation and successful wire evidence; available server packages and directories no longer block that investigation. U01 remains open.

| ID / 原关联 | 尚不能下的结论 | 本次已确认的客户端/协议边界 | 关闭或升级条件 |
| --- | --- | --- | --- |
| U01 / native temporal/national/LOB collections | TIMESTAMP/NCHAR/NVARCHAR2 input and headers are implemented; LOB/locator and NCLOB remain separate | `TestOracleComplexTemporalNational`: 15 fixture configurations, zero failures/skips; named/anonymous IN/OUT, NULL/empty, mixed charsets, named nanosecond INOUT and nested OBJECT pass. Complex national input charset corruption fixed | Anonymous direct TIMESTAMP INOUT defaults to six-digit accuracy on this target; named types retain nine. See [focused evidence](complex-temporal-national-validation-2026-11-03.md). Raw anonymous BLOB/CLOB headers 210/211 return 600/HY000 while named forms succeed (B18); locator input still lacks a successful writer contract |
| U02 / PL-only RECORD | PL-local RECORD and member directory work; native OUT returns 600/0A000 (-4007) | Five conventional raw metadata layouts returned 600/HY000; no successful native input contract is established. Current OUT rejection is B16, not absence of PL RECORD | A supported native wire/version is required before adding a descriptor/writer; no scalar SQL wrapper is called native support |
| U03 / ANYDATA and locator/cursor inputs | PL ANYDATA number operations work; native ANYDATA OUT returns 600/0A000 (-4007) | SYS.ANYDATA is a distinct ANYDATA typecode with no ordinary OBJECT attributes; no successful typed input/locator writer contract yet | B16 is the exact OUT rejection; S02 persistent-column and X01 cursor-IN exclusions remain separate. See [fresh evidence](remaining-validation-2026-11-03.md) |
| U04 / C09 Oracle piece-data 流水化 | 关闭为 C09 的已知顺序协议范围，不列当前缺陷 | Go 每片等待 OK，另一客户端同样逐片 ACK；framed 流不走 pipeline | 只有新增无 ACK 协议成功依据才重开，不盲写无 ACK 发送 |
| U05 / T24 SPARSEVECTOR | 基础 string 子集已验，撤销“正确语法/wire 未知”状态 | `TestMySQLExtendedTypes` 无维数 SPARSEVECTOR，`{1:1,3:2}` 存储/回读/NULL、CHAR/string metadata，server/client 与 text/参数路径通过 | 高维/非法输入/函数组合继续作为专项覆盖，不需要先新增专用 codec |
| U06 / G01 additional charsets | Seven strict mappings implemented; tested MySQL subset complete | UJIS/EUCJPMS/CP932/CP850/HP8/MACROMAN/SWE7 encoders, decoders, handshake IDs and 14 result collation IDs; independent full mapping hashes; six connection charsets and all seven column/result charsets passed server/client live tests | SWE7 cannot encode ASCII `@`, so the supplied tenant-qualified identity is safely rejected for a SWE7 connection; use UTF8 for that identity and SWE7 columns/results. Other versions and Oracle initialization remain separate; see [focused evidence](charset-nul-validation-2026-11-03.md) |
| U07 / 原 G10 native 参数码 | V13 指定子集关闭：真实列已接受通用 string/bytes/整数 | JSON/BIT/YEAR/GEOMETRY server/client 存储回读通过，GEOMETRY bytes 回绑通过 | 未发现必须增加 type245/16/13/255 专用 writer 的依据；以后遇确切通用路径失败再立项 |
| U08 / Oracle long NUL scripts | Confirmed concatenation failure fixed; finite tested subset complete | Client literals use collision-free `REPLACE(...,marker,CHR(0))` with encoded-byte budgets. At 12003/98304/196608/1048576 bytes, single/double SELECTs pass full result consumption and connection reuse; NUL and equal-length control CLOB writes pass independent length/content checks (1 MiB via boundary checks) | Old concatenation failed at 98304/196608 bytes with 1489/HY000. The client-literal NUL boundary is exactly 1 MiB: 1048576 bytes pass, 1048577+ returns 600/-5098 (VARCHAR2 literal limit), while server preparation passed 3 MiB in the probe. Larger statements, other versions and deployments remain unverified; no truncation, replay or unlimited-size promise. See [focused evidence](charset-nul-validation-2026-11-03.md) |

## 明确排除与生态差异

| ID | 范围决定；不声明服务器不支持 |
| --- | --- |
| X01 | REF CURSOR IN/INOUT：无参考writer契约，当前不提供；与已实现的OUT分开 |
| X02 | 自动SQL/事务重放、透明迁移、内置持久XA协调器：安全/职责边界，应用显式恢复 |
| X03 | MariaDB bulk、Aurora/Galera、压缩OB20、proxy reroute：目标协议/产品范围依据不足，不从枚举或继承代码推导；参考客户端拒绝压缩 OB20 仅说明该客户端边界 |
| X04 | 其它生态的类/工厂/容器集成（DataSource 风格工厂、命令构建器、分布式事务包装、JMX、keystore、自建第二连接池）：生态/标准库/产品选择，不逐个算缺失；Go 不复制 JDBC 类层次、选项名或 JDBC 转义 |
| X05 | Oracle Net/OCI、备份复制管理、优化器与索引算法：非本驱动职责，SQL可调用不另计SDK功能 |

## 证据索引与本次复核

- 官方链接见[能力矩阵依据](go-driver-capabilities.md#依据与维护)；本次重新读取MySQL字符集、Oracle类型、XMLTYPE、ANYDATA、向量、ARRAY相关正文。
- H20：[Go发布实库报告](release-validation-2026-10-20.md)；H24：[Go浮点专项](binary-double-live-validation-2026-10-24.md)。只引用明确子集，历史成功不是本次重跑。
- H03：[2026-11-03 Go直连复验](live-validation-2026-11-03.md)：G01/G03/G04/G05a/G07a/G07b、B14 规避、SDO 修复、MySQL 扩展类型、PL 前提及本地检查；不包括所有代理/平台组合。
- H03R：[原生重置与剩余探测复验](native-reset-and-probe-validation-2026-11-03.md)：COM_RESET_CONNECTION 双模式成功但驱动保持淘汰、C02 44 条语句无 1295、Oracle warning 无明细入口、本轮 obtest/集成基线。
- H03I：[ChangeUser 第二身份验收](change-user-acceptance-2026-11-03.md)：经授权创建并删除一次性 MySQL 只读身份；ChangeUser 成功/错误密码 1045 实库通过，obtest 37/0/2/8；Oracle 仍 1031 无 CREATE USER。
- H03T：[第二套直连目标复验](live-validation-2026-11-03-second-target.md)：独立 4.4.2.2 双模式全量集成测试通过；obtest 79/0/3/14；一次性第二身份下 MySQL/Oracle ChangeUser 通过；Oracle 初始代理登录通过、`COM_CHANGE_USER` 未应用代理目标（B21）；双模式及 Oracle combined-PL XA 收据丢失通过；B16/B18/B01/S01 拒绝形状复现。
- H03U：[第三套直连目标复验](live-validation-2026-11-04-third-target.md)：独立 4.4.2.2 双模式全量集成测试通过；obtest 70 场景 108/0/5/27（MySQL 52/0/3/15；Oracle 56/0/2/12）；extended-types/geometry/oracle-spatial/oracle-urowid/oracle-xml 及双模式 floats 二十二个新场景首次实库通过；B16（RECORD/ANYDATA 600/0A000）、B17（SDO 13029）、B11（RoaringBitmap 直传 bytes 拒绝）、B01（MySQL XA 1398）、S01（Oracle NCLOB 902）复现；无第二身份与代理身份，ChangeUser 跳过。
- N：obconnector-dotnet/docs/obconnector-dotnet-completeness.md及obconnector-dotnet-missing-features-roadmap.md的D-02d1～d3、D-04a1/a2/e、D-05、D-07、D-08m、D-09、D-12a/b/c、目录/RECORD段落；本次核对ComplexLobInputTests.cs、XmlProbeTests.cs、VectorArrayStringWireTests.cs的实际用例形状，不记作Go实测。
- 参考 SDK 的 Java 侧仅用于核对协议形状（成员 writer、客户端拒绝、OB20 压缩长度拒绝），不作为 Go API、选项名或 JDBC 兼容目标。

本轮已连接双模式 OceanBase 4.4.2.2，逐项复验 G 表中的明确子集；第 25/26 轮新增 `views`（已实库通过）、`identifier-length` 与 `topology-sources` 两个已注册场景；三者均在 2026-11-04 目标恢复后实库通过（`identifier-length` 首跑暴露的是场景大小写假设错误，已修正场景并补离线夹具，驱动无改动）。第二轮独立直连目标再次通过全量集成与 79/0/3/14 工具结果，并新增 B21（代理重认证目标侧未生效）；第三轮独立直连目标通过全量集成与 70 场景 108/0/5/27；目标恢复后 72 场景全量达成 **112 pass / 0 fail / 5 skip / 27 not applicable**，全量集成套件再次全部 `ok`，见[恢复后复验报告](live-validation-2026-11-04-recovery.md)。本地测试、静态检查和实库跳过项见 H03/H03T/H03U。V/U 尚未全部验收（TLS/代理/压缩/插件/Linux 客户端等环境项与第二身份仍缺），未重新执行的参考 SDK 失败形状继续仅标 N，不提升为 Go 实测。

上一版“15项功能差距”的汇总撤销：原G10–G14多数是通用路径验收/调查，G05/G06/G07按操作形状拆开，服务端/权限/环境也已拆开。后续G表按明确子集立项，C表按需求选择，V表补验收；S/B/A/ENV/U/X不直接转为实现待办。

Fresh continuation: [remaining validation and exact external prerequisites](remaining-validation-2026-11-03.md). This report preserves unavailable and rejected shapes; it does not declare the complete matrix passed.
## 2026-11-04 范围重载与后续推进

**范围重载**：Java Connector/J 的类层次、选项名映射、JDBC 转义与 JDBC 类 API 不再属于本驱动的能力或验收范围。已删除 `go-driver-options-mapping.md`（Java 选项矩阵）与 `obconnector-j-architecture-outline.md`（Java 结构分析），并删除实验性 `RewriteJDBCEscapes` API 与 `jdbc-escapes` 场景。能力矩阵 Q24 标为不适用；本矩阵不再把 Java 兼容列为缺失项。

**重载后的可推进项盘点**（按"是否需要可用数据库/新环境"分类）：

| 类别 | 条目 | 当前状态 | 可离线推进? |
| --- | --- | --- | --- |
| 客户端缺实现 G | G01/G03/G04/G05a/G07a/G07b | 已实现并经指定目标实库验收 | 无 |
| 客户端缺实现 G | U02/U03（原生 RECORD/ANYDATA） | 已实现方向被服务端 600/0A000 拒绝，缺成功 wire 形状 | 否（需目标/新版服务端） |
| 客户端缺实现 G | D28/D30/D31/D32 | 依赖平台（Shared Memory/VC、Linux）与全量验收 | 否（需 ENV05 或 Linux 运行时） |
| 客户端范围 C | C02/C08/C09/C10/C11 | 已知边界与可选扩展，非缺陷 | C10 是协议/标准库边界，服务端与参考 SDK 均无对应普通列流能力 |
| 服务端/目标 S/B/A | S01–S03、B01–B22、A01–A02 | 服务端拒绝/权限/已记录形状 | 否（需目标复验） |
| 环境 ENV | ENV01–ENV05 | TLS/mTLS、代理/多节点、压缩、插件/KDC、平台传输 | 否（需对应部署） |
| 待验证 V | V01–V17 剩余组合 | 需实库/平台证据 | 否（需目标） |

**结论（2026-11-04 恢复后更新）**：缺失矩阵重载后，**Go 侧没有可在离线环境实现的功能缺口**，剩余条目全部指向"新环境/新部署/第二身份"；不存在可自行定义契约的扩展项：C10 的普通列流式读取既非服务端能力，也无参考客户端对应接口，不实现、不定契约。目标恢复后已完成直连复验：72 场景全量 **112 pass / 0 fail / 5 skip / 27 not applicable**（MySQL 54/0/3/15，Oracle 58/0/2/12），全量集成套件全部 `ok`，`identifier-length`/`topology-sources` 两个新场景实库通过；`identifier-length` 的首跑失败是验收场景的大小写假设问题（MySQL 折叠表标识符），已修正场景并补离线夹具，驱动无缺陷。仍未满足的只剩环境类：TLS/mTLS、压缩、OBProxy/多节点/OCP、插件/KDC、客户端 Linux 平台传输、`change-user` 第二身份、显式授权的权限行与 B21 代理重认证。详见[恢复后复验报告](live-validation-2026-11-04-recovery.md)。后续按[待实库验证清单](pending-live-verification.md) §8 执行环境到位后的条目。