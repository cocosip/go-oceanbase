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
| `cases` | 全部 38 项 | 选填场景名称数组；拼写错误、重复项直接拒绝，不允许静默漏测 |
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
| `crud` | 建表、插入/更新/读取/删除、受影响行数、重复键结构化错误、错误后继续使用 |
| `prepare-server`、`prepare-client` | 分别使用服务端和客户端预处理，多次复用、Unicode/引号/反斜杠/NULL、Oracle 重复命名参数 |
| `scalars` | 精确 NUMBER/DECIMAL、Unicode、RAW/二进制、闰日、微秒时间、NULL，文本与参数化查询；完整比较日期及时间值（允许等价的尾随零，拒绝额外非零纳秒、超九位小数及尾随内容）。此场景显式使用 server prepare、关闭 ParseTime，按配置 location 构造输入墙上时间 |
| `transactions` | 建表、提交/回滚、保存点回滚、只读拒绝写入、最终持久化行数 |
| `metadata` | 自建表的表/列/主键字典投影，ColumnTypes 名称与数据库类型 |
| `execution` | Execute、读取结果、提前 Close、完成摘要、原会话继续 Ping |
| `batch` | 五种优选策略的重复键定位、stop/continue、事务回滚；报告 `observations` 保留实际策略，回退不等于原生能力通过 |
| `batch-rewrite` | MySQL 专用，显式开启多语句并设置批量协商变量，要求实际策略为 Rewrite |
| `batch-array` | Oracle 专用，要求实际原生 array binding、逐项重复键状态及事务回滚；要求 OceanBase ≥ 4.2.1，不能以顺序回退算成功 |
| `optimized-execution` | 显式 prepare-execute/statement cache/参数类型复用/checksum 配置，参数类型变化、NULL、重复键与读取结果；服务器内部是否采用具体优化仍取决于协商 |
| `reset-full` | 开启 Full Reset，标准 sql.Stmt 多轮借还后仍正确执行 |
| `multi-results` | MySQL 专用，显式允许多语句，两个结果集顺序/值/边界及原会话复用 |
| `trace` | 带应用 trace 和空 trace 的查询、客户端请求诊断；不宣称服务端链路可见性 |
| `long-data` | 自动生成约 150 KB 二进制和 Unicode 文本，流式上传、内容比较、回滚和提交 |
| `table-blob` | 自建表内 BLOB，原事务写入/读取/截断与回滚后内容 |
| `routines`、`routines-client` | 自动创建过程/函数，分别采用 server/client prepare，INOUT、OUT、函数结果、出错不发布输出 |
| `cursor`、`cursor-update` | 自建两行数据，前向/静态定位；更新、刷新、插入、删除、回滚后值与事务结束失效 |
| `cursor-sensitive` | Oracle 专用，另一会话提交更新后重新定位读取；服务器敏感游标能力须可用 |
| `returning` | Oracle 专用，插入后的 RETURNING INTO 数值/字符串及行数 |
| `native-lob` | Oracle 专用，自动创建 BLOB/CLOB/NCLOB 数据，流式读写、Unicode 字符偏移、截断与事务寿命；此场景显式使用 UTF-16 national charset |
| `complex` | Oracle 专用，自动创建 OBJECT 和 TABLE OF 类型及表，精确数字/RAW、首尾不同的集合值及顺序/NULL、对象 OUT 的完整字段值/属性顺序/schema、事务回滚 |
| `resource-outputs` | Oracle 专用，自动创建过程和临时 LOB，REF CURSOR/BLOB/CLOB 输出内容与关闭 |
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

[复杂类型 integration 用例](../tests/integration/complex_test.go) 同步使用不同集合种子和完整 OUT 内容断言；DATE 只接受预期整秒及驱动可能输出的六位零微秒表示，7项离线正负例验证该断言。数据库流程本轮仅编译，未执行。

Windows amd64 的 Go 1.25.0/1.26.0 全包构建、单测、vet 和 integration 仅编译已通过；最终工具回归分别重复 10 轮，两个工具链的普通及 integration 标签全仓库 golangci-lint 均为 0 issues。`-list`、示例配置 `-check`、38 场景名称唯一性及文档链接检查通过；CI 仅增加不连接数据库的 `-list` 检查。

本次不连接数据库，因此上述 38 项真实运行结果均待用户提供测试 DSN 后执行，不能依据编译、离线检查或单元测试提高数据库验收计数。
