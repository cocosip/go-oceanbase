# OceanBase Go 驱动开发进度

最后更新：2026-10-17。任务定义见 [开发计划](go-driver-development-plan.md)，行为契约见 [架构设计](go-driver-architecture-design.md)。

## 1. 当前状态

- 当前阶段：配置、标准包流、握手/认证、建连/Ping 和标准文本执行、完整 Rows 取消/连接关闭协调、词法绑定、服务端预处理/二进制执行及事务/保存点/reset、客户端预处理/escape 已实现；继续 D11。
- 实现完成：**10 / 32 项**；验收完成：**0 / 32 项**。分母为开发计划 D01–D32，不是代码行数、工时或功能数量估算。
- 当前执行任务：**D11 Execute、多结果摘要与计数策略**。
- 仓库已有：设计文档、Go module、注册/配置/DSN、外部日志、标准包流/故障对端、握手/认证/TLS/实际模式、文本执行/Rows/计数、词法绑定及参数转换、服务端预处理/二进制收发、事务/保存点/reset、包依赖守卫及 CI 定义。
- 仓库尚无：Execute 摘要及高级扩展；D11–D32 仍需实现。
- 工具链：已实际使用 Go 1.25.0、1.26.0 运行 Windows amd64 测试/vet；Linux 与 GitHub CI 尚未执行。OceanBase/OBProxy 版本及平台认证环境未确认。
- 当前执行约束：按用户要求，**暂不进行数据库联调**；集成条目保留未验证，不作为本阶段的开发阻塞，也不计入验收通过。

上述外部条件不阻塞 D01/D02 的本地开发；需要真实环境的任务先完成可独立验证的实现，再如实记录待联调条件。不得据此缩减 Oracle 兼容模式或其他高级能力范围。

## 2. 状态、计数和更新规则

### 2.1 三个独立状态

| 维度 | 可用状态 | 含义 |
| --- | --- | --- |
| 开发 | 未开始 / 进行中 / 已实现 | 已实现要求代码、接口、相关本地测试及必要文档齐备；不能只有声明或空实现 |
| 验证 | 未验证 / 本地通过 / 待联调 / 验收通过 / 失败 | 验收通过要求 §5.3 绑定的全部必需条目满足 §5.2 聚合规则；本地通过不等于真实服务端兼容 |
| 阻塞 | 无 / 依赖 / 环境 / 契约 | 记录原因、影响项、可继续工作和解除条件；不把所有未来任务都标为阻塞 |

典型流转为“未开始 → 进行中 → 已实现”，验证从本地测试推进到真实环境验收。服务端暂缺时可以是“已实现 / 待联调 / 环境”，但不能计入验收完成。发现失败时保留已知事实，验证改为失败；只有实现也不满足契约时才退回进行中。

### 2.2 进度计算

- 实现完成数：开发列为“已实现”的任务数 / 32。
- 验收完成数：开发列为“已实现”且验证列为“验收通过”的任务数 / 32。
- 模式/版本组合只在验证表记录，不拿一个 MySQL 组合通过代替整项通过。
- D32 是完整交付汇总，本地 CI 通过不足以完成 D32；不能用“总任务大多实现”替代全功能声明。
- 文档编写不计入驱动实现进度；不使用任务平均值伪装剩余工期。
- 拆分任务时保留旧 ID 的归属关系并调整分母；不能删除未完成项提高进度。

### 2.3 每次开发后必须更新

1. 更新任务行的开发/验证/阻塞状态和负责人；有实际开始/完成时才填写日期。
2. 填写实际代码/测试位置、命令及通过/失败/跳过结果；不写尚未执行的成功结论。
3. 更新相关验证组合、里程碑、总计及下一步；多个状态相冲突时先修正再结束本次开发。
4. 环境问题记录具体缺少的能力和影响范围，不记录密码、DSN、机器绝对路径或敏感抓包。
5. 接口、依赖、默认行为变化同步到架构/开发计划；不另建走查记录文档。
6. D01 建立双工具链/平台 CI，D02 增加依赖守卫和 fuzz/386，D04 增加 require 集成入口，D06 增加 race；相关任务的验收证据必须含对应 CI 运行，不能推迟到 D32。

## 3. 任务看板

“—”表示尚无负责人或执行证据；依赖对应开发计划，后续任务目前均未开始。依赖存在不等于任务已发生阻塞。

