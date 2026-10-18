# 独立数据库验收工具

## 目标与边界

`cmd/obtest` 是独立命令行程序，不调用 `go test`、不导入测试包。用户配置一个或多个 OceanBase DSN 后，一次运行完成连接、识别兼容模式、创建隔离对象、准备数据、执行断言、逆序清理和生成 JSON 报告。MySQL/Oracle 是 OceanBase 的兼容模式；直连、OBProxy、TLS、压缩等连接组合由不同目标的 DSN 表达。

工具使用公开 `database/sql` 和 `oceanbase` API。所有代码在单一 `main` 包中按职责分文件，依赖方向为 `cmd/obtest → oceanbase`，不访问驱动内部包，不新增依赖或修改驱动公开接口。不使用全局 logger；命令行进度直接写标准输出，驱动默认静默。

每个场景独立建连并创建随机名称的对象，不使用预先存在的业务表，不自动创建用户/租户，不清理其他运行的对象或 XA 分支。DDL 可能自动提交，因此清理不依赖事务回滚。只在创建成功后登记 DROP；创建结果未知时记录对象名供人工确认，禁止重放业务命令。失败、超时和正常中断均尝试以独立超时清理已登记资源，清理失败使场景失败。

默认执行所有场景；模式不适用单列，缺少第二身份或特定服务器能力配置的场景记为跳过，不能算通过。权限不足、功能不支持、数据不符都算失败。报告包含选择范围、实际模式、耗时、断言结果与对象清理状态，不记录 DSN、密码、原始服务器错误文本。

## 开发步骤与进度

- [x] 配置与执行器：严格 JSON、DSN 环境变量、场景选择、超时、中断、报告、脱敏、逆序清理；用不连接数据库的回归测试验证失败传播和取消后的清理。
- [x] 数据库场景：CRUD、预处理/客户端绑定、精确数值/NULL/Unicode/时间、事务/保存点、元数据、执行摘要、批量、长参数、表内 BLOB、过程输出、游标、Oracle 原生 LOB/复杂类型/RETURNING/资源输出、XA、会话、warning、取消和切用户。
- [x] 使用说明、配置示例和现有开发进度入口。
- [x] Go 1.25/1.26 构建、离线工具检查、单测、vet、lint、包依赖守卫；复核未发现剩余明确问题。
- [ ] 用户在实际测试数据库运行完整流程并保存报告；本次开发不连接数据库。

## 运行说明

只在专用测试库运行。账号应具有建表、读写及删除自建对象的权限；完整流程还需要过程、函数、Oracle 类型、字典访问及 XA 权限。工具不创建账号或授予权限。库/schema 应预先存在，不允许 `createDatabaseIfMissing=true`。

在仓库根目录编译，之后可将生成的单个可执行文件放在测试机器运行，无需 Go 工具链：

```powershell
go build -o bin/obtest.exe ./cmd/obtest
$env:OB_DSN = 'oceanbase://test%40tenant:password@127.0.0.1:2881/test?mode=auto'
./bin/obtest.exe -config docs/obtest.example.json -report obtest-run-001.json
```

也可设置 `OB_DSN` 后直接执行 `go run ./cmd/obtest -report obtest-run-001.json`；此时无需配置文件。Linux/macOS 编译时可使用 `go build -o bin/obtest ./cmd/obtest`。本阶段仅验证 Windows 构建/离线入口，不代表其他平台或数据库流程已经运行。

连接字符串采用驱动原有 URL 格式，用户名中的 `@` 等字符应进行 URL 编码。兼容模式由认证后的物理连接确定：工具通过公开的 catalog 能力区分模式，再固定该目标各场景的模式约束。配置 `mode=mysql/oracle` 可增加明确约束，模式不符会失败。TLS、协议、认证、选址等 DSN 字段见[使用指南](go-driver-usage.md)及[配置映射](go-driver-options-mapping.md)。

下面两个命令不会连接数据库，也不会创建数据库对象：

```powershell
./bin/obtest.exe -list
./bin/obtest.exe -config docs/obtest.example.json -check
```

`-list` 直接显示内置场景，不需要配置或凭据。`-check` 检查文件、主 DSN 和连接器配置，不证明凭据、权限、网络、服务器版本或可选 fixture 有效。报告文件必须是新的可写路径；程序拒绝覆盖已有文件，并在首次连接前检查报告文件能否创建。

## 配置

