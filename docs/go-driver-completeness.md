# Go 驱动功能缺失与限制矩阵

更新日期：2026-11-02。已重新核对[能力矩阵](go-driver-capabilities.md)、Go 实现、官方文档及 Java/.NET 的成功和失败记录。**客户端缺实现、客户端范围限制、服务端明确不支持、指定目标拒绝、权限不足、环境未具备、尚未验证、依据不足**分别列出，不合并计算“缺失功能数”。

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

**来源口径**：H20/H24 为 Go 历史实库报告；N 为 .NET 文档和用例中的参考实测记录，必要时含 Java 对照，**本次没有用 Go 重跑**。N 可证明某个子集可实现或限定服务端前提，不能记作 Go 实测通过/失败。索引见文末。

## 已确认的客户端实现差距

这里列一个尚缺实现的明确子集，不是全类型支持承诺，也不是本次已复现的数据损坏 bug。

| ID / 关联能力 | 服务端可用范围与依据 | Go 确切缺口 | 明确不包含 |
| --- | --- | --- | --- |
| G07b / Q05、T14 命名复杂构造输入 | N 的多语句命名 OBJECT、递归 OBJECT、named collection 构造 SQL 有成功子集 | [ClientLiteral](../internal/valuecodec/literal.go)不处理 Object/Array，缺命名构造器路径；binary 复杂绑定仍可用 | 不含匿名值、locator、集合子树 NULL OBJECT、复杂结果/所有 PL-only 类型；展开必须有界并保留类型身份 |

G01/G03/G04/G05a/G07a 已补本地实现，仍需各自的 Go 实库验收；G04 已限定系统 XMLTYPE 字符串结果；G07a 只读取已证明可解析的结果。参考成功不能替代 Go 补实现后的回归和实库验收。

## 已补实现，实库待验收

| ID / 关联能力 | 本次实现与本地证据 | 服务端/验收边界 |
| --- | --- | --- |
| G01 / T19–T20 字符集 | 已补 SJIS/Big5/DEC8/TIS620/GB2312/EUCKR 严格编码、握手和结果 collation，UTF16LE 结果解码及列长度；修正 x/text 与 OceanBase 映射/别名差异，完整单字节/双字节解码摘要及所有有效字符无损回编码、本地公开 SQL/参数/national/握手路径覆盖 | 新增 MySQL 存储/UTF16LE 实库测试入口，当前未配置 DSN，未获得新实库通过；Oracle 指定会话初始化拒绝仍归 B04，未知新字符集仍归 U06；UTF16LE 不开放 SQL client |
| G03 / T12 显式 UROWID 参数 | 已补 `URowID`/`sql.Null[URowID]`/nil `*URowID`、Oracle type209 typed NULL、client CAST、OUT/INOUT 推导与显式 Type；公开 SQL peer 覆盖普通 prepare 类型复用、client/combined 及输出方向；MySQL/坏值/长度在发送前拒绝 | 新增直连/代理长 ROWID 回绑 UROWID 列、NULL 复用和 PL INOUT 入口，当前 DSN 未配置全部跳过；type209 结果仍按 ROWID 元数据读取，未猜测 UROWID 结果名；批处理沿既有资格判定顺序回退 |
| G04 / T17 XMLTYPE String 结果 | 已补 ownerless/SYS XMLTYPE type160 身份识别、text/binary 长度编码和严格 UTF-8 string/NULL、XMLTYPE 元数据；跳过 UDT 描述且不误识别用户同名类型，覆盖 mixed OBJECT/XML 结果 | 直连/代理列存储/函数结果测试入口未配置 DSN，未获得 Go 实库通过；无 XML DOM/SQLXML API；PL OUT/INOUT 指定目标限制 B05 继续独立保留 |
| G05a / T14 命名 LOB 内容输入 | 已补命名 flat/nested OBJECT 和 named collection 的 BLOB bytes/Raw、CLOB 严格 UTF-8 内容输入；保留命名 LOB collection 身份，普通 prepare/combined、NULL/empty、大值和预算本地通过 | PL 读取成员的直连/代理测试入口全部因 DSN 未配置跳过；不包含匿名 LOB 元素头、NCLOB、locator 回绑；B06 持久 BLOB 成员写列限制继续保留 |

