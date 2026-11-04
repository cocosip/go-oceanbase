# 2026-11-03 Go 驱动实库走查与缺口复验

环境：OceanBase 4.4.2.2，Oracle/MySQL 双模式直连，Windows amd64、Go 1.27.2。连接信息只经进程环境变量传入，本文和测试源码不保存实际地址、账号、密码或 DSN。服务端未开启 SSL，因此本轮不提供 TLS 正向实库证据。

## 已确认 G 项的结果

| ID | 当前测试入口 | 本轮结果与范围 |
| --- | --- | --- |
| G01 | [TestMySQLCharsets / TestMySQLUTF16LEResult](../internal/valuecodec/charset_integration_test.go) | utf8mb4、utf8、latin1、gbk、gb18030、ascii、sjis、big5、gb2312、euckr、dec8、tis620 存储回读通过；UTF16LE 结果通过 |
| G03 | [TestOracleURowID](../internal/valuecodec/urowid_integration_test.go) | server/client/combined 的 UROWID 输入、NULL 复用及 PL INOUT 子集通过 |
| G04 | [TestOracleXMLType](../internal/valuecodec/xml_integration_test.go) | text/server/client/combined-option/automatic/cache 的 XML/NULL 列和函数结果全部通过；确切版本 gate 避免 B14，无 B14 skip |
| G05a | [TestOracleNamedLOBContent](../internal/valuecodec/complex_lob_integration_test.go) | 普通/combined × BLOB/CLOB × OBJECT/嵌套/collection 的 PL 内容输入通过 |
| G07a | [TestOracleNamedObjectText](../internal/valuecodec/complex_text_integration_test.go) | NUMBER、普通/national 字符、递归 named OBJECT 文本结果通过 |
| G07b | [TestOracleClientConstructors](../internal/valuecodec/complex_literal_integration_test.go) | 普通/显式 client Stmt、重复命名、多语句、独立连接验证对象存储及 PL collection 通过 |

[TestComplexTypes](../internal/valuecodec/complex_integration_test.go) 的 OBJECT/collection PL 路径通过；nested-table 持久列子项按 B02 的目标拒绝明确 skip。B02 skip 不算该持久列形状通过，也不否定其他子集。

## 走查修复

Oracle 字典可返回 `VARCHAR2(80)`、`NVARCHAR2(80)`、`RAW(20)`、`TIMESTAMP(9)` 等参数化字段类型。旧解析将它们误当作用户类型，导致 client constructor 返回复杂类型描述错误。现在只在无 owner 或 SYS owner 的 builtin 字段上提取基础类型；其他 schema 下的用户类型保留确切名称。

本地回归包括参数化 builtin 名称与限定用户类型身份；用户类型身份用例已在未修正的规范化逻辑下失败、修正后通过。真实 constructor 存储用例也通过。没有添加业务 SQL 重放；下面的 B14 gate 改变了确切受影响版本的可选优化策略。

## B14 的确切边界

Oracle 4.4.2.2，`Execution.PrepareExecute`，表内两行分别为非 NULL XML 和 NULL，执行 `SELECT X FROM <fixture> WHERE ID>=? ORDER BY ID`。text/server/client 路径返回两行，combined 路径只有首行且无读取错误。

临时诊断在原始响应层确认 combined 为一个 21 字节行包（首字节 0），随后直接是 5 字节结束包（首字节 254），而非 XML 解码丢弃第二行；临时诊断已删除。该证据只覆盖上述确切形状，不推广到所有多行 XML 查询或其他版本。

驱动已针对权威 `ob_version()` 为完整 4.4.2.2 的连接禁用通用可选 `Execution.PrepareExecute` 优化，使用既有独立 PREPARE/EXECUTE，查询只执行一次。没有 SQL 文本猜测、执行后重放或新开关。其他完整版本保持原门槛；必需 native OUT/RETURNING 仍使用原最低版本门槛。

回归覆盖普通版本、v 前缀、OceanBase 复合版本和 build suffix，同时确认 4.4.2、4.4.2.3、4.4.2.20、4.4.2.2.1 不会被当作该确切版本。XMLTYPE text/server/client、启用 combined 开关、自动准备、缓存/参数类型复用的列/函数用例全部返回完整 XML+NULL 两行，已删除 B14 skip。RETURNING、显式 prepared RETURNING、server/client scalar call 和执行策略回归实库通过。代价是该版本普通命令失去 combined 优化；此规避不承诺其他版本没有同类服务端问题。

## 本轮新增实库证据