[最小配置示例](obtest.example.json)只需通过 `OB_DSN` 提供一个连接字符串。配置文件最大 1 MiB（含空白），超限或读取失败时在连接数据库前拒绝，不截断解析；文件只能包含一个 JSON 对象。顶层字段如下：

| 字段 | 默认值 | 含义 |
| --- | --- | --- |
| `targets` | 配置文件中必填 | 一个或多个连接目标，按顺序运行；不并行修改目标对象 |
| `cases` | 全部 44 项 | 选填场景名称数组；拼写错误、重复项直接拒绝，不允许静默漏测 |
| `timeout` | `2m` | 每个场景及目标探测的独立超时，最大 `1h` |
| `cleanup_timeout` | `30s` | 每个清理动作的独立超时，最大 `1h`；清理时间不计入场景执行预算 |
| `require_all` | `false` | `true` 时把缺少 fixture 的跳过视为失败；模式不适用仍单列 |

每个 target 的字段：

| 字段 | 含义 |
| --- | --- |
| `name` | 唯一目标标签，1–64 个 ASCII 字母/数字/下划线/短横线，首字符为字母或数字 |
| `dsn_env` / `dsn` | 二选一：环境变量名称或直接配置连接字符串；建议用环境变量保存凭据 |
| `change_user_dsn_env` | 可选，另一身份的 DSN 环境变量；只读取身份和默认 schema，不使用该 DSN 的地址/协议。第二身份须属于同一入口、租户和兼容模式，且与主身份不同 |
| `local_import_sql` | Oracle LOCAL import 的部署专用 SQL，须含 `{table}`，请求文件名为 `input`；MySQL 默认自动生成 LOAD DATA LOCAL INFILE |
| `warning_sql` | 可选，成功执行且产生非零 warning 的测试 SQL；MySQL 默认使用非数值 CAST，Oracle 缺配置则跳过正向 warning 场景 |
| `cancel_sql` | 可选，长时间运行的测试 SQL；默认自动建表并持有行锁，以另一会话的 UPDATE 作为取消对象 |
| `cancel_code` | 可选，显式取消预期的服务端错误码；默认 MySQL `1317`、Oracle `1013`，部署映射不同时显式覆盖 |

`warning_sql`、`cancel_sql` 和 `local_import_sql` 是用户明确提供并交给服务器执行的 SQL，工具不会分析它们是否引用业务对象，也不会重放它们。应仅用于专用测试 fixture。

同一轮覆盖两种兼容模式和 OBProxy 时，给每个实际存在的入口单独配置，例如：

```json
{
  "targets": [
    {"name": "mysql-direct", "dsn_env": "OB_MYSQL_DSN"},
    {"name": "oracle-direct", "dsn_env": "OB_ORACLE_DSN"},
    {"name": "mysql-proxy", "dsn_env": "OBPROXY_MYSQL_DSN"},
    {"name": "oracle-proxy", "dsn_env": "OBPROXY_ORACLE_DSN"}
  ],
  "timeout": "2m",
  "cleanup_timeout": "30s"
}
```

所列目标均为必需，缺 DSN 在任何连接前直接退出。没有的入口应从配置中移除，不能当成已经覆盖。TLS、压缩、OB20、平台传输或认证组合也可增加不同 target，每个目标使用相应 DSN；工具不替用户创建域、证书或平台服务。

## 场景与自动数据

每个场景使用独立连接池及随机大写前缀 `OBT_` 对象，避免 Oracle 未加引号标识符的大小写差异。成功创建后登记清理；表、过程、函数和类型按依赖逆序删除。超时或按 Ctrl+C 后仍尝试清理，失败记入报告；强制杀进程、断电和远端失联无法保证自动清理。