| G07a / T15 named OBJECT text 结果 | 已补描述符驱动的 NUMBER、普通/national 字符、递归命名 OBJECT 文本解码及 UTF-8、NULL、引号/分隔符、身份/字段数/深度/节点/字节预算校验；全响应结束后查询描述符，公开 text/client、混合 XML、多结果、尾包/连接复用通过 | 只支持已证明可解析的 named OBJECT 子集；RAW/DATE/TIMESTAMP/LOB/cursor/collection 描述符继续拒绝，包括整列 NULL；普通 VARCHAR2 不走 parser；新增直连/代理实库用例均缺 DSN 跳过，B07/B08 继续独立保留 |

## 客户端范围限制与可选扩展

| ID / 原关联 | 已有能力与限制 | 正确处理 |
| --- | --- | --- |
| C02 / 原 G02、Q04 prepare 回退 | 显式 PrepareClient 已有；[server prepare 错误](../internal/execution/execute.go)返回调用方，无普遍 1295 自动回退 | 可选自动化，不是参数化 SQL 全部缺失。N 有注入1295+真实存储，目标原生1295未确认；扩展须限定未执行业务的拒绝、可插值参数且不重放 |
| C08 / 原 G08、Q05/Q19 多语句 LongData | [插值](../internal/statement/interpolate.go)拒绝流，server-prepared 长参数与普通多语句已有 | 可选有界展开；不是直接把 piece-data 拼入文本，也不要求无限流式脚本 |
| C09 / 原 G09、Q20 pipeline | [TryNative](../internal/batch/native.go)仅选限定 scalar DML；SELECT/复杂值/流/framed 等顺序执行 | 性能/组合范围差距，不是 SQL 不能执行；N 对应子集可供扩展依据，Oracle piece-data 另见 U04 |
| C10 / Q09 字段无限流式读取 | Rows 按行读取，普通字段/复杂值有限额，大对象另有 LOB/TableBlob | 已定义资源契约；如需普通列流 API，另定顺序/所有权/取消契约 |
| C11 / C17 Oracle warning 详情 | count 已有，[ReadWarnings](../internal/control/warnings.go)只给 MySQL 详情 | 客户端/参考接口范围；当前依据不能证明“数据库没有任何 Oracle warning 详情能力” |

## 官方明确不支持的形态

