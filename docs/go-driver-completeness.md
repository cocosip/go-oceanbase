# Go 驱动功能缺失与限制矩阵

更新日期：2026-11-03。已重新核对[能力矩阵](go-driver-capabilities.md)、Go 实现、官方文档及 Java/.NET 的成功和失败记录，并使用指定 OceanBase 4.4.2.2 直连实库复验 G 项。**客户端缺实现、客户端范围限制、服务端明确不支持、指定目标拒绝、权限不足、环境未具备、尚未验证、依据不足**分别列出，不合并计算“缺失功能数”。

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

**来源口径**：H20/H24 为 Go 历史实库报告；H03 为 [2026-11-03 Go 直连实库复验](live-validation-2026-11-03.md)。N 为 .NET 文档和用例中的参考实测记录，必要时含 Java 对照；仅标 N 的服务端失败形状本次没有用 Go 重跑。N 可证明某个子集可实现或限定服务端前提，不能记作 Go 实测通过/失败。索引见文末。

## 已确认的客户端实现差距

The G01/G03/G04/G05a/G07a/G07b implementation subsets have direct Go database evidence. U06 now adds seven strict charset mappings, with the SWE7 identity boundary documented below; U08 now passes finite long-NUL query and storage checks. Native RECORD/ANYDATA binding (U02/U03) and U01 protocol shapes remain open. These results do not imply support for every type, direction, version or deployment. C/U/S/B/A/ENV/V/X retain their separate classifications.

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
| C02 / 原 G02、Q04 prepare 回退 | 显式 PrepareClient 已有；`EmulateUnsupportedPstmts` 默认关闭，开启后仅独立 server PREPARE 的 1295 回退到 client 插值 | [配置/DSN](../config/dsn.go) 与 [执行回退](../internal/execution/execute.go) 已有本地协议回归；不处理 combined、输出、流参数、其他错误或已执行响应，不重放业务 SQL；真实服务端 1295 覆盖仍待 N |
| C08 / 原 G08、Q05/Q19 多语句 LongData | MySQL `PrepareClient` + `AllowMultiStatements` 支持有界一次性读入后字面量编码 | peer 及 `TestMySQLMultiStatementLongData` 实库通过：含引号、反斜杠、NUL、中文的长参数和第二结果；不扩展 Oracle PL/其他准备路径 |
| C09 / 原 G09、Q20 pipeline | [TryNative](../internal/batch/native.go)仅选限定 scalar DML；SELECT/复杂值/流/framed 等顺序执行 | 性能/组合范围差距，不是 SQL 不能执行；N 对应子集可供扩展依据，Oracle piece-data 另见 U04 |
| C10 / Q09 字段无限流式读取 | Rows 按行读取，普通字段/复杂值受消息与物化预算限制；大对象另有 LOB/TableBlob 流式 API | 这是 Go `database/sql` Rows 与当前协议边界，不计为已确认客户端缺实现；如要新增普通列流 API，必须先定义独立的读取顺序、所有权、取消和消息分片契约 |
| C11 / C17 Oracle warning 详情 | count 已有，[ReadWarnings](../internal/control/warnings.go)只给 MySQL 详情 | 客户端/参考接口范围；当前依据不能证明“数据库没有任何 Oracle warning 详情能力” |

## 官方明确不支持的形态

