# Go 驱动功能缺失矩阵

更新日期：2026-11-02。本文按[能力矩阵](go-driver-capabilities.md)中的 ID 记录 Go 驱动自身不足；结合官方数据库能力、Java 实现与 .NET 现有矩阵识别差距，不把其他 SDK 的 API 名称逐个转为 Go 待办。

## 分类与优先顺序

| 分类 | 含义 | 是否直接要求改产品代码 |
| --- | --- | --- |
| 缺实现 | 当前没有该明确能力的实现路径或完整契约 | 选定功能目标后开发；不是已经定位的运行时 bug |
| 子集限制 | 已有实现明确拒绝某些组合，或安全降级到顺序/通用路径 | 先判断扩展必要性，再补准确子集 |
| 候选差距 | 数据库或参考 SDK 有相关能力，Go 尚未承诺完整支持 | 先取得类型/协议/业务需求证据，不默认新增专用 API |
| 欠验收 | 有代码/本地 peer，缺实际版本、部署或故障证据 | 先验收，发现客户端问题再修改 |
| 服务端/权限阻塞 | 历史目标拒绝、缺权限或环境未配置 | 保留确切条件；不能当 Go 缺实现，也不外推全部版本 |
| 排除/生态差异 | Go 接口不同、非 OceanBase 范围或有意不提供 | 不进入开发积压 |

下面“优先”是后续排期建议：**高**优先核实核心类型/兼容性，**中**为功能扩展，**低**为专用领域或性能扩展；不是承诺本次开发。各行关闭条件只关闭该行的明确子集，不推导全能力验收。

## 实现与功能子集差距

