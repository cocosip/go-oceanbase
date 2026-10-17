# OceanBase Go 驱动开发进度

最后更新：2026-10-17。任务定义见 [开发计划](go-driver-development-plan.md)，行为契约见 [架构设计](go-driver-architecture-design.md)。

## 1. 当前状态

- 当前阶段：配置、标准包流、握手/认证、建连/Ping 和标准文本执行、完整 Rows 取消/连接关闭协调、词法绑定、服务端预处理/二进制执行及事务/保存点/reset、客户端预处理/escape 和 Execute 摘要、D12 标量/字符集/时间、D13 元数据、D14 标量 OUT/INOUT/RETURNING、D15 固定资源生命周期、D16 原生 LOB、D17 表内 BLOB、D18 OBJECT/ARRAY、D19 游标/滚动/更新、D20 连接相关输出、D21 一次性长参数、D22 批量策略、D23 包流/校验/新元数据、D24 合并执行/缓存优化、D25 诊断/FLT 、D26 静态选址/建连切换、D27 TNS/OCP/拓扑发现及 D29 LOCAL INFILE 已有本地实现；继续 D28/D30，D30 会话辅助、warning 数量快照及 ChangeUser 已有本地实现，具体支持边界见架构 §9.1、§11.2.2、§13.2、§13.4 与 §15。
- 实现完成：**28 / 32 项**；验收完成：**0 / 32 项**。分母为开发计划 D01–D32，不是代码行数、工时或功能数量估算。
- 下一执行任务：**D28 平台传输取证与 D30 会话能力补齐**。
- 仓库已有：设计文档、Go module、注册/配置/DSN、外部日志、标准包流/故障对端、握手/认证/TLS/实际模式、文本执行/Rows/计数、词法绑定及参数转换、服务端预处理/二进制收发、事务/保存点/reset、包依赖守卫及 CI 定义。
- 仓库已有原生 Blob/Clob 句柄、同会话有界物化和表内 BLOB 目标/分块；OBJECT/ARRAY、单命令类型描述复用和嵌套 LOB 物化已有本地实现；显式游标、滚动及 PK 更新、REF CURSOR/LOB 输出已实现；长参数同会话分段上传、批量逐项及受限 multi-send 已实现；改写/bulk/array binding 当前按证据回退逐项，原生优化未启用；D23 标准/压缩/OB20、双 checksum 与新元数据已本地实现；D24 显式执行策略已本地实现；D25 诊断和客户端 FLT 已本地实现；D26 静态多地址/角色/健康/建连切换已本地实现；D27 有界发现源/刷新与关闭已本地实现；D29 受控导入已本地实现；D30 schema/catalog、客户端信息/变量、warning 数量快照和 ChangeUser 已有本地实现；D28 Shared Memory/VC、D30 warning 详情/显式取消、D31–D32 仍需实现。
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

“—”表示尚无负责人或执行证据；依赖对应开发计划，具体状态以各行事实为准。依赖存在不等于任务已发生阻塞。

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
| D11 | Execute、多结果、计数策略 | D08、D09 | 已实现 | 本地通过 | 无（待联调） | §5.6 D11 |
| D12 | 全类型、字符集、时间 | D08、D10 | 已实现 | 本地通过 | 无（待联调） | §5.6 D12 |
| D13 | 结构/结果/参数元数据 | D09、D12 | 已实现 | 本地通过 | 无（待联调） | §5.6 D13 |
| D14 | 标量 OUT/INOUT/RETURNING | D11、D12 | 已实现 | 本地通过 | 无（待联调） | §5.6 D14 |
| D15 | 固定会话资源、代次与租约 | D09、D11 | 已实现 | 本地通过 | 无 | §5.6 D15 |
| D16 | 原生 BLOB/CLOB/NCLOB | D12、D13、D15 | 已实现 | 本地通过 | 无（待联调） | 本地协议、双版本/386/lint/fuzz；真实原事务/NCLOB/锁行为待联调 |
| D17 | MySQL 表内 BLOB 模拟 locator | D13、D16 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D17 |
| D18 | OBJECT/ARRAY、类型描述 | D12、D13、D16 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D18，支持边界见架构 §11.2.2 |
| D19 | 游标、滚动、刷新和更新 | D13、D15、D16 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D19，支持边界见架构 §15 |
| D20 | REF CURSOR/流式 LOB 输出 | D14、D16、D19 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D20 |
| D21 | 长参数、分段上传 | D08、D12、D15 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D21 |
| D22 | 批量、bulk、array binding | D11、D14、D18、D21 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D22，三类原生优化回退边界见架构 §13.4 |
| D23 | 压缩、OB20、新元数据、校验 | D06、D08 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D23，边界见架构 §9.1 |
| D24 | PrepareExecute、缓存、优化 | D08、D11、D14、D21、D23 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D24，架构 §10.1.1 |
| D25 | 诊断、profiling、FLT | D11、D23 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D25，架构 §18.3.1 |
| D26 | 静态多地址、负载、故障切换 | D06、D09 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D26、架构 §17.1.1 |
| D27 | TNS/OCP/拓扑发现 | D26 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D27、架构 §17.1.2 |
| D28 | 完整认证、平台传输 | D03、D26 | 进行中 | 未验证 | 无 | old/Ed25519/PAM、SSPI/Unix GSSAPI、Unix/Named Pipe 已补；其余继续 |
| D29 | LOCAL INFILE 受控导入 | D06、D21 | 已实现 | 本地通过 | 无（待联调） | Codex；2026-10-17 开始/完成；见 §5.6 D29、架构 §17.3.1 |
| D30 | 会话辅助、warning、切用户、取消 | D09、D11、D15、D26、D28 | 进行中 | 未验证 | 无 | Codex；首批辅助函数/数量快照已补；ChangeUser 已补；详情、CancelQuery 继续；见 §5.6 D30 |
| D31 | XA 分支、状态与恢复 | D09、D14、D18、D30 | 未开始 | 未验证 | 无 | — |
| D32 | 配置逐项迁移、完整验收与交付 | D01–D31 | 未开始 | 未验证 | 无 | — |

## 4. 里程碑

| ID | 目标 | 实现项 | 验收项 | 状态 |
| --- | --- | --- | --- | --- |
| G1 | 建连与标准查询，D01–D05 | 5/5 | 0/5 | 进行中 |
| G2 | 执行与会话正确性，D06–D12 | 7/7 | 0/7 | 待验收 |
| G3 | 完整扩展数据能力，D13–D22 | 10/10 | 0/10 | 待验收 |
| G4 | 协议与运行能力，D23–D30 | 6/8 | 0/8 | 进行中，D23–D27、D29 本地通过，待联调 |
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
| D17 | V01、V02、V03、V04、V07、V08 | 原生/模拟后端分别验收，表锁/主键/无变化 UPDATE |
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

#### D11 Execute、多结果摘要与计数策略