| 场景 | 自动准备及断言 |
| --- | --- |
| `connect` | 认证、会话初始化、Ping、OceanBase 版本非空 |
| `crud` | 建表、插入/更新/读取/删除、受影响行数、模式对应的重复键错误码、错误后继续使用 |
| `counts-matched`、`counts-changed` | 自动建整数表，分别固定 MatchedRows/ChangedRows；普通 Exec、Execute 完成摘要和单项逐次批量入口均检查 INSERT、无变化/有变化/无匹配 UPDATE、DELETE/空 DELETE 的已知计数，MySQL 另检查 upsert 的插入/无变化/有变化三条分支；最后确认无残留行并清理表。ChangedRows 场景仅适用 MySQL，Oracle 使用原生匹配行数；每个入口重新准备数据，不比较不同数据库的计数差异 |
| `generated-keys` | MySQL 专用，自动建 unsigned BIGINT 自增主键表；普通 Exec 的 LastInsertId 与落库 ID 精确一致，Execute 摘要保留完整 uint64 十进制 ID；提高本轮表的自增起点后检查超 int64 的标准溢出错误及 Execute 精确值。每次 INSERT 后执行 UPDATE，确认不沿用生成 ID，最终核对四行并清理。无需假定自增步长或首个 ID；Oracle 的返回列由 returning 场景验证 |
| `prepare-server`、`prepare-client` | 分别使用服务端和客户端预处理，多次复用、Unicode/引号/反斜杠/NULL、Oracle 重复命名参数 |
| `scalars` | 精确 NUMBER/DECIMAL、Unicode、RAW/二进制、闰日、微秒时间、NULL，文本与参数化查询；完整比较日期及时间值（允许等价的尾随零，拒绝额外非零纳秒、超九位小数及尾随内容）。此场景显式使用 server prepare、关闭 ParseTime，按配置 location 构造输入墙上时间 |
| `mysql-scalars` | MySQL 专用，自动准备三组边界值及全 NULL 行；正负超24小时/零 TIME(6)、YEAR、完整 unsigned/signed BIGINT、TINYINT(1) 整数、BIT(1/9)、空/含零 VARBINARY、BINARY(4) 补零；文本与参数化读取、精确值/NULL/行数、数据库类型/ScanType 和二进制字节长度，显式 server prepare、ParseTime=false |
| `oracle-scalars` | Oracle 专用，自动创建 NVARCHAR2、TIMESTAMP(9) WITH TIME ZONE / LOCAL TIME ZONE 及 interval 列；公开类型绑定与读取、纳秒和原 TZ 偏移、LTZ 等价时刻、最大负 interval、ROWID 回绑定位及各列 typed NULL；文本与参数化读取。显式 server prepare、UTF-16 national charset，保留配置 location |
| `transactions` | 建表、提交/回滚、保存点回滚、只读拒绝写入、最终持久化行数 |
| `metadata` | 自建表的表/列/主键字典内容：catalog/schema/表身份、列名/顺序/类型/长度/精度/NULL、主键列；两列 ColumnTypes 名称/类型，以及缺行/多行拒绝 |
| `metadata-parameters` | 自建表和单行数据，描述带位置参数的 UPDATE、Oracle 重复命名参数 UPDATE 及无参数 SELECT；固定11列、物理/逻辑位置、规范化名称和未知方向、模式对应的 nullable、描述尺寸及原始协议字节。未知类型/尺寸保持未知，不要求参数描述等于业务列定义；每次描述后读取原值，最后确认仅有种子行并清理表。Metadata 始终采用服务端 prepare，即使目标选择客户端预处理 |
| `execution` | Execute、读取结果、提前 Close、完成摘要、原会话继续 Ping |
| `batch` | 五种优选策略的重复键定位、汇总/对应输入的错误码、完成数、stop/continue 状态及事务回滚；报告 `observations` 保留实际策略，回退不等于原生能力通过 |
| `batch-rewrite` | MySQL 专用，显式开启多语句并设置批量协商变量，要求实际策略为 Rewrite |
| `batch-array` | Oracle 专用，要求实际原生 array binding、汇总/对应输入的重复键错误码、完成数、逐项状态及事务回滚；要求 OceanBase ≥ 4.2.1，不能以顺序回退算成功 |
| `optimized-execution` | 显式 prepare-execute/statement cache/参数类型复用/checksum 配置，参数类型变化、NULL、重复键与读取结果；服务器内部是否采用具体优化仍取决于协商 |
| `reset-full` | 开启 Full Reset，标准 sql.Stmt 多轮借还后仍正确执行 |
| `multi-results` | MySQL 专用，显式允许多语句，两个结果集顺序/值/边界及原会话复用 |
| `trace` | 带应用 trace 和空 trace 的查询、客户端请求诊断；不宣称服务端链路可见性 |
| `long-data` | 自动生成约 150 KB 二进制和 Unicode 文本，流式上传、内容比较、回滚和提交 |
| `table-blob` | 自建表内 BLOB，原事务写入/读取/截断与回滚后内容 |
| `routines`、`routines-client` | 自动创建过程/函数，分别采用 server/client prepare，INOUT、OUT、函数结果；主动异常须为 MySQL 1644/Oracle 20001 且不发布输出，其他错误不能算通过 |
| `cursor`、`cursor-update` | 自建两行数据，前向/静态定位；更新、刷新、插入、删除、回滚后值与事务结束失效；每个单行批次验证完整内容、无多余行和后续读取错误 |
| `cursor-sensitive` | Oracle 专用，另一会话提交更新后重新定位读取；服务器敏感游标能力须可用 |
| `returning` | Oracle 专用，插入后的 RETURNING INTO 数值/字符串及行数 |
| `native-lob` | Oracle 专用，自动创建 BLOB/CLOB/NCLOB 数据，流式读写、Unicode 字符偏移、截断与事务寿命；此场景显式使用 UTF-16 national charset |
| `complex` | Oracle 专用，自动创建 OBJECT 和 TABLE OF 类型及表，精确数字/RAW、首尾不同的集合值及顺序/NULL、对象 OUT 的完整字段值/属性顺序/schema、事务回滚 |
| `resource-outputs` | Oracle 专用，自动创建过程和临时 LOB，REF CURSOR/BLOB/CLOB 输出内容与关闭；REF CURSOR 的单行批次拒绝多余行及后续读取错误 |
| `xa-one-phase`、`xa-two-phase`、`xa-rollback` | 自建表和唯一 XID，分支状态、prepare/recover、提交/回滚及持久化可见性 |
| `xa-suspend`、`xa-join`、`xa-recovery` | suspend/resume、end/join、关闭原物理连接后恢复；只操作本轮 XID，绝不回滚其他协调器的分支 |
| `session` | schema/catalog、会话变量、client info、修改会话后池隔离 |
| `warnings` | 非零协议 warning、详情或模式明确拒绝、清除快照 |
| `cancel-context` | 自建表、事务持锁、另一连接等锁到 Context 超时，验证原始值未变化及新连接可用 |
| `cancel-query` | 独立辅助连接取消等锁命令，校验明确错误码、原会话可用、token 一次性及未持久化副作用 |
| `change-user` | 使用配置的第二身份，验证身份切换、旧 statement 失效、标准池恢复原身份 |
| `local-import` | 自动建表、从内存 Reader 导入两行，逐值校验并分别回滚/提交，无须本地数据文件 |