| ID | 任务 | 直接依赖 | 开发 | 验证 | 阻塞 | 负责人 / 证据 |
| --- | --- | --- | --- | --- | --- | --- |
| D01 | 模块、公开配置与日志注入 | 无 | 已实现 | 本地通过 | 无 | Codex；见 §5.5 D01 |
| D02 | 标准包流与故障对端 | D01 | 已实现 | 本地通过 | 无 | Codex；见 §5.5 D02 |
| D03 | 握手、基础认证、TLS 与模式 | D02 | 已实现 | 本地通过 | 无 | Codex；见 §5.5 D03 |
| D04 | 建连、初始化、Ping、集成入口 | D03 | 已实现 | 本地通过 | 无 | Codex；见 §5.5 D04 |
| D05 | 文本执行、Rows 与标准计数 | D04 | 已实现 | 本地通过 | 无（待联调） | §5.6 D05 |
| D06 | 取消、错误边界、连接淘汰 | D05 | 已实现 | 本地通过 | 无（待联调） | §5.6 D06 |
| D07 | 词法绑定与参数转换 | D05 | 已实现 | 本地通过 | 无 | §5.6 D07 |
| D08 | 服务端预处理、二进制结果 | D06、D07 | 已实现 | 本地通过 | 无（待联调） | §5.6 D08 |
| D09 | 事务、保存点、reset 代次 | D08 | 已实现 | 本地通过 | 无（待联调） | §5.6 D09 |
| D10 | 客户端预处理、escape | D07、D08 | 已实现 | 本地通过 | 无（待联调） | §5.6 D10 |
| D11 | Execute、多结果、计数策略 | D08、D09 | 进行中 | 未验证 | 无 | — |
| D12 | 全类型、字符集、时间 | D08、D10 | 未开始 | 未验证 | 无 | — |
| D13 | 结构/结果/参数元数据 | D09、D12 | 未开始 | 未验证 | 无 | — |
| D14 | 标量 OUT/INOUT/RETURNING | D11、D12 | 未开始 | 未验证 | 无 | — |
| D15 | 固定会话资源、代次与租约 | D09、D11 | 未开始 | 未验证 | 无 | — |
| D16 | 原生 BLOB/CLOB/NCLOB | D12、D13、D15 | 未开始 | 未验证 | 无 | — |
| D17 | MySQL 表内 BLOB 模拟 locator | D13、D16 | 未开始 | 未验证 | 无 | — |
| D18 | OBJECT/ARRAY、类型描述 | D12、D13、D16 | 未开始 | 未验证 | 无 | — |
| D19 | 游标、滚动、刷新和更新 | D13、D15、D16 | 未开始 | 未验证 | 无 | — |
| D20 | REF CURSOR/流式 LOB 输出 | D14、D16、D19 | 未开始 | 未验证 | 无 | — |
| D21 | 长参数、分段上传 | D08、D12、D15 | 未开始 | 未验证 | 无 | — |
| D22 | 批量、bulk、array binding | D11、D14、D18、D21 | 未开始 | 未验证 | 无 | — |
| D23 | 压缩、OB20、新元数据、校验 | D06、D08 | 未开始 | 未验证 | 无 | — |
| D24 | PrepareExecute、缓存、优化 | D08、D11、D14、D21、D23 | 未开始 | 未验证 | 无 | — |
| D25 | 诊断、profiling、FLT | D11、D23 | 未开始 | 未验证 | 无 | — |
| D26 | 静态多地址、负载、故障切换 | D06、D09 | 未开始 | 未验证 | 无 | — |
| D27 | TNS/OCP/拓扑发现 | D26 | 未开始 | 未验证 | 无 | — |
| D28 | 完整认证、平台传输 | D03、D26 | 未开始 | 未验证 | 无 | — |
| D29 | LOCAL INFILE 受控导入 | D06、D21 | 未开始 | 未验证 | 无 | — |
| D30 | 会话辅助、warning、切用户、取消 | D09、D11、D15、D26、D28 | 未开始 | 未验证 | 无 | — |
| D31 | XA 分支、状态与恢复 | D09、D14、D18、D30 | 未开始 | 未验证 | 无 | — |
| D32 | 配置逐项迁移、完整验收与交付 | D01–D31 | 未开始 | 未验证 | 无 | — |

## 4. 里程碑

| ID | 目标 | 实现项 | 验收项 | 状态 |
| --- | --- | --- | --- | --- |
| G1 | 建连与标准查询，D01–D05 | 5/5 | 0/5 | 进行中 |
| G2 | 执行与会话正确性，D06–D12 | 3/7 | 0/7 | 进行中 |
| G3 | 完整扩展数据能力，D13–D22 | 0/10 | 0/10 | 未开始 |
| G4 | 协议与运行能力，D23–D30 | 0/8 | 0/8 | 未开始 |
| G5 | 分布式事务，D31 | 0/1 | 0/1 | 未开始 |
| G6 | 完整交付，D32 | 0/1 | 0/1 | 未开始 |

里程碑仅在全部所属任务验收通过且前置契约满足后完成。G1–G5 任一完成都不能单独表述为“全功能驱动完成”。

## 5. 验证组合与证据

### 5.1 目标与必需验证集

真实环境固定四个目标 ID：P-MD（MySQL 兼容模式直连）、P-OD（Oracle 兼容模式直连）、P-MP（MySQL 兼容模式 + OBProxy）、P-OP（Oracle 兼容模式 + OBProxy）。对应计划中的 mysql/oracle/proxy-mysql/proxy-oracle profile；当前四者均为**版本未确认、未验证**。D03 建立兼容矩阵，D04 集成验收前登记首组精确 OceanBase/OBProxy 版本及平台；后续新增能力在开始任务时登记它的正向支持目标和必需拒绝目标。不能等测试结果出来后才选择目标。

以下验证集是最低要求。每个集展开为独立能力条目，每条包含目标 ID、精确版本、Go 补丁版、OS/架构、执行/包流/认证路径和命令。V01–V14 当前均未验证；这里的定义不是执行证据。