- 开发：已实现；验证：本地通过。代码：`execution.go`、`execute.go`、`rows.go` 与参数/prepare 接线；测试：`execution_test.go`、`internal/wire/packet_test.go`。Execute 仅接受非 nil 的标准 Conn/Tx；发送前失败不返回执行对象，发送后保留错误前缀和部分结果。
- 摘要按服务端顺序保存命令、行集与错误；行集从 0 编号，不插入假行集；零计数与未知不同，uint64 生成 ID 保留十进制文本。结果数有界，计数溢出明确失败，Snapshot 复制切片且不访问网络/标准 Rows。
- Close 有界排空、幂等并合并标准库 Rows.Err；调用方应先停止迭代，以 Close 为最终摘要边界。原始事务上下文关联保留 DeadlineExceeded/自定义 cause；显式 Commit/Rollback 内部取消由 Close 协调。首个顶层及逐项错误均保留，包含已传输字节证据。
- 包流每次入口刷新双向 deadline，离场只停止/等待旧取消回调；已收完整响应不被清 deadline 失败覆盖。TLS 升级清除原包流 deadline 后由同一上下文的 HandshakeContext 负责取消；后续包流重新设置 deadline。
- 两工具链 Go 1.25.0/1.26.0 的 Windows amd64 全包 test/vet、Windows/386 运行、普通和 integration lint 均通过；相关执行/事务/TLS 目标测试重复 10 次通过。FuzzPacket 两版本各 30 秒通过，分别 2,236,794 / 2,526,965 次。
- `TestMultiResult` 两模式源码及 MySQL 无变化 UPDATE/upsert 计数对照已编译，CI 手动入口已加入；未执行数据库联调。Linux/远程 CI/race 尚未验证；验收仍为 0/32。

#### D12 标量、字符集与时间语义

- 开发：已实现；验证：本地通过。新增 `types.go`、`number.go`、`temporal.go`、`codec_temporal.go`，扩充绑定、两类预处理、会话初始化、Rows 及 charset 叶包。公开值类型实现 Scanner/Valuer，识别具体类型与 sql.Null[T] 后保留协议身份；普通 Scan 不返回自定义对象。
- 精确 Number 有界展开且不经 float64；NString、Raw、RowID 和原生 Oracle TZ/LTZ/TIMESTAMP/间隔覆盖文本/二进制/客户端三路径。连接 SQL、准备 SQL、参数、结果和列名按字符集转换；UTF16 的 NCHAR/结果支持与客户端限制分开，未知编码/collation 拒绝。
- Session.ParseTime 默认 false；显式启用后按 Location 解析无时区日期/时间戳，TIME/YEAR 不改语义。建连设置真实会话时区，用户初始化后重新应用类型基线。纳秒、偏移和 region 保留；LTZ 墙上时间没有 fold 时不虚构瞬间，DST 缺口/重叠转换报错。
- RED→GREEN 覆盖类型/精度、SQL 与列名编码、parseTime 及 ScanType、sql.Null 身份、初始化覆盖时区、二进制字节参数上限；独立走查发现的公元 1 年误判已回归修复。FuzzCharset 发现 Windows-1252 未定义字节静默替换，现明确拒绝并保留最小 corpus。没有另存走查报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 运行及普通/integration 两套 golangci-lint 均通过。FuzzScalars 各 30 秒，409,076 / 339,348 次；FuzzCharset 修复后各 30 秒，7,067 / 175,854 次，通过，CI 定义已加入。
- `TestAllScalarPaths` 使用真实 database/sql + 协议对端验证八类标量的参数和结果/NULL；非法编码验证物理连接淘汰。`TestAllScalarTypes` 四目标三路径集成源码仅编译，未连接数据库；Linux、远程 CI/race、服务端时区表/SQL及字符集实际兼容仍待验证，验收仍为 0/32。

#### D13 数据库、结果与参数元数据

- 开发：已实现；验证：本地通过。新增 `metadata.go`、两模式 SQL 文件、`metadata_test.go`、`metadata_fuzz_test.go` 和仅编译的集成源码；补齐普通 Rows 的五个 ColumnType 接口及 prepare 参数描述保存。公开 Metadata 仅接受固定 Conn/Tx，返回普通 Rows；17 种 Kind 的字段/NULL/类型/模式边界见架构 §16.1.1。
- 筛选值参数化，内部复用现有安全字面量编码和同一物理会话；保留大小写与 LIKE 转义。不把任意名字拼入标识符位置。公开 Rows 接管连接 gate，内部 Rows 保留外层所有权；未读响应拒绝内部命令，错误/关闭有界排空。描述缓存待 D18，当前没有任意字典行集缓存。
- StatementParameters 无论 PrepareMode 都只做服务端 prepare/close，按物理位置描述并保留重复逻辑参数；方向未取证时 NULL，同时保存原始协议字节。Oracle catalog 为空行集，MySQL 不提供的类型/同义词字典返回 ErrUnsupported；PARAMETERS 仅按真实 ob_version >=4.1 开放，未知/旧版本拒绝。字典权限错误保留服务端代码。
- RED→GREEN 覆盖统一入口、过滤/枚举/数量边界、未知长度/精度、内部响应及锁边界、参数身份和协议描述。独立走查发现的 FLOAT 二进制精度、两个字典 NUMBER_PRECISION 字段、索引 A/D 到 ASC/DESC 规范化均已用回归验证修复；未发现其他 Important/Critical，不另存报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 运行通过；普通和 integration 两套 golangci-lint 均 0 issues。Metadata/ColumnTypes 目标用例重复 10 次通过；FuzzMetadataFilters 两版本各 30 秒，117,697 / 117,036 次，通过，CI 定义已加入。
- `TestMetadataCatalog` 在四目标创建表/外键/视图，比较 DDL、独立字典列顺序和模式字典可用性；当前仅编译，未连接真实数据库。Linux/远程 CI/race、字典字段的目标版本兼容及实际权限仍待验证，验收保持 0/32。

#### D14 标量 OUT、INOUT 与 RETURNING

- 开发：已实现；验证：本地通过。新增 `call.go`、`call_execution.go`、`call_mysql.go`、`prepare_execute.go` 及 OUT/协议/集成测试；标准 Query/Exec 和两种预处理共用输出校验、赋值与最终化。Out 类型与尺寸、ErrOut/OutError 的具体行为见架构 §13.1。
- Oracle 使用真实 ob_version >=2.2.76 的 command161，接收参数/列元数据、内部输出与 mandatory tail；接受 RETURNING 1/3 和 PL OUT，支持弃用 EOF。MySQL 使用同会话随机变量初始化/执行/读取/清空；辅助命令保留 gate，且不继承父 Execution 摘要。临时 statement 或变量清理失败返回错误并淘汰连接。
- RED→GREEN 和回归覆盖发送前拒绝、INOUT 初值及复用、Number/nullable/unsigned 身份、NULL/溢出/尺寸、重复逻辑绑定一致性、零行/多行输出、尾包错误、主/辅助命令错误、关闭失败及最终化期间取消；所有内置转换通过后才写回，自定义 Scanner 失败报告 Partial。原有未知列类型立即拒绝和绑定/标量用例同步通过。独立走查无剩余 Important/Critical，不另存报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 运行通过；普通/integration 两套 golangci-lint 均 0 issues。`TestOut*` 重复 10 次通过；FuzzPrepareExecute 两版本各 30 秒，563,547 / 612,847 次，通过，CI 定义已加入。
- `TestScalarCall`、`TestReturning` 在真实四目标验证过程、函数、匿名块、多输出、异常抑制和 RETURNING；当前仅编译并加入显式手动集成入口，未连接数据库。Linux/远程 CI/race、目标例程/RETURNING 与 OBProxy 行为仍未验证；实现 14/32，验收保持 0/32。

#### D15 固定会话资源与生命周期