执行器在所有创建/断言/权限/协议错误时明确失败，不把 ErrUnsupported 自动转成成功。工具没有定义每一种标量、元数据过滤、所有平台认证、HA 故障和性能压力的全部组合；上表是实际覆盖范围，其他验收要求仍见[开发计划](go-driver-development-plan.md)和[兼容矩阵](go-driver-compatibility-matrix.md)。

## 报告与退出码

标准输出逐项打印 target、场景、状态和耗时。JSON 保存运行 ID、工具链/系统、所选范围、实际模式、每项结果、实际批量策略及对象清理状态。对象状态包括 `dropped`、`cleanup_failed`、`create_unconfirmed`；XA 还保留本轮 XID 和分支处理结果。结果未知或恢复失败时保留标识供人工核对，不自动重试业务 SQL。

| 退出码 | 含义 |
| --- | --- |
| `0` | 所选范围内适用场景全部通过，无缺配置跳过；不表示全平台/全版本验收 |
| `1` | 执行/断言/清理失败，或 require_all 下有跳过 |
| `2` | 命令行、配置或报告文件错误 |
| `3` | 没有失败，但有缺 fixture 的跳过，或没有任何适用场景通过 |

Oracle 专用场景在 MySQL 目标上标为 `not_applicable`，反之亦然；与缺配置 `skip` 分开。目标无法连接时，该目标记失败，其余场景明确标为未执行，其他配置目标继续。成功中断的清理不能证明服务端已经停止所有在途 SQL；未知 XA 结果须依据报告中的本轮 XID 核对。

## 本阶段验证

工具自身的离线回归验证严格配置、完整 CRUD 编排、错误数据不能通过、部分建表失败后的所有权清理、取消后独立清理、清理失败传播、XA 原会话清理及取消错误码识别。它们用进程内 fixture 校验执行器，不是实际数据库验收证据；可执行程序不依赖这些 fixture 或 Go testing。