| ID | 必需验证内容 |
| --- | --- |
| V01 | 本地基线：Go 1.25.x/1.26.x × Linux/Windows amd64 的 test/vet、锁定版 lint；D01 起 list 检测循环，D02 起架构守卫检查含 integration/test 的允许依赖边；每项相关测试及全包测试 |
| V02 | 该任务协议/内核失败路径：在已拥有的最低入口验证，D02 用 net.Conn + PacketIO/故障对端，D03 用握手编排 + 算法/对端；D04 起涉及标准驱动接口的场景用真实 database/sql + 故障对端，D16/D18 的 Query/Next 锁边界不得以直接 helper 单测替代。记录命令数、响应边界、错误/清理和无重放；纯配置/词法项仅测其可观察边界，不伪造网络覆盖 |
| V03 | Linux amd64、两工具链的 race 与该任务资源/并发压力；若某项平台原生适配只在 Windows 工作，另在受支持 Windows race 环境验证它；不能用交叉编译代替运行 |
| V04 | 该任务新增解析器的 seed/边界/fuzz，单个 fuzz 目标至少 30 秒；Go 两版本的 Linux/386 长度、索引及整数边界运行，不能仅编译 |
| V05 | P-MD、P-OD 的真实正向/错误/事务语义；只验证该任务拥有的行为，D04 是建连/初始化/Ping，D05 起增加实际 SQL |
| V06 | P-MP、P-OP 的对应真实行为，含能力协商和直连语义一致性；不能用直连代替代理 |
| V07 | Oracle 原生高级能力：P-OD、P-OP 的 LOB/复杂类型/游标/过程输出等该任务场景；另固定 MySQL 模式中对应公开入口的支持或拒绝行为，不能静默模拟成功 |
| V08 | 表内 BLOB 模拟：P-MD、P-MP 的完整主键/分块/锁/事务/ChangedRows 场景；Oracle 入口原生解析由 V07 验证 |
| V09 | 四个目标上标准包、压缩、OB20/校验/新元数据的协商、正向及拒绝；按 Java 条件登记正向支持目标；D25 同时交叉 FLT 开关和 trace 隔离 |
| V10 | 该任务能力在文本/服务端预处理/客户端预处理/PrepareExecute 的等价性；只绑定已交付执行路径，D08 起服务端，D10 起客户端，D14 为 RETURNING 交付必要合并基础路径，D24 扩展通用合并策略；每条登记实际模式/目标，后续路径任务回归前序能力 |
| V11 | 四目标的地址/角色/HA 故障；D26 静态策略、D27 再覆盖 TNS/OCP/复制拓扑；新建连接切换、总预算、认证拒绝、活动会话不换线及刷新关闭 |
| V12 | D28 的逐算法、逐平台传输清单，见 §5.4；包括正向运行、安全拒绝、切换/阶段状态和清理，不能只编译或选一个插件代表全部 |
| V13 | 四目标的 XA 1PC/2PC、flags、join/resume/suspend、断线后新连接恢复、未知结果不重放及连接回池边界；真实支持条件逐条留证 |
| V14 | 全部已声明正向目标上的外部示例可运行、逐项 option/M01–M16 核对、正确性后的基准和资源稳定性；复核所有前序必需条目，无未验证目标 |

V10 不要求 D08 验证尚未实现的 D24。D24 回归当时已经落地的前序能力；D13–D22 中若有任务在 D24 后才实现，该任务必须覆盖已落地的合并路径，D32 最终核对全量，不能因此形成 D24 与后续任务的隐性循环依赖。不存在的协议能力按 §5.2 做能力拒绝验收；后续路径、版本及平台新增时扩充条目，不把既有目标移出必需集合。

### 5.2 条目状态与聚合规则

| 条目状态 | 判断依据 | 对任务验收的影响 |
| --- | --- | --- |
| 未验证 | 尚无对应运行证据 | 不满足 |
| 通过 | 预登记组合的断言全部通过，证据齐全 | 满足该条目 |
| 失败 | 有断言失败或实际行为不符合契约 | 不满足；不能被其他组合通过覆盖 |
| 环境缺失 | 缺服务、版本、凭据、平台或必要对象 | 不满足，任务待联调；不能写不适用 |
| 服务端已验证不支持 | 源码条件/实际协商或最小用例及能力拒绝测试证明目标不支持 | 只满足该目标的负向能力条目，不证明正向实现完成 |
| 不适用 | 架构确定该能力不属于此组合，并已登记具体理由及替代覆盖 | 只排除该条目；不是未实现/缺环境/未知版本的别名 |

同一任务中正向实现、负向能力判断分别记录。一项功能至少在预先登记的支持目标完成正向运行，才能验收；如果所有目标都不支持或尚未知，不得仅靠正确拒绝把功能计为已完成。调整已有范围必须同步架构与计划、说明证据，不在 D32 临时删减目标。

“驱动未实现”是开发列状态，不是验证的跳过理由。一个组合只包含一种结果，混合结果拆行。任务“验收通过”要求：开发已实现，§5.3 指定验证集已展开且全部必需条目满足，没有失败/环境缺失/未验证，各功能具有正向覆盖；D01/D02/D03/D07 这类本地任务以其本地契约验收，后续集成任务不能引用它们冒充真实兼容。D32 还要补齐后续执行路径对前序功能的回归，不只读取历史任务绿灯。