| ID / 能力 | 官方依据 | 对 Go 的结论 |
| --- | --- | --- |
| S01 / T16 LONG、LONG RAW、BFILE、NCLOB | [Oracle 类型表](go-driver-capabilities.md#依据与维护)明确不支持；H20 原生 NCLOB DDL 也返回902 | 不列客户端待补类型；现有 NCLOB 分支、Java NClob 别名/GetTypeInfo 名称不构成原生支持 |
| S02 / T23 ANYDATA 持久表列 | [ANYDATA 文档](go-driver-capabilities.md#依据与维护)适用于企业版 Oracle 模式，限定 PL，不能作表列/永久存储，含内嵌 LOB 的持久化也受限 | 不提出普通表列 codec；不能反推 PL ANYDATA 也被全面排除，见 B10/U03 |
| S03 / T25 SQL ARRAY 默认值 | [ARRAY 文档](go-driver-capabilities.md#依据与维护)不支持数组默认值 | DDL 语义限制，不增加驱动“数组默认值”功能 |

## 指定目标的服务端拒绝或返回形状限制

仅限记录中的 OceanBase **4.4.2.2** 指定模式/形状。H20 为 Go 历史实测；H03 为本次 Go 复验；N 为参考记录，**不是 Go 复现**。不外推其他版本或全部同名能力。

| ID / 能力 | 模式、来源与确切限制 | 必须分开的可用能力/边界 |
| --- | --- | --- |
| B01 / X03 XA JOIN/SUSPEND | MySQL，H20：指定重关联1398，Java同样复现 | 普通XA/恢复独立保留，不是缺XA SQL |
| B02 / T14 nested-table持久列 | Oracle，H20：指定建列600/-4007，存储子句900 | 建类型、OBJECT、collection PL传输不能一起否定 |
| B03 / Q11 SELECT CURSOR列 | Oracle，H20：指定prepared形状902，text Go拒绝/Java解码失败；N另有execute/FETCH 902/1002 | 过程REF CURSOR OUT独立；不扩大到所有cursor表达式 |
| B04 / T19 EUCKR/SJIS/Big5初始化 | Oracle，N：普通会话初始化900，Java同交互失败 | MySQL相应编码、Oracle GB2312和指定national转换另有成功子集；不能所有用途一起归缺实现 |
| B05 / T17 XMLTYPE PL输出 | Oracle，N：指定OUT 600/0A000，INOUT 600/42000，Java同错 | XMLTYPE列/函数String结果为G04；官方PL概述不能覆盖具体方向/版本失败 |
| B06 / T14 BLOB OBJECT成员写列 | Oracle，N：过程将flat/nested BLOB成员INSERT到持久BLOB列932/22000；Java等效wire replay同错。Java公开writer前置失败本身不算服务端证据 | 同类成员通过PL GETLENGTH/SUBSTR/RAWTOHEX读取成功为G05a；拒绝写列不等于所有PL BLOB输入不可用 |
| B07 / T15 指定复杂text成员 | Oracle，N：RAW/DATE/TIMESTAMP形状重复RAW文本；BLOB/CLOB text缺可靠成员身份，Java同形状不能正常解码complex对象 | NUMBER/字符/递归named OBJECT可解析子集为G07a；binary LOB另有路径 |
| B08 / T14 根NULL OBJECT身份 | Oracle，N：指定OBJECT/嵌套OBJECT外层NULL bit=false，Java不报告外层NULL | 不能把全NULL属性对象猜成根NULL；collection NULL、输入NULL另验 |
| B09 / T09 named timezone输入 | Oracle，N：指定非空地区名binary输入1882，强制Java server prepare同错 | 数字偏移/text/LTZ为其他路径，不是无TZ类型 |
| B10 / T23 ANYDATA SQL 形状 | 历史 N 缺 SYS.ANYDATA 的判断不适用于当前目标；H03 字典存在，PL ConvertNumber/GetNumber 数字回读通过；指定 `SELECT SYS.ANYDATA.ConvertNumber(1) FROM DUAL` 返回 942/42S02 | 只记录此 SQL 形状拒绝；不称 PL 缺包。Go 原生 typed 参数 descriptor/writer 仍归 U03 |
| B11 / T26 Bitmap 直接 bytes 绑定 | MySQL，H03 `TestMySQLRoaringBitmap` 直接写 ROARINGBITMAP 列返回 1235/0A000，明确 skip | rb_from_string/rb_build_varbinary、BLOB/bytes 结果、空/NULL/uint64 最大元素均实库通过；不要求 codec 绕过拒绝 |
| B12 / M01 参数目录字符长度 | Oracle，H03 再次确认 ALL_ARGUMENTS.CHAR_LENGTH/CHAR_USED 查询 904/42S22；N/Java 同 SQL 同码 | 当前 Go 参数计划读 DATA_LENGTH，不查询这两列；不是当前查询故障，也不把字节长度当字符长度 |
| B13 / T08 TIME极限小数 | MySQL，N：指定permissive binary截整秒，strict text1292而binary仍截断成功 | 路径相关服务端语义记录，尚非Go复现缺陷；不静默截断/CAST来制造一致 |
| B14 / T17 + P04 XMLTYPE combined 结果形状 | Oracle 4.4.2.2，H03：原始 combined 响应一个行包后结束，遗漏 NULL 行且无错误 | 驱动已对确切版本禁用通用可选 combined 优化，所有六种配置返回完整两行，删除 B14 skip；必需 OUT/RETURNING 保持并实库通过。版本范围外未推断同故障 |
| B15 / anonymous TIMESTAMP collection INOUT | Oracle 4.4.2.2, current Go live fixture: anonymous element metadata has no accuracy; direct INOUT rounds `.123456789` to `.123457` | Named native collection and independent SQL constructor control retain nine digits; fixed named metadata preserves dictionary precision. Anonymous IN/OUT through a typed local variable is independently verified; see [evidence](complex-temporal-national-validation-2026-11-03.md) |
| B16 / native PL RECORD and ANYDATA OUT | Current target returns 600/0A000 (-4007) before typed output, confirmed by raw wire and public `sql.Out` regressions | PL-local RECORD/ANYDATA and RECORD metadata work. This does not classify every native input shape unsupported |
| B17 / spatial reference 4326 | Oracle SDO point insert returns 13029/HY000; MySQL GIS functions return 3548/SR001; both reproduce with pure SQL controls | SRID 0 point/GIS round trips work; no driver conversion or dictionary modification is applied |
| B18 / anonymous LOB collection headers | Raw Oracle codes 210/211 with inline BLOB/CLOB member bodies return 600/HY000 | The same named collection payloads succeed and store independent expected content; rejection is specific to the tested anonymous headers |

## 权限、身份与环境不足

| ID / 关联能力 | 已记录的前提不足 | 分类边界 |
| --- | --- | --- |
| A01 / M01 Oracle VIEW/SYNONYM | 历史 H20/N 的权限记录不代表当前账号；H03 CREATE VIEW 成功，指定 CREATE SYNONYM ... FOR SYS.DUAL 返回 900/42000 | 当前 VIEW 权限阻塞已解除；SYNONYM 是确切语法/目标拒绝，不是 1031 权限证据；其他同义词用法仍待验 |
| A02 / C09/C12 另一身份/代理 | H20缺第二身份/代理grant，CREATE USER探针1031 | 缺授权fixture；保留ChangeUser/ProxyUser实现，不修改业务账号权限来制造通过 |
| ENV01 / C06 TLS/mTLS | H20及本次指定直连SSL未开 | 缺正向部署，不是OceanBase产品不支持TLS；本地证书peer不是实库通过 |
| ENV02 / P02-P05 protocol/trace | Current direct targets pass standard/auto, OB20/checksum, new metadata and proxy-checksum query subsets in both modes; required compression fails negotiation | Compression needs an offering endpoint; ODP/collector and broader framed workloads remain separate. See [fresh results](remaining-validation-2026-11-03.md) |
| ENV03 / C14–C15 ODP/多节点/OCP/TNS | H20仅直连单端点 | 缺真实代理/控制面/集群，单上游relay不证明HA |
| ENV04 / C08 plugin/KDC | 客户端分支存在，缺真实正负端点和逐插件服务端依据 | 不能列服务器已支持全部，也不能列明确不支持全部；逐项确认 |
| ENV05 / C05 平台原生传输 | 真实Linux VC、系统GSSAPI、服务IPC/ACL前提未全验 | OS/tag/库/服务条件，不是普通SQL缺口 |

A01 与 ENV01 已结合本次部署复核；其他条目保留历史/参考边界，不声明本次已重新部署验证。

## 有路径但缺 Go 专项验收

| ID / 关联能力 | 已有路径 | 待确认的Go证据；不应误列的缺失 |
| --- | --- | --- |
| V01 / C06 TLS/mTLS | tls.Config、证书校验、正负peer | 实际CA/主机名/mTLS/轮换，前提ENV01；不是再写TLS |
| V02 / C05/C08 平台/认证 | transport/authentication与ABI/peer | 实际OS/runtime/plugin/KDC，前提ENV04/05；客户端名字不证明服务端支持 |
| V03 / C14–C15 路由/发现 | 地址选择、TNS/OCP、刷新/建连切换 | 实际租户身份/角色/硬过期/恢复，前提ENV03；已发业务不重放 |
| V04 / P02–P05 协议/trace | 帧、CRC、协商/trace子集 | 实际layer/双向帧/控制/collector，前提ENV02，三类checksum分开 |
| V05 / X03–X04 故障/XA | 正常XA、unknown分类/故障peer | 回执丢失、节点/代理/进程崩溃、独立查账/持久恢复，不是缺XA API |
| V06 / pool/endurance/performance | Four workers x128 rounds per mode: 512 transactions each, 256 committed, zero retained leases; acceptance tool and affected race checks pass | Finite roughly seven-second runs do not prove endurance, server handle trends or throughput; retain real workload acceptance |
| V07 / M01/Q07 元数据 | 目录计划/历史子集 | 最小权限、overload、引用身份/precision等；A01/B12另列 |
| V08 / T08–T11 时间/编码 | 精确时间、national与已有charset | 零日期SQL_MODE、DST/LTZ/地区/存储；B04/B09不变成待写codec |
| V09 / Q10–Q11/T13–T15 资源组合 | cursor/LOB/OBJECT/OUT子集 | 提前Close/取消/尾包/预算/事务池代际/可见性；B02/B03/B06/B07/B08逐形状排除 |
| V10 / C09/C11–C13/Q18/Q22 会话 | 初始化、属性、读超时、ChangeUser/导入/取消 | 真实身份/中断码/部分效果/借用者隔离；A02另列 |
| V11 / platform/version/CI | Current Windows full suites pass on Go 1.26.0 and 1.27.2; native ABI fixtures/race checks and Linux amd64/arm64 plus Darwin arm64 cross-build pass | Native Linux/macOS runtime and hosted CI acceptance remain open; no WSL/Docker runtime available locally |
| V13 / 原 G10、T05–T07 JSON/BIT/YEAR/GEOMETRY | `TestMySQLExtendedTypes` server/client、text/参数查询、NULL 实库通过 | JSON string、BIT(8) 整数、YEAR、GEOMETRY WKT 函数输入及完整 SRID/WKB bytes 回读/回绑已验；不声称所有几何类型/转换均通过；U07 指定子集关闭 |
| V14 / VECTOR | Server/client live boundaries cover short/long dimensions, NaN, overflow and extreme finite values in the 42-case extended suite | Exact target normalization/rejection is retained; independent storage and pinned-session recovery pass. No new dedicated codec needed |
| V15 / SQL ARRAY | Server/client live empty/NULL-element arrays, INT extrema/overflow, malformed elements and nested/empty-inner arrays covered | Tested empty outer nested array returns 7602; independent no-effect checks and pinned-session recovery pass; default-value S03 is separate |
| V16 / RoaringBitmap | Existing bytes/functions/NULL/empty/uint64 cases plus duplicate/negative normalization, invalid strings, overflow and malformed byte payloads pass | Direct raw bytes B11 remains a skip; negative function input normalizes to 4294967295 on this target, not a driver conversion |
| V17 / SDO_GEOMETRY | Polygon plus native point X/Y/NULL Z and non-NULL SRID 0 pass binary and independent scalar checks | SRID 4326 fails with 13029 in both native and SQL controls (B17); other spatial functions remain workload-specific |

## 依据不足，暂不归因

<a id="u-series-status"></a>

This table separates confirmed client boundaries from missing protocol or deployment evidence. U04 is C09; U05/U07 have verified subsets. U06 mappings are implemented, with a SWE7 authentication constraint; U08 has finite-size live evidence. U02/U03 still need native binding implementation and successful wire evidence; available server packages and directories no longer block that investigation. U01 remains open.

| ID / 原关联 | 尚不能下的结论 | 本次已确认的客户端/协议边界 | 关闭或升级条件 |
| --- | --- | --- | --- |
| U01 / native temporal/national/LOB collections | TIMESTAMP/NCHAR/NVARCHAR2 input and headers are implemented; LOB/locator and NCLOB remain separate | `TestOracleComplexTemporalNational`: 15 fixture configurations, zero failures/skips; named/anonymous IN/OUT, NULL/empty, mixed charsets, named nanosecond INOUT and nested OBJECT pass. Complex national input charset corruption fixed | Anonymous direct TIMESTAMP INOUT defaults to six-digit accuracy on this target; named types retain nine. See [focused evidence](complex-temporal-national-validation-2026-11-03.md). Raw anonymous BLOB/CLOB headers 210/211 return 600/HY000 while named forms succeed (B18); locator input still lacks a successful writer contract |
| U02 / PL-only RECORD | PL-local RECORD and member directory work; native OUT returns 600/0A000 (-4007) | Five conventional raw metadata layouts returned 600/HY000; no successful native input contract is established. Current OUT rejection is B16, not absence of PL RECORD | A supported native wire/version is required before adding a descriptor/writer; no scalar SQL wrapper is called native support |
| U03 / ANYDATA and locator/cursor inputs | PL ANYDATA number operations work; native ANYDATA OUT returns 600/0A000 (-4007) | SYS.ANYDATA is a distinct ANYDATA typecode with no ordinary OBJECT attributes; no successful typed input/locator writer contract yet | B16 is the exact OUT rejection; S02 persistent-column and X01 cursor-IN exclusions remain separate. See [fresh evidence](remaining-validation-2026-11-03.md) |
| U04 / C09 Oracle piece-data 流水化 | 关闭为 C09 的已知顺序协议范围，不列当前缺陷 | Go 每片等待 OK，Java 同样逐片 ACK；framed 流不走 pipeline | 只有新增无 ACK 协议成功依据才重开，不盲写无 ACK 发送 |
| U05 / T24 SPARSEVECTOR | 基础 string 子集已验，撤销“正确语法/wire 未知”状态 | `TestMySQLExtendedTypes` 无维数 SPARSEVECTOR，`{1:1,3:2}` 存储/回读/NULL、CHAR/string metadata，server/client 与 text/参数路径通过 | 高维/非法输入/函数组合继续作为专项覆盖，不需要先新增专用 codec |
| U06 / G01 additional charsets | Seven strict mappings implemented; tested MySQL subset complete | UJIS/EUCJPMS/CP932/CP850/HP8/MACROMAN/SWE7 encoders, decoders, handshake IDs and 14 result collation IDs; independent full mapping hashes; six connection charsets and all seven column/result charsets passed server/client live tests | SWE7 cannot encode ASCII `@`, so the supplied tenant-qualified identity is safely rejected for a SWE7 connection; use UTF8 for that identity and SWE7 columns/results. Other versions and Oracle initialization remain separate; see [focused evidence](charset-nul-validation-2026-11-03.md) |
| U07 / 原 G10 native 参数码 | V13 指定子集关闭：真实列已接受通用 string/bytes/整数 | JSON/BIT/YEAR/GEOMETRY server/client 存储回读通过，GEOMETRY bytes 回绑通过 | 未发现必须增加 type245/16/13/255 专用 writer 的依据；以后遇确切通用路径失败再立项 |
| U08 / Oracle long NUL scripts | Confirmed concatenation failure fixed; finite tested subset complete | Client literals use collision-free `REPLACE(...,marker,CHR(0))` with encoded-byte budgets. At 12003/98304/196608 bytes, single/double SELECTs pass full result consumption and connection reuse; NUL and equal-length control CLOB writes pass independent length/content checks | Old concatenation failed at 98304/196608 bytes with 1489/HY000. Larger sizes, other versions and deployments remain unverified; no truncation, replay or unlimited-size promise. See [focused evidence](charset-nul-validation-2026-11-03.md) |

## 明确排除与生态差异

| ID | 范围决定；不声明服务器不支持 |
| --- | --- |
| X01 | REF CURSOR IN/INOUT：无参考writer契约，当前不提供；与已实现的OUT分开 |
| X02 | 自动SQL/事务重放、透明迁移、内置持久XA协调器：安全/职责边界，应用显式恢复 |
| X03 | MariaDB bulk、Aurora/Galera、压缩OB20、proxy reroute：目标协议/产品范围依据不足，不从枚举或继承代码推导；Java拒绝压缩OB20仅说明该客户端边界 |
| X04 | JDBC/ADO.NET类、DataAdapter/CommandBuilder/TransactionScope、JMX、Java keystore、自建第二连接池：生态/标准库/产品选择，不逐个算缺失 |
| X05 | Oracle Net/OCI、备份复制管理、优化器与索引算法：非本驱动职责，SQL可调用不另计SDK功能 |

## 证据索引与本次复核

- 官方链接见[能力矩阵依据](go-driver-capabilities.md#依据与维护)；本次重新读取MySQL字符集、Oracle类型、XMLTYPE、ANYDATA、向量、ARRAY相关正文。
- H20：[Go发布实库报告](release-validation-2026-10-20.md)；H24：[Go浮点专项](binary-double-live-validation-2026-10-24.md)。只引用明确子集，历史成功不是本次重跑。
- H03：[2026-11-03 Go直连复验](live-validation-2026-11-03.md)：G01/G03/G04/G05a/G07a/G07b、B14 规避、SDO 修复、MySQL 扩展类型、PL 前提及本地检查；不包括所有代理/平台组合。
- N：obconnector-dotnet/docs/obconnector-dotnet-completeness.md及obconnector-dotnet-missing-features-roadmap.md的D-02d1～d3、D-04a1/a2/e、D-05、D-07、D-08m、D-09、D-12a/b/c、目录/RECORD段落；本次核对ComplexLobInputTests.cs、XmlProbeTests.cs、VectorArrayStringWireTests.cs的实际用例形状，不记作Go实测。
- Java：核对ComplexUtil.storeComplexAttrData成员writer、createSQLXML客户端拒绝和OB20 reader压缩长度拒绝；没有把Java拒绝当服务端不支持证明。

本轮已连接双模式 OceanBase 4.4.2.2，逐项复验 G 表中的明确子集；本地测试、静态检查和实库跳过项见 H03。V/U 尚未全部验收；未重新执行的参考 SDK 失败形状继续仅标 N，不提升为 Go 实测。

上一版“15项功能差距”的汇总撤销：原G10–G14多数是通用路径验收/调查，G05/G06/G07按操作形状拆开，服务端/权限/环境也已拆开。后续G表按明确子集立项，C表按需求选择，V表补验收；S/B/A/ENV/U/X不直接转为实现待办。

Fresh continuation: [remaining validation and exact external prerequisites](remaining-validation-2026-11-03.md). This report preserves unavailable and rejected shapes; it does not declare the complete matrix passed.