[TestScalarsTemporalAssertions](../cmd/obtest/scalars_test.go) 在两种兼容模式的文本与参数化查询上覆盖32个离线流程：精确值、等价小数尾零、日期时间变化、额外纳秒、超九位小数和非法尾部。包含单数字小时缩短整串后夹带超精度小数的拒绝回归。每个流程检查实际场景状态及自建表清理；Oracle DATE 必须保留预期的午夜，不能只比较日期前缀。本地 fixture 不证明真实服务端的日期/精度行为。

[TestComplexOutputAssertions/TestComplexCollectionAssertions](../cmd/obtest/complex_test.go) 覆盖20个离线流程，验证 OBJECT OUT 的数值/RAW/schema/属性顺序，以及集合首尾不同值、错序/重复/缺项/NULL 和对象/集合 schema。每个流程检查事务回滚及表、集合类型、对象类型的逆序清理；等价十进制尾零允许通过。夹具只替代数据库依赖，不证明真实类型描述或服务端往返。

[TestMetadataContentWorkflow/TestMetadataSchemaFilters](../cmd/obtest/metadata_test.go) 提供90个离线流程：两模式验证自建 ID/V 表的完整表/列/主键字典身份、列顺序、INTEGER 精度/scale、VARCHAR 长度与 NULL，以及两列结果元数据；错误值、缺行/多行、未知必需属性、读取/关闭错误均失败。另检查非空 schema 的 LIKE 转义和精确大小写匹配；每次结果均关闭，完整场景在失败后仍删除自建表。旧实现仅计数时50个错误输入被误判通过；夹具不代表真实字典或权限已经验收。

[TestCursorBatchAssertions](../cmd/obtest/cursor_test.go) 提供22个离线流程，覆盖两列普通游标与单列 REF CURSOR 的单行批次校验：精确值、缺行、重复/额外行、首行后的读取错误、关闭错误、非法/NULL 标识符、列宽及内容错误；全部验证结果关闭和场景失败后的表清理。旧实现6个错误批次被误判通过，修复后拒绝；共享断言用于前向/静态、刷新、敏感游标和过程资源输出，不代表真实服务端 fetch 已验收。

[TestRoutineFailureAssertions](../cmd/obtest/routines_test.go) 提供48个离线流程，覆盖两模式和 server/client 两个场景选择；完整创建三份例程、两轮 INOUT/OUT、函数调用和主动异常，验证参数方向/初值与逆序清理。预期错误和包装错误通过；语法/权限/例程缺失/约束/其他模式/零错误码，以及意外成功、传输失败、超时、失败输出发布均失败。旧实现24个无关服务器错误被误判通过，修复后拒绝；集成例程的同类断言同步收紧，本轮仅编译。

[TestRowsAffectedWorkflows](../cmd/obtest/counts_test.go) 提供51个离线流程，使用标准库内存连接与独立字节协议夹具，通过真实公开驱动执行计数场景，检查 CLIENT_FOUND_ROWS、三种入口、异常计数/零、超 int64 计数、服务端错误及失败后的自建表清理。Execute 计数溢出导致原会话淘汰时，夹具还检查新连接初始化后只清理已登记表。SQL 均无参数，批量为单项 Sequential，用于隔离计数契约；不代表多项批量、参数绑定或服务端 SQL 语义已验收。

[TestParameterMetadataWorkflow](../cmd/obtest/parameter_metadata_test.go) 提供53个离线流程，检查元数据请求的原始 Query/Kind、完整列和参数身份、已报告或未知描述、原始方向字节、Oracle 重复名称，以及错位/缺行/多行、非法 nullable/尺寸/协议范围、读取/关闭失败和种子被修改时的失败传播；已打开结果均关闭，失败后仍清理自建表。该夹具替代公开 Metadata 的返回结果，验证工具编排和断言；服务端 prepare、临时 statement 关闭及 PrepareClient 下仍服务端描述由既有[驱动协议测试](../metadata_test.go)覆盖，不把这些离线证据当作真实参数推断或 SQL 行为验收。

[TestDuplicateKeyWorkflows / TestBatchDuplicateAssertions](../cmd/obtest/duplicate_test.go) 分别提供56个完整工具流程和80个批量结果校验场景。CRUD/优化执行检查正确、包装或同类合并的重复键错误、错误模式/语法/权限/缺表/数据超长/零码、意外成功、传输和取消错误，并检查自建表清理；批量校验汇总与对应输入的错误码、错误位置、完成数和 stop/continue 状态，不能由汇总错误掩盖不同的输入错误。遍历合并错误的全部分支，重复键伴随清理/传输等其他错误时仍判失败；回归先复现误判，再修复通过。工作流使用替代的 SQL 驱动，批量测试直接提供结果；不证明优化或原生 array binding 协议和真实 SQL 已验收。对应[集成执行策略](../tests/integration/execute_strategies_test.go)和[集成批量](../tests/integration/batch_test.go)同步加强断言；[集成辅助分类回归](../tests/integration/env_test.go)不连接数据库，其余数据库流程仅编译。