### 5.3 逐任务必需验证集

任务特有场景取自开发计划 Dxx 的四步清单，本表确定哪些组合必须执行。本表定义必需条目，实际结果见下方任务证据；验收条目 ID 使用 `Dxx/Vxx/能力/目标/路径/平台`，不能只给 Vxx 一个汇总成功状态。

| 任务 | 必需验证集 | 额外约束 |
| --- | --- | --- |
| D01 | V01 | 首次建立 CI；配置、DSN、日志隔离；本地任务 |
| D02 | V01、V02、V04 | 包流与标准库 testutil；依赖守卫拒绝反向导入；本地任务 |
| D03 | V01、V02、V04 | 基础认证/TLS/模式的协议与安全边界；真实认证由 D04/D28 补齐 |
| D04 | V01、V02、V05、V06 | 基础认证正向、失败及模式；建立 require 集成 job |
| D05 | V01、V02、V05、V06 | 文本 CRUD/Rows/计数/错误与提前关闭 |
| D06 | V01、V02、V03、V05、V06 | 取消、发送边界、淘汰和 CI race |
| D07 | V01、V02、V04 | 词法/绑定及发送前拒绝；本地任务，SQL 往返由 D08/D10 覆盖 |
| D08 | V01、V02、V04、V05、V06、V10 | 服务端预处理、二进制与缓存代次 |
| D09 | V01、V02、V03、V05、V06 | 隔离级别/只读/保存点/reset 与旧句柄 |
| D10 | V01、V02、V04、V05、V06、V10 | 客户端预处理/escape，与服务端路径等价 |
| D11 | V01、V02、V03、V05、V06、V10 | 混合结果、摘要、最终化与不确定结果 |
| D12 | V01、V02、V04、V05、V06、V10 | 全类型、字符集、精度、时区和 NULL |
| D13 | V01、V02、V05、V06 | 真实字典/参数元数据、权限、内外字典调用边界 |
| D14 | V01、V02、V05、V06、V10 | OUT/INOUT/RETURNING 逐能力，含必要合并基础协议；两模式支持及拒绝 |
| D15 | V01、V02、V03、V05、V06 | 资源身份、代次、父租约及幂等清理 |
| D16 | V01、V02、V03、V07 | LOB 正向；Query/Next 内物化无锁重入、原事务可见性 |
| D17 | V01、V02、V03、V07、V08 | 原生/模拟后端分别验收，表锁/主键/无变化 UPDATE |
| D18 | V01、V02、V04、V07、V10 | 描述缓存未命中 Query/Next 无锁重入；复杂/嵌套 LOB 往返 |
| D19 | V01、V02、V03、V05、V06、V07 | static/sensitive/update 分条，含刷新和行定位 |
| D20 | V01、V02、V03、V07 | REF CURSOR/流式 LOB OUT、输出发布及父 statement 寿命 |
| D21 | V01、V02、V03、V05、V06、V10 | LONG_DATA/PIECE_DATA、Reader 失败与无重传 |
| D22 | V01、V02、V03、V05、V06、V07、V10 | 顺序/改写/bulk/array binding、部分成功与 OUT |
| D23 | V01、V02、V04、V09 | 标准包/压缩/OB20、校验/包上限及组合协商 |
| D24 | V01、V02、V03、V05、V06、V09、V10 | 合并执行及已落地前序能力的路径回归；其余由所属任务/D32 补齐 |
| D25 | V01、V02、V03、V05、V06、V09 | slog/profiling/FLT 脱敏、nil 静默及跨请求隔离 |
| D26 | V01、V02、V03、V05、V06、V11 | 静态选址、角色和无活动会话迁移 |
| D27 | V01、V02、V03、V04、V11 | TNS/OCP/复制拓扑各有真实发现及关闭证据 |
| D28 | V01、V02、V03、V12 | §5.4 每个算法和传输独立验收；原生目标实际运行 |
| D29 | V01、V02、V05、V06 | LOCAL INFILE 授权、失败/取消及事务行为 |
| D30 | V01、V02、V03、V05、V06 | 辅助通道 TLS、ChangeUser、warnings 和身份隔离 |
| D31 | V01、V02、V03、V13 | 两模式、直连/代理、真实恢复及状态/flags 拒绝 |
| D32 | V01–V14 | 完整复跑、配置逐项映射/示例/基准；不缩减必需条目 |

### 5.4 D28 认证与传输子清单

以下每行均需独立证据，当前均未验证。开始 D28 时在兼容矩阵登记精确目标版本、每种算法支持目标及平台；真实平台需求不能从名称猜测，也不能用平台不存在解释驱动尚未实现。