| ID / 能力 | 官方依据 | 对 Go 的结论 |
| --- | --- | --- |
| S01 / T16 LONG、LONG RAW、BFILE、NCLOB | [Oracle 类型表](go-driver-capabilities.md#依据与维护)明确不支持；H20 原生 NCLOB DDL 也返回902 | 不列客户端待补类型；现有 NCLOB 分支、Java NClob 别名/GetTypeInfo 名称不构成原生支持 |
| S02 / T23 ANYDATA 持久表列 | [ANYDATA 文档](go-driver-capabilities.md#依据与维护)适用于企业版 Oracle 模式，限定 PL，不能作表列/永久存储，含内嵌 LOB 的持久化也受限 | 不提出普通表列 codec；不能反推 PL ANYDATA 也被全面排除，见 B10/U03 |
| S03 / T25 SQL ARRAY 默认值 | [ARRAY 文档](go-driver-capabilities.md#依据与维护)不支持数组默认值 | DDL 语义限制，不增加驱动“数组默认值”功能 |

## 指定目标的服务端拒绝或返回形状限制

仅限记录中的 OceanBase **4.4.2.2** 指定模式/形状。H20 为 Go 历史实测；N 为参考记录，**不是 Go 复现**。不外推其他版本或全部同名能力。

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
| B10 / T23 ANYDATA包 | Oracle，N：该目标缺SYS.ANYDATA，Java/.NET同SQL失败 | 只限该目标包前提；不覆盖官方PL支持，不开发猜测codec |
| B11 / T26 Bitmap直接bytes绑定 | MySQL，N：byte[]直接写ROARINGBITMAP列1235/0A000 | rb_from_string/rb_build_varbinary路径可用、结果BLOB/bytes，归V16；不要求codec“修复”拒绝的绑定 |
| B12 / M01 参数目录字符长度 | Oracle，N：ALL_ARGUMENTS.CHAR_LENGTH/CHAR_USED查询904/42S22，Java同SQL同码 | Go[当前参数计划](../internal/catalog/metadata_sql_oracle.go)读DATA_LENGTH，未查询这两列；不是Go当前查询已失败，也不能把字节长度当字符长度 |
| B13 / T08 TIME极限小数 | MySQL，N：指定permissive binary截整秒，strict text1292而binary仍截断成功 | 路径相关服务端语义记录，尚非Go复现缺陷；不静默截断/CAST来制造一致 |

## 权限、身份与环境不足

| ID / 关联能力 | 已记录的前提不足 | 分类边界 |
| --- | --- | --- |
| A01 / M01 Oracle VIEW/SYNONYM | H20缺CREATE VIEW；N另有VIEW/SYNONYM 1031 | 账号授权不足，不是无视图/同义词或无元数据接口 |
| A02 / C09/C12 另一身份/代理 | H20缺第二身份/代理grant，CREATE USER探针1031 | 缺授权fixture；保留ChangeUser/ProxyUser实现，不修改业务账号权限来制造通过 |
| ENV01 / C06 TLS/mTLS | H20指定直连SSL未开 | 缺正向部署，不是OceanBase产品不支持TLS；本地证书peer不是实库通过 |
| ENV02 / P02–P05 协议端点 | 历史直连缺压缩/OB20正向目标与FLT/collector组合 | 缺协商环境，不把合成帧当实库，也不把拒绝协商扩大为产品无此协议 |
| ENV03 / C14–C15 ODP/多节点/OCP/TNS | H20仅直连单端点 | 缺真实代理/控制面/集群，单上游relay不证明HA |
| ENV04 / C08 plugin/KDC | 客户端分支存在，缺真实正负端点和逐插件服务端依据 | 不能列服务器已支持全部，也不能列明确不支持全部；逐项确认 |
| ENV05 / C05 平台原生传输 | 真实Linux VC、系统GSSAPI、服务IPC/ACL前提未全验 | OS/tag/库/服务条件，不是普通SQL缺口 |

这些是历史或参考材料，不声明本次已探测当前部署状态。

## 有路径但缺 Go 专项验收

| ID / 关联能力 | 已有路径 | 待确认的Go证据；不应误列的缺失 |
| --- | --- | --- |
| V01 / C06 TLS/mTLS | tls.Config、证书校验、正负peer | 实际CA/主机名/mTLS/轮换，前提ENV01；不是再写TLS |
| V02 / C05/C08 平台/认证 | transport/authentication与ABI/peer | 实际OS/runtime/plugin/KDC，前提ENV04/05；客户端名字不证明服务端支持 |
| V03 / C14–C15 路由/发现 | 地址选择、TNS/OCP、刷新/建连切换 | 实际租户身份/角色/硬过期/恢复，前提ENV03；已发业务不重放 |
| V04 / P02–P05 协议/trace | 帧、CRC、协商/trace子集 | 实际layer/双向帧/控制/collector，前提ENV02，三类checksum分开 |
| V05 / X03–X04 故障/XA | 正常XA、unknown分类/故障peer | 回执丢失、节点/代理/进程崩溃、独立查账/持久恢复，不是缺XA API |
| V06 / C10/Q23/R01 长稳/性能 | 标准池、代际隔离、有限测试/peer benchmark | 真实长稳/句柄/服务端资源和实库吞吐；性能未测不算业务功能缺失 |
| V07 / M01/Q07 元数据 | 目录计划/历史子集 | 最小权限、overload、引用身份/precision等；A01/B12另列 |
| V08 / T08–T11 时间/编码 | 精确时间、national与已有charset | 零日期SQL_MODE、DST/LTZ/地区/存储；B04/B09不变成待写codec |
| V09 / Q10–Q11/T13–T15 资源组合 | cursor/LOB/OBJECT/OUT子集 | 提前Close/取消/尾包/预算/事务池代际/可见性；B02/B03/B06/B07/B08逐形状排除 |
| V10 / C09/C11–C13/Q18/Q22 会话 | 初始化、属性、读超时、ChangeUser/导入/取消 | 真实身份/中断码/部分效果/借用者隔离；A02另列 |
| V11 / R01 平台/版本/CI | 既有构建测试/历史CI | 当前发布源码适用OS/tag/Go/实库组合，不外推未来版本 |
| V13 / 原G10、T05–T07 JSON/BIT/YEAR/GEOMETRY | [string/bytes/整数绑定](../internal/valuecodec/binary.go)与结果解码 | 先验真实列/转换；无专用Json/Bit/Year/Geometry类型本身不算缺功能，native writer必要性见U07 |
| V14 / 原G11、T18 VECTOR | N的wire为CHAR/String，Go已有[文本结果](../internal/valuecodec/text.go)/string参数 | Go专项存储/NULL/维数/metadata/坏值，无专用codec不列缺失；稀疏另见U05 |
| V15 / 原G12、T25 SQL ARRAY | N的ARRAY(INT)为CHAR/String，Go string可承载；Oracle某些collection构造text结果也只是VARCHAR2 | Go专项空/NULL/元素/metadata/存储；不把Oracle Array API等同MySQL ARRAY，不对普通string强用complex parser |
| V16 / 原G13、T26 RoaringBitmap | N的BLOB/bytes结果与SQL函数写入，Go已有bytes/string | Go专项64位元素/空NULL/序列化/函数回绑；直接bytes绑定B11不算客户端缺失 |
| V17 / 原G14、T22 SDO_GEOMETRY | 官方复合类型，N用generic OBJECT/ARRAY有整列读取/受限输入 | 先验Go描述符/成员/NULL/SRID；无GIS领域模型不算缺功能 |

## 依据不足，暂不归因

| ID / 原关联 | 尚不能下的结论 | 需要先核实 |
| --- | --- | --- |
| U01 / 原G05 TIMESTAMP、G06 collection头 | Go有拒绝点，未核实所有TIMESTAMP/NCHAR/NVARCHAR2/LOB元素输入wire，不能整个组合算确定缺失或服务端不支持 | named/anonymous、元素头/成员体/方向/NULL/empty逐形状取得成功依据；PL LOB内容子集已列G05a |
| U02 / 原G15 PL-only RECORD | 缺字段目录/公开绑定不等于数据库不能声明/使用RECORD；Java前置拒绝不能证明服务端拒绝 | 普通RECORD字段描述/成功wire；含cursor成员导致package INVALID的参考形状仅说明该包形状前提失败 |
| U03 / T23 ANYDATA PL、locator/cursor成员输入 | 官方PL支持、已有输出/标量locator不能证明这些专门输入协议 | 满足B10包前提后取得描述/writer/方向；REF CURSOR IN/INOUT按X01排除 |
| U04 / C09 Oracle piece-data流水化 | Java也逐片ACK，不能直接算Go少功能 | 是否存在允许多片无ACK的目标协议；无依据保留顺序，不猜协议 |
| U05 / T24 SPARSEVECTOR | N失败的是SPARSEVECTOR(3)，官方示例是无维数的SPARSEVECTOR且说明V4.3.5 BP2起支持；1064不足以证明类型不支持 | 按正确语法验DDL、值/metadata/函数；TO_VECTOR 1305只说明那个函数调用不可用 |
| U06 / G01 新编码/版本 | 官方列出UJIS/EUCJPMS/CP932/CP850/HP8/MACROMAN/SWE7和版本条件，但目标部署/用途未核实 | 逐版本/模式/用途确认ID/映射；不能Go不识别就归服务端，也不外推Oracle初始化 |
| U07 / 原G10 native参数码 | 通用输入已有，无type245/16/13/255专用writer不自动说明相关类型无法用 | 先验V13真实存储，只有明确无法满足的参数身份才新增缺口 |
| U08 / Oracle超长NUL脚本 | Java/.NET特定大脚本超时不能单独证明服务端不支持或Go parser有缺陷 | 正确SQL/确切大小预算/发送执行消费阶段/独立效果；保留成功与失败长度，不随意截断 |

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
- N：obconnector-dotnet/docs/obconnector-dotnet-completeness.md及obconnector-dotnet-missing-features-roadmap.md的D-02d1～d3、D-04a1/a2/e、D-05、D-07、D-08m、D-09、D-12a/b/c、目录/RECORD段落；本次核对ComplexLobInputTests.cs、XmlProbeTests.cs、VectorArrayStringWireTests.cs的实际用例形状，不记作Go实测。
- Java：核对ComplexUtil.storeComplexAttrData成员writer、createSQLXML客户端拒绝和OB20 reader压缩长度拒绝；没有把Java拒绝当服务端不支持证明。

本次定向运行 charset、valuecodec、parameters、materialize、statement、batch 六包中与映射、复杂值、拒绝/不重放及批处理回退相关的 15 个顶层测试，全部通过。默认 Go 缓存首轮遇到访问拒绝，改用项目内缓存后通过；这只证明所选本地用例，不是实库兼容性验收。本次未连接数据库，也未重新验证当前部署、权限或参考 SDK 的服务端失败形状。

上一版“15项功能差距”的汇总撤销：原G10–G14多数是通用路径验收/调查，G05/G06/G07按操作形状拆开，服务端/权限/环境也已拆开。后续G表按明确子集立项，C表按需求选择，V表补验收；S/B/A/ENV/U/X不直接转为实现待办。