自建主键的重复键错误码按固定 OceanBase [OB_ERR_PRIMARY_KEY_DUPLICATE 定义](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.def#L695)判断：MySQL 模式的 [ER_DUP_ENTRY 为1062](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/mysql_errno.h#L79)，Oracle 模式为1；不依赖消息文字或从 SQLSTATE 推断。原生 array binding 的诊断可能没有 SQLSTATE，因此只要求其真实错误码与输入归属。

错误码按固定 OceanBase 源码核对：[未处理的 SIGNAL](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.def#L1183)映射到[ER_SIGNAL_EXCEPTION 1644](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/mysql_errno.h#L661)；[Oracle 错误码转换](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/src/share/ob_errno.cpp#L27832)保留应用异常号码，并将负值转换为协议正值，所以自建 `RAISE_APPLICATION_ERROR(-20001,...)` 应为20001。源码和离线夹具不替代实际版本/代理的联调证据。

[TestMySQLScalarsWorkflow](../cmd/obtest/mysql_scalars_test.go) 提供44个离线流程：两条读取路径上的完整边界值、最大整秒/零 TIME 的两种合法表示及非零越界小数、整数溢出/精度、TINYINT 非 bool、TIME 尾部、BIT 宽度、YEAR 零值与空二进制非 NULL、定长补零、缺行/多行、NULL 及错误元数据。检查原始输入类型、查询参数、Rows 关闭及自建表清理；夹具元数据和行值不证明实际服务端行为。

TIME 种子以最大整秒 `838:59:59` 和 `-838:59:58.999999` 分别覆盖范围与微秒；固定 OceanBase 的[TIME_MAX_VAL](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/deps/oblib/src/lib/timezone/ob_time_convert.h#L159)和[越界处理](https://github.com/oceanbase/oceanbase/blob/7b0f43693565654bb1d7343f728bc2013dfff959/deps/oblib/src/lib/timezone/ob_time_convert.cpp#L5451)表明最大整秒后非零小数会报错或截断，不能作为正常种子。整秒输出可带六位零微秒；非零/额外尾部仍拒绝。原 integration 标量用例同步修正种子，本轮仅编译。

[TestOracleScalarsWorkflow](../cmd/obtest/oracle_scalars_test.go) 提供32个离线流程：两条读取路径上的精确值、等价 LTZ offset、错误 national text、TZ 偏移/精度、LTZ 时刻、interval、ROWID 绑定/定位及五种 typed NULL。检查场景状态、自建表清理及公开类型输入；夹具不证明真实服务端支持这些类型。

[复杂类型 integration 用例](../tests/integration/complex_test.go) 同步使用不同集合种子和完整 OUT 内容断言；DATE 只接受预期整秒及驱动可能输出的六位零微秒表示，7项离线正负例验证该断言。数据库流程本轮仅编译，未执行。

生成键场景在自建表上执行 `ALTER TABLE ... AUTO_INCREMENT=9223372036854775808`，需要对应 DDL 权限及目标版本对 unsigned BIGINT 自增范围的支持；不会修改全局或租户自增配置。标准 `LastInsertId` 返回 int64，因此超范围时仅检查溢出错误及落库范围，完整 ID 的精确比较由 Execute 摘要完成。31个公开驱动与内存协议对端流程检查断言、边界及失败清理，不证明实际版本的自增分配行为。

Windows amd64 的 Go 1.25.0/1.26.0 全包构建、单测、vet 和 integration 仅编译已通过；工具回归重复运行、普通及 integration 标签全仓库 golangci-lint、Windows/386、`-list`、配置 `-check` 和文档检查的逐次证据见[开发进度](go-driver-development-progress.md)。工具提供44项场景；CI 仅增加不连接数据库的 `-list` 检查。

本次不连接数据库，因此上述 44 项真实运行结果均待用户提供测试 DSN 后执行，不能依据编译、离线检查或单元测试提高数据库验收计数。