- 开发：已实现；验证：本地通过。新增 `resource.go`、`resource_test.go`；复用 Session 具体类型校验，接通标准 Exec 私有关闭请求、connection 内记录、物理/会话/事务身份和 statement 租约。句柄只持标准 Conn/Tx 引用及不透明标识，不持 driverConn；无公开资源管理器或全局注册表。
- 事务开始/结束、池 reset 与服务器可观察事务状态切换使旧代次失效。完整外层响应清理未发布/过期记录，内部辅助响应保留外层资源；statement 调用方 Close 立即禁止继续执行，服务器关闭等最后一个资源租约释放。资源 Close 不提交/回滚/关闭调用方 Session。
- RED→GREEN 覆盖无效 Session、错误物理连接、Conn/Tx/新事务/reset、并发幂等关闭、未发布记录、手动事务边界及有界清理。评审发现的事务清理无超时与标准库分派前取消已分别由 `TestResourceTransactionCleanupBounded`、`TestResourceCancellationAtDispatch` 复现并修复；保留 statement 的 OUT 执行与预取消 Close 回归也通过。不保存独立走查记录。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 全包运行通过；普通/integration 两套 golangci-lint 均 0 issues；`TestResource*` 各重复 20 次通过，既有集成源码仅编译。文档路径/链接/围栏、155 项 option 表与 15 项实现计数检查通过。
- 具体 locator/游标协议及公开句柄仍由 D16/D19/D20 实现。未连接真实数据库；Linux/远程 CI/race 仍未验证，当前 Windows race 运行条件未恢复。实现 15/32，验收保持 0/32。

#### D16 原生 BLOB/CLOB/NCLOB

- 已实现 OpenBlob/OpenClob，Blob 字节随机读写/长度/缩短、显式 context Reader/Writer，Clob 码点读写/长度/缩短。只保存标准 Session 和不透明资源身份；原生解析留在 wire，调度/物化在根包，无新包依赖环。
- Locator V1/V2、内联/外联、临时/持久、NULL/空值分别处理；原生 DBMS_LOB 复用 D14 合并执行内核，实际 ob_version >=2.2.76。读取数量无损接受整数/NUMBER；分块和清理有界；写失败返回已完成前缀，不重放。协商 CONNECT_ATTRS 后只声明已实现的 V2 属性位。
- 普通 Query、多个行集及标量 OUT 有界物化内容，完成整个响应/尾部后才插入内部命令；不重入公开 sql/Metadata 或 gate，不开辅助连接。Open 基数错误清理所有已取得临时对象，成功句柄在查询/清理/最终取消检查完成后发布。
- 字符索引采用 Unicode 码点；已知 UTF16 collation 转为服务端单位；改变已有代理对宽度的覆盖在写前 ErrUnsupported，追加/等宽覆盖及截断支持。真实 NCLOB 单位/编码和同事务可见性仍未验收。
- RED→GREEN 覆盖 out-row 短读、QueryContext 原会话物化、临时多对象清理、标量 LOB OUT，以及走查发现的缓冲事务 context、NUMBER 数量、UTF16 覆盖和 OUT 后首个普通 LOB 行集。补充超过16MiB Reader、混合结果、部分写错误、CLOB/NCLOB 等宽覆盖/截断和发送前取消。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 全包运行通过；LOB/Clob 各重复10次通过；普通/integration golangci-lint 均 0 issues。Locator fuzz 各30秒通过（1.25: 2248880，1.26: 2524672 次）。TestNativeLOB 已加入手动集成入口，仅编译，未连接数据库。
- 实现 16/32，验收仍为 0/32。Linux/远程 CI/race 未验证；当前 Windows race 运行条件未恢复。不保存独立走查报告。

#### D17 表内 BLOB 与 MySQL 分块后端

- 开发：已实现；验证：本地通过。负责人 Codex；开始/完成日期 2026-10-17。新增 `lob_target.go`、`lob_mysql.go`、对应普通/故障/fuzz 测试及仅编译的 `tests/integration/table_blob_test.go`；复用 Blob、标准 Session 私有分派与现有资源代次，无新增公开包或横向依赖。
- 目标由当前模式字典确认基表、二进制非生成 BLOB 列和完整主键。Schema 在原会话解析后固定，标识符逐部分引用，Key 与字节值复制，自定义 Valuer 只求值一次。打开只读取元数据、存在性/NULL/长度或原生 locator，不整体读取内容；无行/NULL/非 NULL 空值分别处理。
- MySQL OCTET_LENGTH/SUBSTRING 逐次重新按键访问；每块最多 32767 字节并缩小到消息/单块预算，句柄内容可超过物化上限。WriteAt/Truncate 仅允许调用方可写 Tx 和已知 InnoDB 锁语义，每次先 FOR UPDATE 确认身份及长度；不填稀疏空洞，ChangedRows=0 不误报缺行。四种 BLOB 容量在首次修改前预检；UPDATE 有 warning 时 ErrLOB，失败块可能已产生影响，回滚由调用方负责。
- Oracle 相同目标解析原生 Blob，可写 Tx 打开时 FOR UPDATE；只读 Tx/Conn 不加该锁。公开名字使用字典精确拼写，MySQL 列名忽略大小写解析。内部命令始终保留原 gate/物理会话并消费完整响应；关闭模拟句柄只释放本地记录，事务结束/reset 使句柄失效。
- `TestBlobTarget*`、`TestTableBlob*` 覆盖复合主键/特殊标识符、Valuer 一次及修改隔离、缺行/NULL/空、大于物化上限的跨块读取、最大偏移/追加/相同值/空写/缩短、只读和未知引擎、锁错误、断线/取消不重放及完成前缀。Oracle CHAR/DATE 主键、默认表达式与生成列区分、原生事务行锁、容量和 warning 问题均由 RED→GREEN 回归验证；独立走查未留下可操作问题，不另存报告。
- 最终在 Go 1.25.0/1.26.0 Windows amd64 运行 `go test ./... -count=1`、`go vet ./...` 和目标用例重复 10 次通过；两版本实际 Windows/386 全包测试通过。普通与 `--build-tags=integration` 的 golangci-lint 均 0 issues；集成源码使用 `-run '^$'` 仅编译。`FuzzBlobIdentifiers` 两版本各 30 秒通过，分别 554034 / 642929 次；CI 定义已加入。
- 实现 17/32，验收仍为 0/32。`TestTableBlob` 四目标源码覆盖大对象、ChangedRows、并发锁/回滚、截断和资源结束；未连接数据库，实际 SQL/字典/锁/Oracle locator/非严格模式仍待联调。Linux、远程 CI 和成功 race 尚无证据。

#### D18 OBJECT/ARRAY 与同会话类型描述

