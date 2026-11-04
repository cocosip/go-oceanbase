# 待实库验证清单（目标恢复后执行）

更新日期：2026-11-04。原第三套直连目标（OceanBase 4.4.2.2 双模式）在 2026-11-04 复验过程中开始对任意语句返回 `wire: read after 0 bytes: EOF`（TCP 2881 仍可建连），随后用户确认为**数据库服务器损坏、恢复时间未知**。本文档把恢复后需要补做的验证按顺序列出，避免遗漏；不含任何 DSN、口令或地址。

## 1. 前置条件

- 环境变量（由使用者在本机 shell 设置，仓库与日志中不出现；`Linux 环境`指运行驱动的**客户端**，不是服务端操作系统）：
  - `OB_MYSQL_DSN`：MySQL 模式 DSN（`oceanbase://用户@主机:端口/库?mode=mysql`）
  - `OB_ORACLE_DSN`：Oracle 模式 DSN（`oceanbase://用户@主机:端口/库?mode=oracle`）
- 首次连通性探测（任一失败即先处理环境，不要直接跑验收）：

```powershell
go run ./cmd/obtest -config <config.json> -check
```

## 2. 两个已注册但未实库验证的场景

| 场景 | 预期 | 重点断言 |
| --- | --- | --- |
| `identifier-length` | 两模式 pass | 最大长度表名（MySQL 64 / Oracle 128 字符）与 128 字符列名经 DDL、绑定插入、查询及表/列字典**逐字符不变**；超限 1 字符被 1059 / 972 拒绝 |
| `topology-sources` | 两模式 pass | 场景本身不连库，仅验证 inline TNS / tnsnames.ora / OCP 源的解析与拒绝路径；harness 仍会先探测目标 |

命令：

```powershell
# 仅这两个场景
$cases = '{"targets":[{"name":"mysql","dsn_env":"OB_MYSQL_DSN"},{"name":"oracle","dsn_env":"OB_ORACLE_DSN"}],"timeout":"3m","cleanup_timeout":"90s","cases":["identifier-length","topology-sources"]}'
```

## 3. 三个必须先查清的疑点

1. **MySQL 长表名是否被字典截断**：故障窗口内一次 `identifier-length` 实跑在"表名字典被截断"断言处失败，时间窗与服务器开始降级重合，无法判定。恢复后先单独跑该场景；若仍失败，用纯 SQL 对照：
   `SELECT LENGTH(TABLE_NAME),TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA=<当前库> AND TABLE_NAME=<64 字符名>`。
2. **`ALL_COL_PRIVS` 权限列名**：OceanBase 文档该页写作 `PRIVILLEGE`，但 `USER_COL_PRIVS`/`DBA_COL_PRIVS` 页与驱动都使用 `PRIVILEGE`。用一个已授权列权限的账号执行：
   `SELECT PRIVILEGE FROM USER_COL_PRIVS WHERE ROWNUM=0`（能解析即列名正确）。
3. **权限字典 0 行是否符合预期**：对象的隐式权限不在字典中，需用"被显式授予表/列权限的第二个账号"验证 `MetadataTablePrivileges`/`MetadataColumnPrivileges` 返回非空且 grantee/grantable 正确。

上述疑点 1 若确认是字典截断，属于 `MetadataTables` 缺陷，需要修驱动并补离线测试；2、3 目前无证据表明是缺陷。

## 4. 全量回归

```powershell
go run ./cmd/obtest -config <config.json> -report <不存在的路径>.json
```

预期：**72 场景 × 2 目标 = 144 项 → 112 pass / 0 fail / 5 skip / 27 not applicable**（MySQL 54/0/3/15；Oracle 58/0/2/12）。其中 skip 仍是目标侧拒绝与缺失夹具：MySQL XA suspend/join 1398、Oracle NCLOB 902、两模式 `change-user`（缺第二身份）。

## 5. 清理确认