| 入口 | 结果与边界 |
| --- | --- |
| `TestMySQLExtendedTypes` | server/client × JSON、BIT(8)、YEAR、GEOMETRY、VECTOR(3)、ARRAY(INT)、无维数 SPARSEVECTOR，共 14 子项；text/参数查询与 SQL NULL 均通过。JSON 函数提取、GEOMETRY 完整 SRID/WKB bytes 与回绑通过；向量/数组/稀疏为 CHAR/string metadata |
| `TestMySQLRoaringBitmap` | 函数输入、BLOB/bytes metadata、普通/空/NULL/uint64 最大值和 rb_build_varbinary bytes 回绑通过；直接 bytes 写列 1235/0A000，B11 明确 skip |
| `TestMySQLMultiStatementLongData` | PrepareClient + AllowMultiStatements 下，长文本含中文、NUL、引号、反斜杠完整返回，第二结果正确，实库通过 |
| `TestOracleSDOGeometry` | generic Object/Array polygon 输入、显式 prepare 整列读取、NULL SRID/POINT、elem-info 与 ordinate 数组元素全部通过；初次 ownerless 结果 ErrComplex 已修复 |
| `TestOraclePLTypeReadiness` | ANYDATA ConvertNumber→GetNumber 的 42 回读通过；普通 RECORD package/body VALID、PL INOUT 更改字段、NUMBER/VARCHAR2 字段目录通过；不包含 Go 原生 RECORD/ANYDATA typed 参数 |

SDO 修复仅在 ownerless SDO_GEOMETRY、本地类型和 private/PUBLIC 同义词均不存在时解析 SYS.SDO_GEOMETRY。本地回归验证 local type/synonym 优先、显式 schema 不回退，并避免将 SYS fallback 缓存在当前 schema 的限定名 key 下。没有新增 GIS 模型。

RECORD 字段目录在 PL 编译/使用该类型后才返回布局；使用前直接查询为空，不能据此误判服务端缺字段目录。ANYDATA 类型在当前 SYS 字典存在；指定 SQL `SELECT SYS.ANYDATA.ConvertNumber(1) FROM DUAL` 返回 942/42S02，但 PL 数字构造/回读成功，旧“目标缺 ANYDATA 包”的结论撤销。

当前账号 CREATE VIEW 成功；指定 CREATE SYNONYM ... FOR SYS.DUAL 返回 900/42000，不能记作权限不足。ALL_ARGUMENTS.CHAR_LENGTH/CHAR_USED 查询仍返回 904/42S22（B12）。这些探索性探针已清理，未把日志探针作为正式自动化验收保留。

七种新增 MySQL 字符集已有服务端 collation；SET NAMES 和 `HEX(CONVERT('abc' USING ...))=616263` 均接受：

| charset | general / binary collation ID |
| --- | --- |
| cp850 | 4 / 80 |
| hp8 | 6 / 72 |
| swe7 | 10 / 82 |
| ujis | 12 / 91 |
| macroman | 39 / 53 |
| cp932 | 95 / 96 |
| eucjpms | 97 / 98 |

The table above records the initial server-readiness probe, not complete charset acceptance. The later [charset/NUL follow-up](charset-nul-validation-2026-11-03.md) implements strict Go mappings and validates non-ASCII storage/results; its SWE7 authentication boundary remains explicit. `have_ssl=YES` indicates capability only and does not prove TLS is enabled; this deployment provides no positive TLS acceptance evidence.

## 检查与复现

先在终端私下配置对应的 DSN 环境变量；Oracle 使用 `OB_TEST_PROFILE=oracle`，MySQL 使用 `OB_TEST_PROFILE=mysql`，均设 `OB_TEST_REQUIRE=1`。不要把实际连接串写入脚本、fixture 或 Git。

```powershell
# MySQL profile
go test -tags=integration ./internal/valuecodec -run '^(TestMySQLCharsets|TestMySQLUTF16LEResult|TestMySQLExtendedTypes|TestMySQLRoaringBitmap)$' -count=1 -timeout 10m -v
# Oracle profile
go test -tags=integration ./internal/valuecodec -run '^(TestOracleURowID|TestOracleXMLType|TestOracleNamedLOBContent|TestOracleNamedObjectText|TestOracleClientConstructors|TestComplexTypes|TestOracleSDOGeometry|TestOraclePLTypeReadiness)$' -count=1 -timeout 10m -v
```

既有 G 项与新增专项均退出 0；B14 无 skip，B02/B11 的确切服务端拒绝仍单列。多语句 LongData 另运行 `go test -tags=integration ./internal/statement -run TestMySQLMultiStatementLongData -count=1`；Oracle 原生输出回归覆盖 internal/call 与 internal/statement。本地检查：

| 检查 | 结果 |
| --- | --- |
| `go test ./... -count=1 -skip NamedPipe` | 通过 |
| `go test ./internal/transport -run NamedPipe -count=1 -timeout 2m` | 沙箱内三个用例 Access is denied；沙箱外重跑通过 |
| `go vet ./...` | 通过 |
| `golangci-lint run --build-tags=integration` | 0 issues |
| `go test -tags=integration ./... -run '^$'` | 全仓集成标签编译通过；不算实库执行 |
| `git diff --check` | 通过 |

## 剩余项能否完成

整个能力矩阵尚未全部验收。已实现子集、还可开发的客户端工作、目标服务端拒绝和环境前提应分别判断：