- 开发：已实现；验证：本地通过，真实目标待联调。负责人 Codex；开始/完成日期 2026-10-17。新增 `complex.go`、`complex_codec.go`、`type_descriptor.go`、`internal/wire/complex.go`、复杂值普通/故障/fuzz 测试及仅编译的 `tests/integration/complex_test.go`；根包编排复用现有 wire/charset/metadata/LOB，不增公开包、依赖或循环引用。
- Object/Array/Attribute 保留精确类型与字段顺序，Scanner/Valuer 使用 format=1 JSON；NUMBER 十进制、RAW/BLOB base64、墙钟时间、嵌套/NULL/空集合无损。NamedValueChecker 保留原生输入身份，参数发送真实 COMPLEX160，不把 JSON 字符串当作对象协议。
- 字典缓存仅限单业务命令/单物理会话；模式/schema/name/协议版本为键，命令/reset 清空；0 表示服务端当前版本。复用 D13 类型/属性/集合/同义词计划，支持 private/PUBLIC synonym，拒绝远程链接和循环。未读响应禁止内部命令；先有界缓冲全部剩余响应再查字典/读嵌套 LOB，不重入公开 Metadata/Session/database/sql，不开辅助连接，不重放业务 SQL。
- 当前支持及拒绝边界见架构 §11.2.2、兼容矩阵 §9。要求服务端预处理二进制结果；非 NULL TIMESTAMP/LOB 字段输入和未确认的内置集合元素头明确拒绝。JSON/树/描述响应有界，根复杂值的嵌套 LOB 共享剩余额度；最终表示与整响应另受 MaxMessageBytes 限制。
- `TestComplexLossless`、`TestDescriptorNoReentry`、`TestDescriptorRefresh` 覆盖标准 database/sql 缓存未命中、完整响应、类型版本/字段变化及精确值；补齐具名/匿名对象集合、CLOB 字符集、PUBLIC 同义词、描述累计预算、复杂 OUT 尾错等路径。四处问题均先复现再修复：OUT 列数越界、树构建期共享预算、Close/Exec 后续复杂结果集排空、非法数组元素头；同时补齐最终 OUT 清理超时及缓冲描述刷新回归。独立只读复核未发现遗留 Important/Critical，不保存独立走查报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 `go test ./... -count=1` / `go vet ./...`、复杂/描述用例重复 10 次、实际 Windows/386 全包运行均通过；普通及 `--build-tags=integration` 的 golangci-lint 两版本均 0 issues。`FuzzComplex` 两版本各 30 秒通过，分别 203745 / 266653 次，CI 已加入。integration 使用 `-run '^$'` 仅编译，没有数据库连接。
- 实现 18/32、G3 6/10，验收仍为 0/32。`TestComplexTypes` 已提供四目标对象/集合、精度/RAW/DATE、NULL、嵌套 LOB、原事务及复杂 OUT 源码；实际类型/权限/版本/OBProxy 行为仍未联调。Linux、远程 CI 和成功 race 仍无运行证据。

#### D19 原生游标、滚动、刷新与可更新结果

- 开发：已实现；验证：本地通过，真实目标待联调。负责人 Codex；开始/完成日期 2026-10-17。新增根包 cursor.go/cursor_protocol.go/cursor_update.go、协议/边界/更新测试及仅编译的 integration 游标源码；未增加包、依赖或循环引用。server prepare 提取同 gate 内部入口供 Refresh/DML 复用，公开 Prepare 路径继续持有原 gate 并保持有界清理。
- Forward 使用原生 cursor execute/fetch；Static 使用受 MaxCacheBytes 约束的真实 fetch 快照；Sensitive 通过 Oracle >=2.2.76 的命令161/scrollable mode8/绝对 offset fetch，每次定位向服务器请求，不用缓存冒充。旧/未知 Oracle offset 版本使用普通9字节向前 fetch；EOF 两路径均消费正确响应，row EOF 状态在尾 OK 前保留。
- 标准 Conn/Tx + sql.Rows 批次，独立 Fetch context、批次未关闭即 ErrCursorBusy；所有值有界缓冲，复杂描述/LOB 在完整响应后原 gate 内处理。Close/事务/reset 释放既有 statement 租约，不开辅助会话、不重放业务 SQL。
- 修改/Refresh 要求可写 Tx、单基表投影和完整 PK；MySQL 要求 InnoDB。修改列显式、安全引用，DML/Refresh 用原生参数；Update/Delete 先同事务 FOR UPDATE，ChangedRows=0 不误报缺行。Insert 拒绝 NULL/缺失 PK；生成列拒绝写入；Refresh 读取基表投影，static 保留原快照。具体位置/主键/模式/版本边界见架构 §15。
- `TestCursorFetch`、`TestCursorStatic`、`TestCursorSensitive`、`TestCursorUpdateIdentity`、`TestCursorInsertRefresh`、Oracle LOB/修改、两种 EOF、旧版本、事务/reset/context 等本地通过。回归先复现再修复了空游标重复 fetch、失败 fetch 后复用、NULL 字节主键、原生参数契约差异、prepare/存在性锁后事务代次变化继续修改、Refresh 缺行误淘汰调用方事务，以及损坏错误包复用连接；相应场景通过。独立只读走查未发现遗留 Important/Critical；没有另存走查报告。
- 最终 Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、游标重复10次、实际 Windows/386 全包测试通过；两版本普通及 integration 标签 golangci-lint 各0 issues。`FuzzCursorPosition` 两版本各30秒通过，分别295037 / 588323次；CI 已加入。integration 均以 `-run '^$'` 仅编译。
- 当前实现19/32，G3 7/10，验收仍0/32。`TestCursorModes`、`TestCursorUpdates` 已有直连/代理两模式的原生 fetch、LOB、滚动/敏感变化、PK 增删改刷新、并发缺行及事务失效源码；真实服务器/锁/版本/权限/敏感性与隔离尚未联调。Linux、远程 CI 和成功 race 仍无运行证据。

#### D20 REF CURSOR 和流式 LOB 输出

- 开发：已实现；验证：本地通过，真实目标待联调。负责人 Codex；开始/完成日期 2026-10-17。新增根包 cursor_out.go、输出/子 Rows 回归和 integration 源码；复用标准 database/sql、资源代次及父 statement 租约，无新增包或依赖。
- CursorOut、显式 BLOB/CLOB/NCLOB Out 只由 Execute 接受；Oracle 已知 >=2.2.76 原生隐藏 OUT/完整尾响应。目的初始 nil、NULL 保持 nil；拒绝 INOUT 和句柄长度/精度提示。最后资源关闭才关闭父 statement；已发布句柄跨 Execution.Close，Fetch/LOB 读取使用新 context。
- 普通子 REF CURSOR 在完整父响应后有界读取；返回标准子 driver.Rows 并随父 Rows 关闭。总预算包含嵌套元数据/行体，最大 16 层及循环拒绝；static 的跨批缓存计入子快照，回看创建独立子读者。事务内部 operation context 与调用方 iteration context 分离。
- `TestCursorOutPublish`、`TestOutputLease`、LOB 混合/超物化上限、NULL、尾错/损坏响应、普通接口/模式/版本发送前拒绝、Scanner 失败及转换中取消、事务租约、嵌套/循环/深度及 fetch 取消本地通过。独立走查发现的事务子 context、static 子缓存预算/回看和父子元数据总预算均先复现再修复，不保存独立报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、输出与子游标回归重复10次、实际 Windows/386 全包测试通过；两版本普通及 integration 标签 golangci-lint 各0 issues。FuzzStmt 两版本各30秒通过，分别1259914 / 423113次。integration 均仅编译，未连接数据库；`TestProcedureResources` 四目标源码仅编译，真实过程、临时 locator、REF CURSOR/OBProxy 行为待联调。Linux、远程 CI 和成功 race 仍无运行证据；实现20/32、G3 8/10，验收0/32。

#### D21 一次性长参数与分段上传