所有验收对象均以唯一前缀 `OBT_OFFLINE_*` 创建并在场景结束时 DROP；恢复后执行一次：

```sql
-- 两模式分别执行；结果应为 0 行
SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_NAME LIKE 'OBT\_OFFLINE\_%';
SELECT VIEW_NAME FROM information_schema.VIEWS WHERE VIEW_NAME LIKE 'OBT\_OFFLINE\_%';
-- Oracle 模式
SELECT OBJECT_NAME,OBJECT_TYPE FROM USER_OBJECTS WHERE OBJECT_NAME LIKE 'OBT\_OFFLINE\_%';
```

## 6. 完成后更新的文档

- [第三套目标复验](live-validation-2026-11-04-third-target.md)：补跑结果与新基线。
- [测试工具指南](go-driver-test-tool.md)：场景表计数与"已注册/已实库"口径。
- [开发进度](go-driver-development-progress.md)：新条目与实现/验收计数。
- [缺失矩阵](go-driver-completeness.md)：V07 行与验收相关结论。
- [交接说明](continuation-handoff.md)：基线命令与预期输出。

## 7. 恢复后 30 分钟最小验证序列

目标恢复后按此顺序执行即可得到可发布结论（每步失败即停止并记录）：

| # | 步骤 | 命令/动作 | 预计 |
| --- | --- | --- | --- |
| 1 | 配置与连通性 | `go run ./cmd/obtest -config <config.json> -check` | 1 min |
| 2 | 两个待验场景 | 见 §2 命令 | 2 min |
| 3 | 全量回归 | 见 §4 命令，预期 112/0/5/27 | 5 min |
| 4 | 三个疑点核对 | 见 §3 的三条 SQL/场景 | 5 min |
| 5 | 残留清理确认 | 见 §5 的查询，应为 0 行 | 2 min |
| 6 | 更新文档 | 见 §6 | 10 min |

若第 2 步 `identifier-length` 仍失败且 §3 疑点 1 的纯 SQL 对照显示表名被截断，则按"驱动缺陷"处理：修 `MetadataTables` 查询并先补离线测试，再重跑第 2、3 步。
## 8. 目标恢复但没有 TLS 时：能做 / 不能做

**结论：没有 TLS 不影响绝大多数验收。** TLS 只是 ENV01/V01 专项与个别依赖"verified TLS"的路径；普通 DSN 直连即可完成下列工作。

| 可做（无需 TLS） | 说明 |
| --- | --- |
| 两模式连通性与 `-check` | 普通 TCP 2881 直连 |
| `identifier-length`、`topology-sources` | 前者连库；后者不连库（TNS/OCP 源解析） |
| 全量 72 场景（预期 112/0/5/27） | 场景本身不使用 TLS |
| 全量集成测试（配 DSN） | 未提供 TLS 的用例按既有规则跳过，其余实跑 |
| V05 故障/XA、V06 耐久、V07 元数据、A01/A02、B22/B20 疑点复验 | 均为普通连接上的行为 |
| 跨平台/CI 证据的 Windows 部分 | 本机 |

| 仍不能做（需要 TLS 或其它部署） | 原因 |
| --- | --- |
| ENV01 / V01 TLS/mTLS 正向验收 | 服务端未开 TLS，客户端证书校验/轮换没有真实端点 |
| 依赖 verified TLS 的 D28 GSSAPI 路径 | 该路径要求已验证 TLS 的连接 |
| ENV02 required compression、ENV03 ODP/多节点/OCP、ENV04 插件/KDC、B21 代理重认证 | 缺对应端点/控制面/插件 |
| D28/D30/D31/D32 的 Linux 侧子项 | 需要**客户端** Linux 运行时（Unix socket/VC/系统 GSSAPI） |

TLS 客户端逻辑本身已有本地证书 peer 测试（`tls.Config`、证书校验、mTLS 用例），缺的只是"真实服务端部署"证据，不需要为它重写代码。