| 条目 | 当前判断 | 下一步/能否由客户端独立完成 |
| --- | --- | --- |
| U06 additional charsets | Updated: seven strict Go mappings implemented | Six connection charsets and all seven column/result charsets passed live; SWE7 `@tenant` identity rejection is preserved. See [focused evidence](charset-nul-validation-2026-11-03.md) |
| U02 普通 RECORD | 服务端 PL 与成员目录可用；Go 缺原生 descriptor/writer | 可继续做客户端协议工作；须先取得成功 typed IN/OUT wire，不能用 PL 内部变量测试代替绑定验收 |
| U03 ANYDATA / locator 成员 | ANYDATA PL 已可用，Go typed writer 与 locator/cursor 成员输入仍无成功依据 | 可以继续调查原生描述和输入协议；没有证据称当前服务端全面不支持；持久 ANYDATA 列 S02、REF CURSOR IN/INOUT X01 独立排除 |
| U01 temporal/national/LOB collection headers | TIMESTAMP/NCHAR/NVARCHAR2 native IN/OUT, named nanosecond INOUT and mixed national charsets verified in 15 fixture configurations | [Focused evidence](complex-temporal-national-validation-2026-11-03.md); anonymous direct TIMESTAMP INOUT remains six-digit on this target; anonymous LOB/locator headers remain open |
| U04 piece-data 无 ACK pipeline | 现有协议逐片 ACK，Java 同样如此 | 归 C09 可选性能范围，不盲写无 ACK pipeline；未来有协议依据再重开 |
| U05/U07/V13–V17 | 上述基础子集已通过，SDO 已修复 | 不再将全部类型列为待开发；坏维数/空嵌套数组/非 NULL SRID/其他 GIS 形状等边界仍可补验 |
| U08 Oracle long NUL scripts | Updated: old concatenation failure reproduced and fixed with REPLACE | 12003/98304/196608-byte single/double queries and independent CLOB storage/control checks passed; larger sizes remain unverified. See [focused evidence](charset-nul-validation-2026-11-03.md) |
| C02 自动 PREPARE 1295 回退 | 实现与 peer 回归已有，实库触发 1295 形状缺 fixture | 需合适 SQL/版本重现；不重放已执行业务 SQL |
| C08 多语句 LongData | 指定 MySQL 组合实库已通过 | 已关闭该组合验收；其他方向不是同一承诺 |
| C09/C10/C11 | pipeline 资格、普通列流 API、Oracle warning 详情的已知范围 | 边界记录，不扩为自造契约/猜测 API；C10 无服务端逐列流原语、参考 SDK 仅覆盖 LOB 与游标；warning 详情仍须服务端接口依据 |
| S01/S02/S03 | 文档明确不支持 LONG/LONG RAW/BFILE/原生 NCLOB、ANYDATA 持久列、ARRAY 默认值 | 不能靠驱动补齐；需要服务端产品支持或改用已支持形态 |
| B01–B14 | 保留每项版本/模式/方向和证据来源；本轮新增 Go B11/B12 与 B14 规避证据 | 仅限确切形状；如 XA JOIN/SUSPEND、nested-table 持久列、XMLTYPE PL 输出、Bitmap 直接 bytes 等不能靠猜测 codec 绕过；B14 已由驱动安全规避 |
| A01/A02 | 当前 view 权限已通过；同义词 900 不归权限；第二身份/代理 grant 仍无 fixture | 不改账号权限；补身份/授权后验收 ChangeUser/ProxyUser 等 |
| ENV01–05 / V01–11 | 无启用 TLS/mTLS、真实 ODP/多节点/OCP、KDC/plugin、完整平台及长稳/故障部署 | 需要对应服务/环境；不能称为服务端产品不支持，也不能用本地 peer 替代实库正向证据 |

B 表中只有标 H03 的条目可算本次 Go 复验；仅 N/H20 的失败形状仍属参考/历史证据。完整逐项分类见[缺失矩阵](go-driver-completeness.md)。连接配置和临时探针均未保留在 Git；本轮不提交或推送修改。

## Continuation after native temporal/national support

See [fresh continuation results](remaining-validation-2026-11-03.md): acceptance 76 pass / 0 fail / 5 skip / 13 not applicable; 42 extended boundaries; 16 GIS cases with explicit positive and negative counts; native PL output and anonymous LOB restrictions; 12 protocol positives and two required-compression negotiation failures; finite concurrent transactions; current local gates. This updates the earlier protocol/environment snapshot without claiming missing deployments passed.

Oracle warning follow-up: `cmd/obtest` now creates and cleans its own compilation-warning fixture; the fresh full run verifies count > 0, detail-API rejection without overwriting count, and clear-to-zero. No caller-supplied warning SQL is required on the tested privileged account.

Native reset continuation (2026-11-03): [focused evidence](native-reset-and-probe-validation-2026-11-03.md) records that COM_RESET_CONNECTION succeeds in both modes on the direct target, that `ResetFull` still evicts because `database/sql` would reuse connection-bound statement handles, that the MySQL 44-statement PREPARE probe produced no 1295 fixture, and that Oracle mode exposes no warning-detail SQL.