- 开发：已实现；验证：本地通过，真实目标待联调。负责人 Codex；开始/完成日期 2026-10-17。新增根包 long_data.go/long_data_test.go 和 integration 长参数源码；参数值体省略复用 wire 参数结构，保留一个公开根包，无新增依赖或循环引用。
- *LongData 明确借用 Reader，精确源字节 Length/-1、MaxBytes 源/转换双上限和 Text UTF-8 增量编码；上传开始原子消费，同一命名参数多位置逐块分发，不能 seek/rewind 或自动重试。分块先发送再继续读取，不整流物化；任意阻塞 Reader 需要调用方取消协作。具体公开契约见架构 §13.5。
- MySQL LONG_DATA24 无逐段响应；Oracle >=2.2.76 PIECE_DATA162 逐段确认，二进制 hex/文本会话字符集及 STRING254；execute 只保留参数位图/类型头。普通预处理、显式游标、Oracle 原生 OUT 和 MySQL OUT 的服务端流 CALL 均接入原会话/事务和 gate。
- TestLongDataChunks/Failure/Text/OracleOutputs/MySQLOutputs/Cursor、命名 fanout、增量发送、非法 Reader/空指针/空读、长度/预算、段错误/损坏、部分写、Tx/操作取消及 consumed 拒绝均通过。Reader 的 n+err/ErrUnexpectedEOF 不吞错；ErrBadConn 特殊隔离防止 database/sql 自动重放。
- 独立只读走查发现上传完成后取消时共享 Rows 清理可能停止待运行回调而保留长数据；TestLongDataCancelAfterPiece/CancelBeforeExecute 均先失败再通过。Rows 释放在观察到取消时直接淘汰连接，覆盖上传释放窗口和上传后 execute 前窗口；没有保存独立报告。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、长参数重复10次、实际 Windows/386 全包测试通过；两版本普通/integration 标签 golangci-lint2.14.0 各0 issues。FuzzLongTextBoundary 两版本各30秒通过，分别184336 / 185667次；CI 已加入。integration 两版本均仅编译，未连接数据库。
- TestLongParameters 四目标源码包含超过普通物化上限的二进制上传、文本 UTF-8、哈希比对和提交/回滚；真实 Oracle/MySQL/OBProxy 的包限制、空流/LOB 转换、锁及事务仍待联调。Linux、远程 CI 和成功 race 仍无运行证据；实现21/32、G3 9/10，验收保持0/32。

#### D22 批量执行与保守策略选择

- 开发：已实现；验证：本地通过，真实目标待联调。负责人 Codex；开始/完成日期 2026-10-17。新增根包 batch.go/batch_protocol.go、internal/wire/pipeline.go 及测试、integration 批量源码；复用普通参数校验、绑定、Rows 最终化与标准库分派，无新增包、依赖或循环引用。
- ExecBatch 保持输入顺序、已知/未知计数、Started 与四种状态；默认停止，显式继续仍在不确定/取消/损坏时停止。DB 全批次固定连接，Conn/Tx 一次标准库锁定；不改变事务边界，不重放业务。确认成功后清理失败保留成功和计数，并返回错误。
- Auto/MultiSend 只在 server prepare、显式继续、简单 DML 和显式标量条件下提前发送独立命令；并发写/按序读、独立序号、行/物理参数位/总字节限制，失败关闭连接并等待写协程。Oracle 重复具名参数超出单块物理位限制回退逐项。OUT/流/复杂值/自定义 Valuer 回退普通逐项；句柄输出仍只接受 Execute。
- 改写、bulk、array binding 偏好已接入，但三类原生优化未启用。Java 有 OceanBase bulk 不支持说明；array 的特殊错误计数及事务门禁未形成完整等价证据；改写可改变部分成功/触发器/错误边界，均按架构 §13.4 回退逐项，不猜测服务器版本支持。
- TestBatchResults/Atomicity/Strategies/TxBoundary/LongAndOutputs、断线单次拨号、取消、损坏与额外响应、typed-nil、分块及 Pipeline 提前发包/背压/续包用例通过。回归先失败再修复了确认响应后清理状态、既有 SQL 错误下最终取消丢失、迟到写错覆盖 SQL 错误、共享 prepare 失败停止继续批次及非法参数归因顺序、具名多位置分块不等价；没有独立走查报告。
- 独立只读复核未发现遗留 Critical/Important。最终 Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、批量及流水线重复10次、实际 Windows/386 全包测试均通过；两版本普通/integration 标签 golangci-lint2.14.0 各0 issues。FuzzStmt 两版本各30秒通过，分别997617 / 1885852次；六份文档结构/链接/本地路径、32任务/22实现/0验收/155选项检查通过。`TestBatchExecution` 四目标源码覆盖全部策略偏好、重复键部分成功与原事务回滚，仅编译，不连接数据库；真实 SQL、版本、代理缓冲和事务仍待联调。Linux、远程 CI 和成功 race 仍无运行证据。当前实现22/32、G3 10/10，验收保持0/32。

### D23 压缩、OB20、新元数据与校验

- 开发：已实现；验证：本地通过，验收未完成。根包 `ProtocolOptions` 五项三态；标准/压缩/OB20 在最终认证 OK 后固定，全部命令继续复用原编解码。配置及 DSN、能力、包流、列元数据和批量调用点同步接入，无新包/依赖。
- zlib/raw、OB20 请求 ID/双层序号/长度/标志、新 TLV 与旧 extra 跳过、CRC16 及原始 CRC32C 已实现；代理位 3/6 与 OB20 CRC 分别控制。Oracle 新元数据长度适配保留 Go 类型契约；包流内多个逻辑消息按各自预算检查，frame 的 24 位上限独立。
- 两模式本地 `TestPacketModes` 覆盖三类包流的初始化、查询、预处理及逐项批量；framed multi-send 在业务发送前回退 Sequential，完整流水线身份扩展尚未启用。回归覆盖跨帧头/续包/空终止、独立 checksum 向量、错长度/序号/CRC/extra、部分写、取消、request ID 回绕及未读响应不能重置。
- RED→GREEN：缺失 API；聚合合法小消息被误算超限；坏 CRC 消耗的网络读取字节证据丢失；新元数据 RAW/ROWID 长度。独立复核未发现遗留 Critical/Important，没有另存走查报告。
- 最终 Go 1.25.0/1.26.0 Windows amd64 全包 test/vet、协议目标用例重复10次、实际 Windows/386 全包运行通过；两版本普通及 integration 标签 golangci-lint2.14.0 各0 issues。`FuzzFrames` 两版本各30秒通过，分别1466178 / 73653次；integration 两版本仅编译。六份文档的本地路径/相对链接/代码块及32任务/23实现/0验收/155选项计数检查通过。
- `TestPacketModes` 四目标源码及 CI/fuzz 入口已补齐，仅编译，不连接数据库。正向与拒绝登记见兼容矩阵 §14；真实目标协议、版本和代理校验尚未验证。Linux、远程 CI 和成功 race 仍无运行证据；实现23/32、G4 1/8，验收保持0/32。

### D24 PrepareExecute、缓存与执行优化

- 开发：已实现；验证：本地通过，真实目标待联调。根包 ExecutionOptions/DSN、通用合并执行、statement LRU/type 状态，复用既有 Rows、OUT、长参数、游标和批量路径；没有新增包、依赖或循环引用。配置零值保持原行为，支持边界见架构 §10.1.1。
- 普通 Oracle 合并门禁为显式启用和实际 ob_version >=2.2.76；标准 SQL checksum 为 Oracle >=4.1.2，合并 checksum 保持独立尾字段；缓存按物理会话有界，显式 sql.Stmt 和过程所有权不变。完整成功后才保存类型向量，变化、NULL、unsigned、复杂描述与错误有本地验证。
- 两模式缓存复用、LRU、SQL 字节限制、错误驱逐、会话文本失效、Full reset、合并查询/DML、显式 statement、服务端错误不重放及参数黄金包测试通过。最初缺失合并 dispatch 的测试失败后补齐；取消 context 包装和 OUT 清理顺序的回归由既有测试检出并修复。
- 独立走查发现带参数 OpenBlob/OpenClob 的合并路径丢失 locator 捕获与锁交接；两条新增测试先复现 sql.ErrNoRows，再复用共享捕获逻辑修复通过。最终独立复核未发现遗留 Critical/Important；没有保存独立走查报告。
- 最终 Go1.25.0/1.26.0 Windows amd64 全包 test/vet、PrepareExecute/Statement/长参数取消/OUT/LOB 相关用例重复10次、实际 Windows/386 全包运行通过；两版本普通及 integration 标签 golangci-lint2.14.0 各0 issues。扩展后的 FuzzPrepareExecute 各30秒通过，分别396546 / 450199次。32参数编码 benchmark 本地样本 fresh/reuse 分别352/288 B、均1 alloc，不据此推断真实数据库吞吐。
- TestExecuteStrategies 四目标、普通及优化配置的返回值/NULL/类型变化/OUT/重复键/计数与关闭源码已接入 CI，仅编译，不连接数据库。真实版本和服务端类型头/checksum、事务、性能仍待联调；Linux、远程 CI 和成功 race 无新证据。当前实现24/32、G4 2/8，验收保持0/32。