| ID / 关联能力 | 分类 / 优先 | Go 当前缺口与影响 | 代码证据、最小补齐及关闭条件 |
| --- | --- | --- | --- |
| G01 / T19–T20 字符集覆盖 | 缺实现 / 高 | Go 白名单与 collation 表缺 GB2312、EUCKR、SJIS、Big5、DEC8、TIS620 等服务端/.NET 已有覆盖的编码；UJIS/EUCJPMS/CP932/CP850/HP8/MACROMAN/SWE7 另属版本相关候选。配置拒绝或未知结果 collation 会阻止读取，不是全部可用 UTF8 自动替代 | [charset](../internal/charset/charset.go)、[握手 ID](../internal/charset/handshake.go)；逐编码确认目标 ID、严格映射与 SQL/绑定/列名/结果/national 行为；坏字节拒绝、NULL、跨包分片与实库存储往返分别通过，不能仅扩名字白名单 |
| G02 / Q04 普通 prepare 自动回退 | 缺实现 / 中 | Go 有显式 PrepareClient，但默认 server 路径 prepare 失败即返回；没有 .NET/Java 相关客户端回退能力的等价明确契约，服务端不支持 prepare 的普通参数化语句需调用方主动选模式 | [执行路径](../internal/execution/execute.go)、[client 路径](../internal/execution/client.go)；如纳入目标，仅在明确未执行业务的 prepare 不支持错误且参数可安全插值时回退；显式 Prepare、流、输出、资源及未知错误不得隐式降级；验证无业务重放/事务不变/实际存储 |
| G03 / T12 UROWID 参数身份 | 缺实现 / 高 | type209 结果和 ROWID 文本已有，但 RowID 参数目前发 type253、client 为 CHARTOROWID；没有独立 UROWID/type209 参数及 NULL/OUT/RETURNING 语义，不能宣称已覆盖 UROWID 绑定 | [scalar 值](../types/scalar.go)、[参数编码](../internal/valuecodec/binary.go)、[字面量](../internal/valuecodec/literal.go)；确认目标 wire 后最小增加参数身份，验证 NULL、text/binary、表存储、OUT/INOUT/RETURNING、目录类型与非法值；可沿用字符串存储，不必建立复杂领域类型 |
| G04 / T17 XMLTYPE String 子集 | 缺实现 / 高 | Oracle type160 一律走复杂值，text type160 被拒绝，未适配 .NET 已验收的 ownerless XMLTYPE String 结果子集；Java createSQLXML 拒绝不代表数据库没有 XMLTYPE | [物化入口](../internal/materialize/decoder.go)、[复杂描述](../internal/valuecodec/descriptor.go)；先获取实际 metadata/owner/type160 payload，区分 XML String 与真正 OBJECT，验证 text/binary/NULL/参数/存储；可先复用 string，不要求 SQLXML DOM 或专用 XML 对象 |
| G05 / T14 TIMESTAMP/LOB 成员输入 | 子集限制 / 中 | `EncodeComplexScalar` 非 NULL TIMESTAMP、BLOB/CLOB 等走 ErrUnsupported；复杂值读取中的 nested LOB 不代表可以写入成员；.NET 的指定输入子集不自动转为 Go 支持 | [complex scalar writer](../internal/valuecodec/complex.go)；分别定义内容、locator、timestamp 输入，取得成功 wire/存储证据后扩展；flat/nested、NULL/empty、大值、national/时区、失败不发送及事务资源回收分别验证；当前目标 named BLOB 成员拒绝不能靠猜码修复 |
| G06 / T11、T14 collection 元素头 | 子集限制 / 中 | builtin NCHAR/NVARCHAR2/TIMESTAMP/LOB collection 元素头未支持；即使空集合或元素全 NULL 也不能绕过类型头校验；普通 national 标量支持不覆盖此项 | [ComplexElementWireType / EncodeComplexMetadata](../internal/valuecodec/complex.go)、[复杂绑定](../internal/parameters/complex.go)；逐元素类型确认 metadata/writer，再验命名/匿名、空/NULL/嵌套及实库回读；不把 Oracle 数组绑定当 collection 测试 |
| G07 / T15 复杂值 text 结果与插值 | 子集限制 / 中 | Go 拒绝 type160 text 结果；ClientLiteral 也没有 Object/Array 输入。使用 text 查询、client prepare 或多语句时，不能获得 binary 路径相同的复杂值覆盖 | [物化](../internal/materialize/decoder.go)、[ClientLiteral](../internal/valuecodec/literal.go)；若有可靠类型描述和可逆 payload，可先支持已证明的命名 scalar/nested 子集；输入构造器与输出 parser 单独验收，保留无成员身份/服务端坏 payload 的拒绝 |
| G08 / Q05、Q19 多语句流参数 | 子集限制 / 中 | client 插值在发现 LongData 时拒绝；.NET 有有界流展开子集，Go 尚无同等路径；这是已有普通多语句的组合限制，不是完全没有多语句 | [Interpolate](../internal/statement/interpolate.go)；如扩展，先定义总字节预算、单次读取、源错误/取消和编码边界，完整展开成功后才发送 SQL；不得假称流式上传或在失败后重读同一流 |
| G09 / Q20 pipeline 范围 | 子集限制 / 中 | 目前只选择限定 scalar DML；SELECT、Object/Array、LongData、资源输出、framed 流/Shared Memory 不在通用流水化范围，不满足门槛则顺序执行；顺序能成功不算 pipeline 已支持 | [TryNative / batchScalarRows](../internal/batch/native.go)、[pipeline](../internal/batch/pipeline.go)；按实际需要选择 reader/复杂输入/流中的一个子集，验证精确发送顺序、逐响应排空、prefix/error/suffix、取消和未知结果；必须检查实际策略，不以性能选项存在作为通过 |
| G10 / T05–T07 显式 JSON/BIT/YEAR/GEOMETRY 输入 | 候选差距 / 中 | 已有通用 string/bytes 与结果解码，但没有参考 .NET 那样的全部显式参数类型身份；隐式转换成功与 native type245/16/13/255 writer 等价性未建立 | [binary 编码](../internal/valuecodec/binary.go)、[结果映射](../internal/valuecodec/text.go)；先用真实列验证通用路径及目录元数据，再决定是否确需公开 typed 参数；SQL NULL/JSON null、BIT64、YEAR 边界、geometry/SRID 分别验证，不默认添加四套新 API |
| G11 / T18 VECTOR/SPARSEVECTOR | 候选差距 + 欠验收 / 中 | 无专用 codec，也无 Go 专项证据证明通用 String 路径存储、结果和 metadata 正确；服务端支持向量搜索不等于 driver.Value 可直接返回向量对象 | [官方类型依据](go-driver-capabilities.md#依据与维护)、[现有 codec](../internal/valuecodec)；优先验证 String/SQL 转换，记录维数、NULL、非法 NaN/Inf、text/server/client 路径和真实版本；SPARSEVECTOR 单独验，不从 dense 通过外推 |
| G12 / T18 MySQL SQL ARRAY | 候选差距 + 欠验收 / 中 | Go Array 是 Oracle collection；MySQL native complex 输入被拒绝，不能以这个 API 宣称 SQL ARRAY 支持。通用 String 路径尚无 Go 专项闭环 | [复杂输入模式检查](../internal/parameters/complex.go)；先验真实 MySQL ARRAY 的字符串参数/结果、列 metadata、空/NULL/NULL 元素、嵌套与非法元素，保持 MySQL ARRAY、Oracle collection、DML 数组绑定三者独立 |
| G13 / T18 RoaringBitmap | 候选差距 + 欠验收 / 中 | 无专用 codec，byte[]/SQL 函数是否足够尚无 Go 实库验收；不能把普通 BIT(1..64) 当位图 | [官方依据](go-driver-capabilities.md#依据与维护)、[通用 byte[]](../internal/valuecodec/binary.go)；优先验证 SQL 构造/序列化和原始 binary 往返、64 位元素、空/NULL、metadata、坏 payload；只有通用路径不足再增加 codec |
| G14 / T17 SDO_GEOMETRY、ANYDATA | 候选差距 / 低 | SDO 可尝试 Object/Array 通用映射但无指定契约/实库证据；ANYDATA 未有公开映射，且官方限定 PL 使用，不能要求实现普通表列读写 | [Object/Array](../types/complex.go)、[官方依据](go-driver-capabilities.md#依据与维护)；SDO 先验通用 descriptor/整列读取/绑定及 SRID/非法几何；ANYDATA 先确认可用包、字段描述和成功调用 wire，未满足前不开发猜测性 parser |
| G15 / Q07 PL-only RECORD、复杂过程签名 | 候选差距 / 中 | 有过程目录读取但没有 PL-only RECORD 字段布局/绑定契约；也不自动生成 .NET 风格参数对象。命名 SQL OBJECT、package RECORD、含 cursor RECORD 不可混为一个能力 | [Oracle 目录计划](../internal/catalog/metadata_sql_oracle.go)、[参数计划](../internal/catalog/parameters.go)；先记录目录实际能提供的唯一签名/字段，再验证可执行 writer；缺字段或 package INVALID 是服务端前提，不能从顶层 DATA_TYPE 猜结构 |

G02 是行为扩展，G03/G04 是明确类型覆盖不足，G10–G15 含候选能力；本表没有把它们误写为已经复现的数据损坏缺陷。G01 的各编码也须逐项确认部署，不因参考 SDK 有名字就一次性宣称全部可用。

## 有实现但欠验收

| ID / 关联能力 | 当前不足 | 可关闭的验收边界 |
| --- | --- | --- |
| V01 / C06 TLS/mTLS | 有本地 TLS 正负 peer，历史真实直连端点未启用 SSL；缺真实证书链、服务器身份、客户端证书映射和轮换证据 | 两模式分别验证正确/错误 CA、主机名、过期证书、mTLS 正负、取消/池替换；实际 ODP 单列；若要求撤销需定义 Go/部署实际策略 |
| V02 / C08 非 native 认证 | 客户端分支存在，但缺服务器插件/KDC、权限与平台正负闭环；Windows NTLM 测试不能替代域 Kerberos；Unix ABI 测试不能替代系统 GSSAPI | 按实际服务端 plugin × 模式 × OS/tag × TLS 策略验收；没有部署支持依据不列服务器支持 |
| V03 / C05 平台传输 | Unix socket、Pipe、Shared Memory、Linux VC 的本地 peer/ABI 与真实服务仍有差距 | 实际 OS/运行库/服务权限、混合客户端、取消/超时/退出/句柄回收分别验收；保留共享内存软取消限制 |
| V04 / C14–C15 ODP、多节点、OCP/TNS | 缺真实代理、控制面响应、租户身份、角色变化和故障切换证据；静态回调/单上游 relay 不等价 | 真实服务身份与角色验证、刷新/硬过期、原节点恢复、新连接路由；业务发送后断线无自动重放，原事务不迁移 |
| V05 / P02–P05 压缩、OB20、trace | 历史直连只证明普通协议回退/拒绝；合成帧不能证明目标双向使用该协议 | 记录实际协商 layer/能力与双向帧；CRC、proxy checksum、SQL checksum 分开；FLT sampling/control/ID 与 collector 分别验收 |
| V06 / X03–X04 真实故障与 XA 恢复 | 正常 XA 和 peer 故障不覆盖节点/代理/协调器崩溃、提交回执丢失、进程重启后的持久恢复 | 明确故障发生于发送前/后，独立连接查账，确认不重放；prepare 后新进程 recover，协调器决定 commit/rollback，旧句柄晚释放不污染新会话 |
| V07 / C10、Q23、R01 并发与长稳 | 有有限轮数和本地 peer 稳定性；没有证明长期真实资源有界、全部高级资源交错与故障组合；benchmark 不等于实库吞吐 | 真实双模式混合负载、池容量/等待、goroutine/heap/系统句柄/服务端资源曲线；记录失败和清理；性能独立测，不从短跑计算生产结论 |
| V08 / M01、Q07 最小权限与目录身份 | 目录计划与 peer 不证明所有 ALL_*/information_schema 列、同义词、引用标识符、overload、schema/catalog 语义 | 真实基础/最小权限账号、表/视图/同义词、重载、引号大小写、NUMBER precision/scale unknown、参数方向；保留权限错误，不以空结果掩盖 |
| V09 / T08–T11 时间/字符边界 | 历史部分时间/编码成功不能覆盖零日期、DST、LTZ/named region、未知 collation、全字符集的存储链路 | 独立确认 SQL_MODE、区域文件、会话/national 编码；text/server/client、实际列存储/NULL/坏值分别验，不用常量或偏移时区替代 |
| V10 / Q10–Q12、T13–T15 高级资源组合 | 已有 cursor/LOB/OBJECT/OUT 的局部实库与本地证据，仍缺所有输入/结果、ordinary/binary/combined、事务/池释放组合 | 优先按明确支持子集验收；跨连接可见性、提前 Close/取消、尾包错误、预算、事务结束和旧资源代际均需有断言 |
| V11 / C09、C11–C13、Q18、Q22 会话扩展 | ProxyUser、distinct ChangeUser、属性/初始化、读超时、导入失败部分效果、CancelQuery 等并非所有模式/权限/代理均已验证 | 逐项具备授权 fixture 后验证实际身份/目标/中断码/存储效果与新借用者隔离；没有权限时保留 Deferred，不以任意 SQL 错误作为通过 |
| V12 / R01 跨版本、平台与当前 CI | 历史 Windows/hosted 报告不能证明当前全部源码、OS、架构、tag 与数据库版本组合均通过 | 对拟发布源码执行适用 build/test/vet/lint、最低 Go、race/无 cgo、OS/tag；实库支持声明绑定实际版本/模式/网络/认证/选项；不承诺全部未来版本 |

以上是 Go 的验收差距，不沿用 .NET 的环境冻结或排期决定。不存在对应端点只限制验收结论，不自动禁止独立的文档/协议研究，也不自动授权申请数据库账号、改租户参数或执行 DDL。

## 必须保留的历史服务端与权限阻塞

这些记录来自 Go [H20 实库报告](release-validation-2026-10-20.md)，本次未重跑。参考 .NET 中后来新增的失败只能作为线索，未复现前不记为 Go 已确认阻塞。

| ID / 关联能力 | 确切历史现象 | 不应推导的结论与重新打开条件 |
| --- | --- | --- |
| E01 / X03 MySQL XA JOIN/SUSPEND | 指定 4.4.2.2 目标返回 1398，Java 同样复现 | 不等于 Go 没有 XA SQL；普通 XA 成功独立保留；新版本/配置提供重关联支持后再验 |
| E02 / T16 Oracle NCLOB | 指定目标 DDL 返回 902，官方类型文档也列不支持 | 不把已有 NCLOB 兼容分支写成原生 NCLOB 支持；不列为当前补 codec 待办 |
| E03 / T14 nested-table 持久列 | 建列 600/-4007 或嵌套存储子句 900，Java 同样失败 | 不等于 collection PL/SQL 传输或 OBJECT 全部不支持；持久列须新目标提供可用 DDL 后再验 |
| E04 / Q11 SELECT CURSOR 指定形状 | 指定 prepared 表达式返回 902；text 路径 Go 拒绝、Java 解码失败 | 不等于所有 cursor 表达式均不支持；过程 REF CURSOR OUT 单列；取得新的可读取 wire 形状后再扩展 |
| E05 / M01 Oracle VIEW | 测试身份缺 CREATE VIEW 权限 | 不是没有视图元数据计划；具备权限后验目录与生命周期 |
| E06 / C09、C12 Oracle 另一个用户/代理身份 | 缺第二身份/代理 grant，CREATE USER 探针 1031 | 不是 ChangeUser/ProxyUser 没实现；需已有授权测试身份，不能修改现有业务账号来制造通过 |
| E07 / C06、P02、P03 TLS/压缩/OB20 | 历史直连端点不提供 TLS/标准压缩等正向协商条件 | 是该部署前提不足，不等于 OceanBase 产品无此能力；有实际协商端点再关闭 V01/V05 |

不得把历史报告“暂未集成”的旧阶段文字覆盖后续 H20/H24 成功，也不得由后续 .NET 成功反推 Go 已通过。

## 不进入功能开发积压的差异

| 项目 | Go 当前决定/替代方式 |
| --- | --- |
| JDBC 类、Java 160 个配置项原样复制 | 已有[选项迁移清单](go-driver-options-mapping.md)；标准库/Go API/固定语义/安全拒绝分别处理，拒绝多少 Java 参数不等于缺多少功能 |
| ADO.NET DataSource/DI/CommandBehavior/DataAdapter/CommandBuilder、TransactionScope | 不同语言接口；Go 使用 database/sql、Metadata、ExecBatch、显式 Tx/XA；不复制接口壳 |
| 自建第二连接池、Java keystore、JMX、内置 OTel exporter | 使用标准库连接池/TLS、应用转换证书并通过 Stats/callback 接观测；若未来有具体产品需求再单独立项 |
| 自动 SQL/事务重放、透明迁移、持久化 XA 协调器 | 不提供安全性不明的自动恢复；应用显式判断 unknown outcome、持久化全局决策 |
| MariaDB bulk、压缩 OB20、proxy reroute、Aurora/Galera | 当前没有 OceanBase 支持承诺；枚举/继承代码/常量不算协议依据 |
| LONG/LONG RAW/BFILE/原生 NCLOB | 官方所列服务端类型边界，不制造客户端专用实现来宣称支持 |
| Oracle Net/OCI、复制/备份管理、SQL 优化器与索引算法 | 属于其他协议、运维产品或服务器职责；普通 SQL 可调用不单列 SDK 功能完成率 |

## 建议推进顺序与复核方式

1. 优先核实 **G01 字符集、G03 UROWID、G04 XMLTYPE**：范围可精确定义，直接影响已有类型数据能否被正确输入/读取；每项单独确定契约后实施。
2. 按业务需要选择 **G02 自动 prepare 回退、G05–G08 复杂值/多语句子集**；先保留拒绝边界，不合并为“支持所有 Oracle 类型”的大任务。
3. 对 **G10–G14 新类型**先跑通用 string/bytes/SQL 转换的最小存储闭环，再判断是否需要新 API；避免仅因服务端有类型就建立专用框架。
4. **G09 pipeline**放在语义正确且有性能需求之后；V01–V12 则根据已有部署独立验收，不拿本地测试替代实库。

本次交付为源码/文档审计和矩阵整理，没有修改驱动实现或执行数据库测试。后续每次关闭缺口，应同步更新本文件和能力矩阵，附实际路径、用例、结果及仍未覆盖的组合；不使用开发任务平均值生成完成百分比。