| 子项 | 必需目标/场景 |
| --- | --- |
| native password | 四目标中预登记支持组合；初始/切换、错误密码 |
| SHA-256 | 支持组合；TLS 与固定公钥加密、无可信公钥拒绝 |
| caching SHA-2 | 支持组合；fast/full-auth、缓存失效、TLS 门禁 |
| clear password | 支持组合；TLS 正向、无 TLS 零凭据字节 |
| old password | 支持组合；显式启用、默认拒绝、错误挑战 |
| Ed25519 | 支持组合；正向、无效挑战/密钥和会话状态隔离 |
| GSSAPI / Windows SSPI | 各声明支持的 Linux/Windows 原生环境；身份、票据错误、多阶段/切换与清理分别记录 |
| PAM | 支持平台/服务；对话各阶段、TLS 拒绝、取消与清理 |
| TCP | Linux/Windows amd64；两模式直连及代理，TLS/断线/超时 |
| Unix socket | Linux 原生运行；支持模式、身份、取消和清理 |
| Named Pipe | Windows 原生运行；支持服务、身份、取消和清理 |
| Shared Memory | Windows 原生运行；支持服务、边界、取消和清理 |
| OBProxy VC | 对应实现所支持平台/代理版本；连接、认证、读写、关闭 |

每种算法再按初始/切换/多阶段安全要求拆条；共享门禁在 D28 覆盖重新认证阶段，公开 ChangeUser 的真实组合在 D30 验证，避免反向依赖。每种传输需完成真实收发，不只验证 Dial 返回非 nil。所有**声明支持**的算法/平台/服务组合必须正向通过；不支持的交叉组合有取证和拒绝行为，不要求无意义的全笛卡尔积。至少保留上述每种能力的一个正向支持目标，缺失目标时该能力待联调。D28 可以先完成某些子项，但整个任务不能因此验收通过。

### 5.5 单项验证条目格式

在所属任务开始后按下面模板填写，内容留在本文件相应任务的证据条目中；大型日志由测试系统保存，本表引用稳定的相对路径或 CI 链接，不提交敏感原始日志。

```text
任务：Dxx
负责人 / 开始日期 / 最近更新：
代码与测试：仓库相对路径、测试名
已实现范围：具体 API、模式、执行路径
必需条目：Dxx/Vxx/能力/目标/路径/平台；正向/负向，版本基线
条目结果：未验证/通过/失败/环境缺失/服务端已验证不支持/不适用
不适用或能力拒绝：预登记理由、依据与该功能的正向覆盖位置
本地检查：实际命令、Go/OS/架构、通过/失败/跳过结果
集成检查：OceanBase/OBProxy 版本、模式、路径、命令、结果
故障/资源：对应断言及结果；无适用项时写明理由
未验证范围：实际缺少的环境或用例
阻塞与解除条件：原因、影响任务、可继续内容、下一动作
下一步：一个可以直接执行的动作
```

### 5.6 已实现任务的本地证据

#### D01 配置与日志

- 开发：已实现；验证：本地通过。代码：`config.go`、`dsn.go`、`connector.go`、`driver.go`、`logging.go`；测试：`config_test.go`、`dsn_test.go`、`logging_test.go`。
- RED：默认值、无效配置、严格 DSN、快照隔离和 logger 断言均在功能空实现上实际失败；GREEN：Go 1.25.0/1.26.0 Windows amd64 的 `go test ./... -count=1`、`go vet ./...` 通过；golangci-lint 2.14.0 为 0 issues。
- 已实现：配置校验/默认值、TLS/map/slice 快照、注册、logger 注入/nil 静默与脱敏。155 个 Java option 已逐项记录任务/承载/状态，见 [迁移清单](go-driver-options-mapping.md)。D01 的 Connect 不可用路径已在 D04 替换；标准文本执行已在 D05 接通，参数与预处理由后续任务交付。
- V01 未完成条目：Linux 实际执行、GitHub CI 运行证据；CI workflow 已建立，尚未远程执行。回调、私钥/已解析证书与 logger 的并发安全由调用方保证。

#### D02 包流与依赖守卫

- 开发：已实现；验证：本地通过。代码：`internal/wire/packet.go`、`internal/testutil/peer.go`；测试：对应单测、`packet_fuzz_test.go`、根外部 `architecture_test.go`。
- RED：包边界、长度、短 I/O、截断、序号、取消及故障对端断言实际失败；依赖守卫的反向/横向/生产导入测试辅助负例先失败后通过。
- GREEN：Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 通过；Go 1.25.0/1.26.0 Windows/386 全包运行通过；lint 0 issues。`FuzzPacket` 两版本各运行 30 秒，分别 2,277,684 / 2,606,613 次，通过。
- V01/V04 待补：Linux/CI 实际运行。全部是合成协议/内核测试，没有数据库联调，也不能证明真实协议协商或 SQL 兼容。
- 架构守卫实际读取 `go list -deps -json -test -tags=integration ./...` 的 Standard/Imports 信息；内部包无反向/横向依赖，testutil 只用标准库。

#### D03 握手、认证与模式