### D25 诊断、profiling 与 Full Link Trace

- 开发：已实现；验证：本地通过，真实目标待联调。根包 Diagnostics/TraceInfo、注入 slog 与 wire 有界快照/FLT，复用既有锁和包流；无新包、依赖、全局 logger/exporter 或后台队列。详细契约见架构 §18.3.1。
- 元数据诊断覆盖命令时长、真实收发字节、SQL 指纹/字节数、安全错误码和丢弃计数；认证、原始 SQL/参数/LOB/错误文本不采集。阈值升级 Warn；logger 和回调均在解锁后同步交付；建连只提供安全总耗时事件。
- 三态 FLT 独立于 logger，支持新/旧 extra、控制和 query 时间、每请求清空 app、采样/打印/慢请求/ShowTrace、16字节 ID/父 span 桥接和有界延后 span 上传。multi-send 仍保持原策略，分别记录写/读后合并，不引入并发访问同一 recorder。
- RED→GREEN 覆盖缺失编码/公开 API/传输协商和流水线诊断。独立走查复现完整但畸形 OK 被诊断为成功，新增 TestDiagnosticMalformedResponse 先失败，再在持锁快照阶段记录解码淘汰失败；复核无遗留 Important/Critical，没有独立报告文件。
- Go 1.25.0/1.26.0 Windows amd64 全包 test/vet 和实际 Windows/386 全包运行通过；诊断/追踪/流水线目标用例两版本重复10次及386回归通过，普通/integration 标签 golangci-lint2.14.0 各0 issues。FuzzFullTrace 两版本各30秒通过，分别2061507 / 1355028次；TestFullLinkTrace 四目标源码及手动 CI 已加入，本阶段仅编译。直连登记 off、代理登记 required，服务端 app 清空、trace/span ID 和实际采样必须另取真实证据；客户端事件不计验收通过。Linux、远程 CI 和成功 race 仍无新运行证据。实现25/32、G4 3/8，验收0/32。

### D26 静态多地址、角色与建连故障切换

- 开发：已实现；验证：本地通过，真实目标待联调。新增根包 routing.go/failover.go 和 internal/routing 的自有地址/选址/健康类型；Config/DSN、connector/reset/tx、CI 和集成源码同步接入；保持唯一公开根包和 root → leaves 依赖，无新增外部依赖、池或后台 goroutine。API/默认值见架构 §17.1.1。
- 最多1024个规范化且唯一的静态候选，组优先/组内顺序、随机、轮转、权重亲和不重复排序；共享健康和冷却按 Connector 隔离。固定 Role 严格过滤；只读服务发送前拒绝写事务，不分析 SELECT，实际主从身份/SQL 权限由调用方与服务端保证。
- MaxAttempts/固定退避/冷却等待均共用总 ConnectTimeout；每次 AttemptTimeout 覆盖拨号、TLS、认证、初始化。认证/证书/模式/配置/初始化失败终止；初始化可能有副作用，绝不重放。TLS 使用实际选中主机；仅收到 TCP 断线而未见拒绝消息时按传输故障判断。可选 idle validation 在 ResetSession 同 Socket PING，失败交标准池淘汰；业务/事务不换线或重放。
- TestAddressSelection/Health/Parsing、TestRoutingDSN/Snapshot/IdleValidation、TestRoleIsolation/ReadOnlyTransaction/TransactionSocket、TestFailoverBudget/AttemptTimeout/AllDown/TotalDeadline/TerminalErrors/HandshakeDisconnect/TLS/NoBusinessReplay 本地通过；覆盖退避/取消、健康过期/并发、隔离、严格参数校验和业务丢响应单次执行。
- 独立走查确认 TLS fatal alert 被 net.OpError/wire.IOError 包装后误重试，以及初始化未共用单次预算，两项已用 TLS1.2/1.3 客户端证书拒绝和慢初始化回归先复现再修复；最终复核无遗留 Important/Critical。TLS 测试使用本地缓冲 TCP 并等待对端完成，避免丢弃 alert 或提前关闭夹具；未保存独立报告。
- Go1.25.0/1.26.0 Windows amd64 全包 test/vet、目标用例各重复10次、实际 Windows/386 全包运行、普通及 integration 标签 golangci-lint2.14.0 各0 issues 均通过。FuzzAddressParsing 两版本各30秒通过，分别1450183 / 2557225次。真实数据库联调按要求不运行。TestConnectionFailover 四目标源码和手动 CI 已加入，仅编译；各 profile 需两个同租户/模式真实入口，测试只注入首个拨号失败、验证活动只读事务不重拨及关闭后新建连恢复，不代替真实宕机或数据库侧未知结果取证。Linux、远程 CI 和成功 race 仍无运行证据。实现26/32、G4 4/8，验收0/32。

### D27 TNS、OCP 与拓扑发现

- 开发：已实现；验证：本地通过，真实目标待联调。根包 DiscoveryOptions/TopologySource、内置 TNS 文件和 OCP HTTP 源、静态 DESCRIPTION/DSN 已接入 Connector；internal/routing 只依赖标准库，未新增公开子包、连接池或第三方依赖。默认值、来源策略映射和服务身份见架构 §17.1.2。
- NewConnector 无 I/O；首次 Connect 才惰性启动一个串行刷新任务。显式 Service、严格 Role、唯一地址和解析预算校验后原子发布候选快照；最后成功快照在 MaxAge 内可用，过期失败关闭降级，已知失败按刷新间隔恢复，不随新调用发起请求风暴。各代共享保留成员的健康/轮转，迟到拨号结果仍可更新新代，成员移除时裁剪健康表。
- Connector.Close/sql.DB.Close 只取消发现，已取得连接继续使用；不变更认证身份/兼容模式、不切换活动 Socket、不重放事务或业务命令。driver.Open 的私有 Connector 无论成功/失败都清理。HTTP 超时/重定向/失败响应和损坏描述明确拒绝；本地常规文件打开前后检查，跟随可信符号链接，操作系统阻塞与违规用户回调不能由 Go 强杀。
- replication 通过显式角色快照/两个标准 sql.DB 适配；Aurora/自有控制面用 TopologySource 接入其真实服务能力，不向 OceanBase 猜测 Aurora SQL。Java OBLB 策略提示校验后不覆盖 Go RoutingOptions；OCP 包装 NET_SERVICE_NAME 为逻辑身份，CONNECT_DATA 不自动切库。真实外部适配器仍逐项待联调。
- TestTNSParser/PolicyHints、TestOCPRefresh/HTTPEnvelope、TestTopologyChanges/RejectsInvalidSnapshots/ConcurrentAndLateResponse/FailureCached/HealthAcrossRefresh/CloseCancelsLookup/CloseBounded，以及公开 Source/DSN/连接预算/活动会话/旧 Open 清理回归本地通过。走查的四处问题均先复现再修复：文件别名绕过、误认 OCP HTTP 格式、driver.Open 刷新泄漏、旧代迟到拨号健康丢失；最终复核无遗留 Important/Critical。不保存独立走查报告。
- Go1.25.0/1.26.0 Windows amd64 全包 test/vet 和实际 Windows/386 全包运行通过，发现刷新/HTTP/关闭相关回归两版本各连续20次通过；普通与 integration 标签 golangci-lint2.14.0 两版本各0 issues。FuzzTNSParser 同时覆盖 TNS 两种别名路径与 OCP，各30秒通过，分别679357 / 802846次。HTTP 清理回归经高负载发现服务端晚到请求的测试误判，改为有界等待静默；临时禁用生产清理时确认新测试仍失败，恢复后全量验证通过。符号链接正向用例因本机创建权限跳过，Linux 等可用平台仍需补取证。
- TestDiscovery 四目标 TNS/OCP 源码和手动 CI 已加入，仅编译；部署方提供真实身份 SQL/预期租户和角色，固定会话验证来源声明，不将常量 SQL 或初次连接代替真实刷新证据。外部复制/Aurora 真实发现、Linux/远程 CI 与成功 race 无新运行证据；按要求不连接数据库。实现27/32、G4 5/8，验收保持0/32。

### D28 完整认证与平台传输（进行中）

- 首批代码：old password、client_ed25519、PAM dialog，Auth.PasswordCharset、DSN allowOldPassword/passwordCharset，以及实例 io.Closer 生命周期。内置算法留在 internal/auth；根包负责 TLS、模式、编码和会话，未新增包或循环依赖。Ed25519 使用纯 Go filippo.io/edwards25519 v1.1.0；不手写曲线算法、不加载本地动态库。
- TestAuthPluginLifecycle/TypedNil、Algorithms、ExtendedHandshake/LegacySwitch/OldPasswordPolicy、PAM/Gate/Fallback/Failure/UnverifiedHandshake、PasswordCharset/OptionsDSN 覆盖实例清理、关闭错误、初始/切换、二进制末尾零、弱算法禁止、回答取消/NUL/错误提示及自定义传输统一门禁。FuzzAuthPlugins 覆盖算法及 PAM 提示。未连接数据库；真实服务插件和平台组合仍待联调。
- Shared Memory/OBProxy VC 仍需开发；Unix GSSAPI 的系统库运行/真实票据、Windows Kerberos 的真实域票据及其余服务组合待验证；公开 ChangeUser 归 D30。D28 保持进行中，实现完成仍27/32，验收0/32；不以部分子项代替完整任务。
- 首批本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 全包测试及 integration 源码编译通过；AuthPlugin 两版本各10次通过，普通与 integration 标签 golangci-lint2.14.0 两版本均0 issues。FuzzAuthPlugins 各30秒通过，1180142 / 629696次。独立走查发现 PAM 切换重复剥离终止符，回归先失败后修复通过；无其他遗留已确认问题。Linux/远程 CI/成功 race 和真实服务认证仍无新证据。
- 后续传输子项：Config.Network 和 DSN network/socket、Unix 标准库拨号、Windows Named Pipe build tags/go-winio 已实现；IPC 单端点、显式 TLS ServerName、不回退 TCP、保留统一取消/明文门禁。TestPlatformTransport 覆盖本机原生 socket/管道的协议收发、配置/DSN 和解析配置再建 Connector、超时/关闭及默认明文拒绝；Shared Memory 的原生事件/映射和分配取消继续取证，VC 的 native ABI 仍需核对。
- 传输子项本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、各10次 PlatformTransport、实际 Windows/386 全包测试及 integration 源码编译通过；普通与 integration 标签 golangci-lint2.14.0 两版本均0 issues，go mod tidy -diff 无差异。Go1.25 Linux/amd64 全包构建和根包测试源码交叉编译通过，仅编译不代表运行。独立走查发现缺失管道未进入配置重试，原生缺失后恢复回归先失败后修复通过；最终复核无其他已确认问题。远端 UNC 的同步 CreateFile 不能由 context 严格中断，已在架构 §9.0.1 记录；真实服务、远端权限、Linux 运行和成功 race 仍待验证。
- GSS 子项：新增可选 AuthConversation，建立上下文前完整保留原始令牌首字节与切换种子末尾零；nil 不发包、非 nil 空切片发送空包，上下文完成后仍须最终 OK。各轮保留 TLS/context 门禁及实例清理。Auth.ServicePrincipalName/DSN servicePrincipalName 已接入校验。Windows 使用当前进程身份和原生 SSPI，Kerberos 检查实际 mutual-auth 标志，NTLM/Negotiate 每轮要求驱动验证 TLS；Unix 提供方的后续实现及启用条件见下文。
- GSS 子项本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、GSS/Conversation 各10次、实际 Windows/386 全包测试及 integration 源码编译通过；普通与 integration 标签 golangci-lint2.14.0 两版本均0 issues。Go1.25 Linux/amd64 全包构建和根包测试源码交叉编译通过，go mod tidy -diff 无差异。FuzzAuthPlugins 扩展种子解析，各30秒通过846952 / 841195次。受限进程因 SEC_E_NO_CREDENTIALS 跳过原生身份用例；在当前用户身份下已实际通过原生 NTLM 令牌和经验证 TLS 的完整 SSPI 握手，并完成双版本/386 检查。原始令牌/切换终止符回归先失败后修复通过，独立走查无遗留已确认重要问题。Windows 域 Kerberos/Negotiate、真实数据库、Linux 运行和成功 race 仍未验证；SSPI 同步系统调用的 context 时限边界见架构 §17.2.1。
- Unix GSS 子项：Linux amd64/arm64/386、macOS amd64/arm64 的内置提供方已实现，通过 -tags=gssapi 显式启用；purego v0.11.1 调用固定系统库，保持无强制 cgo/头文件构建。启用后需原生加载器/C 运行库；默认构建不导入 purego，保留静态部署能力。每次从默认缓存取得 Kerberos initiator 凭据，导入固定 SPN 并校验实际 mutual 标志；不读数据库密码、不修改进程环境、不请求委派。RFC2744 缓冲区/OID/集合的 ABI 宽度、续轮上下文和内存固定/复制/释放、错误返回仍带句柄及幂等关闭均有测试；当前 Windows amd64 实际编译和运行独立 C ABI 对端，不能据此声称 Linux/macOS 的系统库已运行。公开配置和包依赖方向不变。
- CI 已为 Linux 加入 -tags=gssapi,gssapitest 的全量测试，包括系统 GSSAPI 无凭据失败及原生 ABI 检查，OB_TEST_GSS_REQUIRE=1 禁止以缺库跳过该 CI 检查；测试专用 gssapitest 标签需要 C 编译器，普通驱动构建/测试无需。真实 Unix 票据双向交换、系统库/平台运行和远程 CI 结果继续待取证；Shared Memory/VC 继续开发，D28 保持进行中。
- Unix GSS 子项本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、实际 Windows/386 全包测试、GSS 定向各10次、独立 C ABI 各3次及 integration 源码编译通过；普通/integration/gssapitest 的 Windows lint 与 gssapi,gssapitest 的 Linux lint 两版本均0 issues。两个工具链分别完成 Linux amd64/arm64/386、macOS amd64/arm64 的启用标签构建和 ABI 测试源码交叉编译，仅代表编译证据。默认 Linux CGO_ENABLED=0 根包测试 ELF 均无 PT_INTERP/PT_DYNAMIC，默认依赖图不含 purego，启用标签后包含。native Step 前取消的回归先失败再通过；独立走查无遗留已确认重要问题。go mod tidy -diff 无差异；未连接数据库，Unix 系统库运行、真实票据、远程 CI 和成功 race 仍未验证。实现27/32、验收0/32不变。