- 开发：已实现；验证：本地通过。代码：`authentication.go`、`errors.go`、`internal/auth`、`internal/wire/handshake.go`；测试：`authentication_test.go`、`authentication_paths_test.go` 及内部算法/包解析测试。
- 已覆盖：native、caching SHA2、SHA256、clear；初始/切换/full-auth、固定 RSA 公钥、凭据每次刷新、认证工厂与 TLS 门禁、真实本地 TLS 握手、错误证书/主机名、自定义 Dial 伪造安全状态、模式不符和 Oracle ChangedRows 拒绝。握手不锁 ModeAuto；成功初始化后的锁定归 D04。
- RED→GREEN：算法向量、报文/TLS 门禁和配置快照用例先失败后通过；快速认证必须最终 OK、初始旧格式服务端 ERR 分别由回归用例复现后修复。独立代码走查的问题已处理；不另存走查报告。
- V01/V02/V04：Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 与 Windows/386 全包运行通过，golangci-lint 2.14.0 为 0 issues；`FuzzHandshake` 各 30 秒，分别 314,204 / 223,550 次，通过。CI 定义加入握手 fuzz，尚未远程执行。
- race 未通过运行环境检查：Windows 启动 race 测试进程返回 `0xc0000139`；指定 GCC 并前置其 DLL 搜索目录后结果相同。没有得到 race 检测结果，不计为通过；Linux/CI 的 race 仍待运行。
- 版本/能力依据见 [兼容条件矩阵](go-driver-compatibility-matrix.md)。真实服务端四目标及 OBProxy/平台插件均未验证，本阶段未连接数据库。D28 继续覆盖完整平台认证，不把上述四种算法当作全部认证能力。

#### D04 建连、初始化与 Ping

- 开发：已实现；验证：本地通过。代码：`connector.go`、`conn.go`、`session.go`；测试：`connector_test.go`、`tests/integration/env_test.go`、`smoke_test.go`。
- 已实现：Dial/握手/TLS/凭据/初始化共用建连 context 总预算，成功初始化后原子锁定模式；失败会话关闭且不锁模式，并发相反模式只允许一个发布；设置字符集/autocommit、确定顺序的会话变量和 InitSQL；Ping、COM_QUIT、有界关闭及 Validator 连接淘汰。
- RED→GREEN：预算/初始化/并发模式用例先失败后通过；非法初始化配置及 Ping 失败仍回池的用例复现后修复。独立走查发现并修正并发测试的失败清理顺序，模式并发用例连续运行 20 次通过。
- V01/V02：Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 通过；普通构建与 integration 辅助代码 lint 通过；`TestProfiles` 只验证 profile 选择，未连接数据库。既有依赖守卫同时检查 integration/test 的导入。
- 真实验收入口按四种 profile 建立，默认常规 test 不编译/执行 integration；手动 CI 显式启用才运行 TestSmoke，OB_TEST_REQUIRE=1 强制缺环境失败。未触发 CI，也未执行 TestSmoke。
- D05 已接通查询式 InitSQL 排空及精确 OceanBase 版本捕获。Prepare/Begin 暂返回明确能力错误；后续所属任务替换。命令结束必须 autocommit 且无悬挂事务，不将事务状态作为池基线。

#### D05 文本执行与标准结果

- 开发：已实现；验证：本地通过。代码：`execute.go`、`rows.go`、`result.go`、`codec.go`、`internal/wire/text.go`；测试：`execute_test.go`、`codec_test.go` 及 wire 文本测试。外部示例在 `example_test.go`。
- 已实现：COM_QUERY、NULL/空值/二进制所有权、EOF 与协商 OK 替换、混合多结果排空、完整 ERR 后复用、基础 ColumnTypes、计数溢出、精确 NUMBER/DECIMAL 和大无符号数、Oracle 特殊浮点；元数据累计/消息/LOB/结果项均有资源边界。未知类型拒绝；完整字符集/时间/扩展类型继续归 D12。
- 建连查询 `ob_version()`，Oracle 加 `FROM dual`，保留 OceanBase 版本原文；查询式 InitSQL 使用相同结果执行器排空。握手声明已实现的 multi-results/session-track/deprecate-EOF；多语句位仅显式配置时声明。
- RED→GREEN：文本执行基础断言，以及 JSON 类型、元数据累计大小、LOB 物化上限、正常 NextResultSet 不使用清理超时、Oracle NAN/INF/-INF 回归均实际观察失败后修复。独立复审无剩余可操作发现；不另存报告。
- V01/V02/V04：Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、Windows/386 全包运行通过；普通及 integration 辅助代码 lint 0 issues。`FuzzText` 两版本各 30 秒，分别 3,631,645 / 1,457,299 次，通过；CI 已增加该 fuzz。
- `TestProfiles` 双版本通过，仅验证选择；`TestQueryCRUD`、`TestQueryResults` 和增强的 `TestSmoke` 已编译且由显式手动集成 job 覆盖，未执行数据库测试。Linux/远程 CI/race 仍待验证；验收继续 0/32。

#### D06 取消、关闭与无重放

- 开发：已实现；验证：本地通过。代码：`conn.go`、`connector.go`、`execute.go`、`rows.go`；测试：`cancel_test.go`、`retry_test.go`。
- RED→GREEN：闲置 Rows 取消不能断流、等待会话锁忽略 context、Conn.Close 被未关闭 Rows 卡住，均由回归测试复现后修复。会话锁现在可取消；关闭共享清理预算，超时直接淘汰；closed 原子状态允许并发关闭及 Validator 检查。
- Query 的 AfterFunc 覆盖整个 Rows 生命周期；释放前 stop/等待回调，避免旧取消影响下一操作。部分写与丢响应保留 wire.IOError/字节证据，禁止发送后的 ErrBadConn 自动重放；坏连接不会回池。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 通过，integration 辅助代码 lint 0 issues；取消/关闭/无重放用例连续 20 次通过。独立复审无可操作发现；没有数据库联调。
- Go 1.25 race 再次在 Windows 进程启动时返回 `0xc0000139`，没有检测结果；CI 增加两个 Go 版本的 Linux 全包 race，尚未远程运行。真实语义/跨平台验证保留待验收。

#### D07 词法绑定与参数转换

- 开发：已实现；验证：本地通过。代码：`sql_lexer.go`、`bind.go`、`execute.go`；测试：`bind_test.go`、`bind_fuzz_test.go`。
- 覆盖引号/注释/Oracle q-quote、命名与编号占位符、重复逻辑参数到多个物理位置、缺失/多余/混合/重复参数拒绝、Valuer 单次转换、typed nil 和 uint64 保真。MySQL 原生服务端预处理在 D08 以服务端参数元数据为准；严格词法计划对引号模式歧义拒绝插值。
- 普通零参数 SQL 原样发送，语法和未绑定占位符由服务端报错；不按分号猜测存储过程/匿名块边界。多语句开关只控制 CLIENT_MULTI_STATEMENTS 协商，不插入查询 SQL 模式的辅助 SQL，避免改变 ROW_COUNT/FOUND_ROWS/warning 等诊断。参数执行在 D08 接通，当前仍明确拒绝。
- 原始例程/匿名块/可执行注释、ANSI_QUOTES 与无辅助查询回归均 RED→GREEN；独立复核无剩余可操作发现。Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、Windows/386 运行通过；integration 辅助代码 lint 0 issues。
- FuzzBindPlan 两版本各 30 秒通过，Go 1.25 复跑 56,859 次、Go 1.26 14,432 次；Go 1.25 首次并发运行在退出阶段超时，单工作进程复跑成功。CI 增加该 fuzz。Linux/远程 CI/race 与数据库联调未执行；验收仍 0/32。

#### D08 服务端预处理与二进制收发

- 开发：已实现；验证：本地通过。代码：`stmt.go`、`codec_binary.go`、`internal/wire/stmt.go`；修改 `execute.go`、`rows.go`、连接及能力声明。测试：`stmt_test.go`、`binary_rows_test.go`、wire stmt 单测及 `FuzzStmt`。
- 标准 ConnPrepareContext、StmtExecContext/StmtQueryContext 及旧方法委托；MySQL 原文与服务端参数数量、Oracle 命名/编号到物理参数映射；每次发送类型头，NULL bitmap、uint64、精确 decimal、固定整数 NUMBER、特殊浮点、字节/文本和基础时间。时间输入超过微秒或范围时明确拒绝，纳秒/扩展时间及其他完整类型由 D12 交付。
- 无参数走普通文本；带参数使用临时服务端 statement，终态/提前关闭自动有界释放；显式 sql.Stmt 可重复执行，标准池在各物理会话独立 prepare，不共享 ID。尚无隐式 statement 缓存，D24 再交付有界 LRU。二进制结果复用既有 Rows 取消、EOF、多结果和故障清理；仅在服务端声明时协商 CLIENT_PS_MULTI_RESULTS。
- RED→GREEN：预处理缺失、二进制 MySQL 非有限浮点错误接受、自动 statement 清理错误遗漏、Oracle 特殊浮点输入被误拒绝；均有针对回归。Prepare context 不保存到 statement，后续取消不影响下一执行；Stmt.Close 等待也有清理上限。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 与 Windows/386 运行通过，integration 辅助代码 lint 0 issues。FuzzStmt 各 30 秒，916,857 / 898,336 次，通过；CI 已增加该 fuzz。
- 独立复核未发现剩余可操作问题；`TestPrepareRoundTrip` 已仅编译并加入手动集成 job，未连接数据库。Linux/远程 CI/race 与真实两模式/OBProxy 往返待验证；验收仍 0/32。

#### D09 本地事务、保存点与 reset

- 开发：已实现；验证：本地通过。代码：`tx.go`、`savepoint.go`、`reset.go`，标准 ConnBeginTx/Tx/SessionResetter 接线；测试：`tx_test.go`、`reset_test.go` 和仅编译的 `tests/integration/tx_test.go`。
- MySQL 支持默认、ReadCommitted、RepeatableRead、Serializable；Oracle 支持默认、ReadCommitted、Serializable，ReadOnly 与显式 ReadCommitted 的冲突在发送前拒绝；两模式均拒绝 ReadUncommitted。不用 Java 的只读到可重复读转换改变 Go 请求语义。
- COMMIT 丢响应返回 ErrCommitUnknown，不重放或声称回滚；确认提交后恢复失败返回 ErrTxCleanup。取消后的 Rollback 使用独立清理 context；事务始终留在原物理连接。保存点名称按模式引用，Oracle RELEASE 在发送前拒绝。
- Managed 仅在池复用边界修复驱动管理的事务/autocommit，不承诺清除任意用户状态。Full 在未确认可靠 OceanBase 原生 reset 条件时淘汰物理连接；database/sql 在新会话重新 prepare，不迁移旧 statement ID。本任务没有声称原生 COM_RESET_CONNECTION 已取证。
- RED→GREEN：事务接口、提交结果未知/恢复失败、脏池复用、Full 后 statement 重建及 MySQL ReadUncommitted 发送前拒绝；事务/reset 针对套件连续 10 次通过。独立复核发现的隔离级别门禁已修复，不另存报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、Windows/386 运行通过；普通和 integration 辅助代码 lint 0 issues。集成源码仅编译，未连接数据库；Linux/远程 CI/race 仍待验证，验收维持 0/32。