### D29 LOCAL INFILE 受控导入

- D29 依赖 D06/D21，可独立于尚未完成的 D28 推进；本次不把 Shared Memory/VC 的协议取证问题计为完成。新增根包 `Import` / `LocalInfile`，DB/Conn/Tx 同会话执行、一次性请求授权，精确名称 Reader 或授权 Open，大小/时限及来源关闭归属见架构 §17.3.1。没有新增包、依赖、全局文件状态或循环依赖。
- 复用 longSource 的读取/取消/限额与既有 Rows 响应状态机；标准/压缩/OB20 上传使用 wire 延续写入，包序号回绕不重启 trace/诊断。正常结束后强制最终 OK/ERR；来源错误、拒绝、取消、超限和畸形响应淘汰连接；不隐式重放或提交事务。
- TestLocalInfilePolicy/Failure/SourceFailure/Resolver/Timeout/Validation/ServerResponse/Transaction/SequenceWrap/FilenameCharset/Framing 覆盖普通查询无授权、精确名称、回调/Reader 所有权、nil/畸形来源、超限/取消/最终确认超时、重复请求、最终 ERR 后复用、同事务和分块/序号回绕；TestContinuationSequenceWrap 验证三种包流及同一诊断记录。独立走查发现最终结果集被误当导入确认，回归先失败再通过；会话 GBK 文件名匹配也已先复现再修复，内容保持原始字节。
- TestLocalImport 四目标源码与手动 CI 已加入：入库计数/内容、提交/回滚、事务内已发送部分输入后超限、会话淘汰及新连接检查。Oracle 模式需提供实际目标的 LOCAL_IMPORT_SQL 模板，缺失时强制验收失败；普通缺失配置可跳过且不计通过。本阶段仅编译源码，不连接数据库，不声称真实 SQL 或服务端事务行为已验证。
- 本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、导入/延续包目标各10次、实际 Windows/386 全包测试及 integration 源码编译通过；普通与 integration 标签 golangci-lint2.14.0 两版本均0 issues。两版本 Linux/amd64 CGO_ENABLED=0 全包构建和根包测试源码交叉编译通过，go mod tidy -diff 无差异；仅编译，不计 Linux 运行。走查的最终确认与字符集问题均有先失败后通过的回归，无遗留已确认重要问题；完整双向压缩/OB20及部分传输失败事务的集成源码已补。实现28/32、G4 6/8，验收0/32；数据库、Linux 运行、远程 CI 和成功 race 仍未验证。

#### D30 首批：会话辅助与 warning 数量快照

- 开发：进行中，尚不计入已实现项。新增根包 session_ops.go、单元测试与四目标 integration 源码，不增加包、第三方依赖或连接包装对象。普通 sql.Conn 上封闭函数承载 schema/catalog、客户端信息及系统变量；实际模式负责 SQL/名称/编码。
- setter 在发送前拒绝活动事务/autocommit 关闭及受保护编码/时区变量，所有 setter 发送前清理自动 statement 和类型描述缓存（含 sql_mode 解析语义变更）；发送修改后关闭 pinned 连接时淘汰，避免原池状态泄漏，完整服务端错误也不恢复为可回池。校验失败保持原会话可复用。
- Warnings 和每项 ResultInfo.Warnings 保留 OK/EOF 的数量及已知性；普通 SQL 发送后替换快照，helper/Ping/内部清理不覆盖，归还池后清空。没有把成功带 warning 改为错误，也不依赖 logger。warning 详情和 CancelQuery 尚未实现；ChangeUser 见本节后续子项，整项 D30 清单保持未完成。
- TestSessionOperations/MutationIsolation/MutationTransaction/OperationValidation/OperationResponse/OperationQuoting/Warnings 覆盖两模式、转义、事务/池隔离、服务端错误与额外结果拒绝、数量快照及 nil logger。TestSessionOperations 集成源码验证 schema/client info/变量回读、快照与新连接无属性泄漏；仅编译，未运行。真实 warning 详情及身份切换/取消仍需后续用例。
- 独立走查发现 sql_mode helper 可能复用旧解析语义的自动 statement，TestSessionMutationStatementCache 先失败后通过；所有 setter 统一清理自动 statement/类型描述缓存。修复后 Go1.25.0/1.26.0 Windows amd64 全包 test/vet、目标重复10次及实际 Windows/386 全包测试通过；两版本普通/integration 标签 golangci-lint2.14.0 各0 issues，集成源码只编译。六文档链接/代码块/无机器路径、32任务/28实现/0验收/155选项及 CI YAML 检查通过。未连接数据库，未验证 Linux 运行、远程 CI 或成功 race；D28/D30 保持进行中。

#### D30 ChangeUser 子项

- 已补根包 ChangeUser(ctx, *sql.Conn, Credentials, database)，在原物理连接及原标准/压缩/OB20 包流内重新认证，沿用已验证 TLS 和认证插件策略，不重拨、不修改原 Connector。仅允许无事务且 autocommit 开启的固定连接；参数、字符集及初始认证安全门禁先校验，失败保留原身份。
- 切换前清理自动 statement、类型描述及 LOB/Cursor；显式 server/client statement 通过会话代次失效，旧 Close 不关闭新身份可能复用的 statement ID。发送后认证、取消、模式或初始化失败均淘汰会话；成功重新执行 Config.Session 基线，固定连接关闭后退出原池，不保留新密码。不新增包、依赖或反向引用。
- TestChangeUserIsolation/Failure/Validation/Encoding/Database/PluginLifecycle/Cancel/VerifiedTLS/Framing 覆盖两模式、两种预处理、旧句柄失效、ID 复用、原池隔离、认证插件及关闭、安全拒绝、同连接 TLS、取消和三种包流。TestChangeUserSessions 四目标及手动 CI 输入已补，源码仅编译，未连接真实服务。
- D30 仍进行中，warning 详情和显式取消尚未实现；实现28/32、验收0/32不变。
- 本地验证：Go1.25.0/1.26.0 Windows amd64 全包 test/vet、ChangeUser 各10次及实际 Windows/386 全包测试通过；两版本普通/integration 标签 golangci-lint2.14.0 各0 issues，integration 源码仅编译。两版本 Linux/amd64 CGO_ENABLED=0 全包构建与根包测试源码交叉编译通过，go mod tidy -diff 无差异。独立走查无遗留已确认问题；六文档编码/链接/代码块/无机器路径、32任务/28实现/0验收/155选项及 CI YAML 检查通过。未连接数据库，Linux 运行、远程 CI 和成功 race 仍未验证。

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

1. 继续 D28 Shared Memory/VC 取证及 D30 warning 详情、显式取消；保持单公开根包、池隔离与单向依赖，不把首批辅助函数计作整项完成。
2. 继续用故障对端验证标准 database/sql 查询，不连接真实数据库。
3. 保留完整 G2–G6 范围；数据库联调暂缓，Linux/CI 和所有未执行条目如实保留。

本表只管理开发事实和后续动作，不保存独立走查报告，也不预估尚无依据的完成日期。