#### D10 客户端预处理与显式 escape

- 开发：已实现；验证：本地通过。代码：`client_prepare.go`、`escape.go`、`internal/charset`，Config.PrepareMode/DSN 及标准 Prepare/Exec/Query 接线；测试：`client_prepare_test.go`、`escape_test.go`、两个 fuzz 和字符集单测。
- 默认 PrepareServer；仅显式 PrepareClient 使用客户端插值，不自动降级。执行时读取当前 NO_BACKSLASH_ESCAPES 状态，歧义的 MySQL 双引号跨度拒绝；不插入 sql_mode 查询。每个字面量保留 token 空白边界，防止负数变成注释或数字与别名合并。
- MySQL 字符串使用明确字符集的 hex literal，二进制独立编码；Oracle 使用引用/HEXTORAW/CHR(0)。有限浮点保留 DOUBLE 身份：MySQL 指数形式、Oracle d 后缀；不让 Oracle NUMBER 范围截断 double。消息、累计编码和 LOB 参数有界，Valuer 复用 D07 的单次转换。
- 字符集叶包提前自 D12 接入当前连接已支持的 UTF8MB4/UTF8MB3/Latin1（Windows-1252），唯一依赖 x/text v0.31.0；其他字符集及完整类型映射继续 D12。整条客户端 SQL 与参数均编码，非法/不可表示文本拒绝。
- RewriteJDBCEscapes 显式处理日期/时间戳、函数、嵌套、call/返回调用、outer join/escape；不改写引号/注释，Auto 与会话引号歧义拒绝。Oracle 无 TIME 类型及未确定的 timestamp arithmetic 明确拒绝；普通 SQL 没有隐式 JDBC 改写。MySQL REAL/FLOAT/DOUBLE 转换保留浮点语义。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、Windows/386 运行通过，普通及 integration 辅助代码 lint 0 issues。两个 fuzz 在两版本各 30 秒通过；客户端插值 549,084 / 344,984 次，escape 371,466 / 7,673 次。CI 定义已加入；本地后续回归与全量验证覆盖走查修复。
- 独立复审发现的负数边界、浮点类型和 REAL 转换已回归修复。TestPrepareEquivalence 源码仅编译，未执行数据库联调；Linux/远程 CI/race 待验证，验收仍为 0/32。

## 6. 待取证条件与推进方式

| ID | 条件 | 影响范围 | 解除/取证方式 | 当前判断 |
| --- | --- | --- | --- | --- |
| E01 | 两模式 OceanBase 与 OBProxy 实际测试入口/版本 | D04 起的集成验收 | 配置测试 profile，运行 TestSmoke 并记录版本/模式 | 未确认；不阻塞配置与包流 |
| E02 | Go 1.25、1.26 工具链及平台运行能力 | 双版本兼容声明、D32 | CI 安装目标版本并保存 go version 与测试结果 | Windows 两目标版本已运行；Linux/CI 未执行 |
| E03 | reset/PrepareExecute/offset fetch/LOB V2/array binding 支持组合 | D09、D16、D19、D22、D24 | 固定 Java 条件 + 实际目标版本用例 | 待取证；未知时保守选择或拒绝 |
| E04 | Oracle 游标与 LOB/类型字典同连接交错边界 | D16、D18–D20 | 在完整 fetch 响应边界验证原事务可见性 | 待取证；禁止换辅助会话冒充 |
| E05 | OUT/REF CURSOR 响应顺序和父 statement 依赖 | D14、D20 | 协议夹具 + 真实过程/多输出生命周期测试 | 待取证；保持发布与租约契约 |
| E06 | 各平台认证、IPC 及拓扑发现环境 | D27、D28 | 每个适配器独立运行/失败用例 | 未确认；可先做平台编译与协议单测 |
| E07 | Java 全部 option 的逐项对应 | D01 起持续、D32 收口 | 固定版本提取并归类，每项关联 Go 承载与任务 | 155 项清单已建立；后续功能逐项实现和验证 |

这些是开发输入和验证条件，不是缩减功能的决定。出现实际阻塞时，再把受影响任务标记“环境/契约”等阻塞，并写明可继续工作。

## 7. 下一步工作

1. 推进 D11：标准 Rows 上的混合结果摘要、线程安全快照与有界最终化。
2. 继续用故障对端验证标准 database/sql 查询，不连接真实数据库。
3. 保留完整 G2–G6 范围；数据库联调暂缓，Linux/CI 和所有未执行条目如实保留。

本表只管理开发事实和后续动作，不保存独立走查报告，也不预估尚无依据的完成日期。
