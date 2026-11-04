# 原生会话重置与剩余矩阵项复验（2026-11-03）

范围：OceanBase 4.4.2.2 直连，MySQL 兼容模式与 Oracle 兼容模式。连接信息只经进程环境变量提供，未写入仓库；一次性 raw-wire/公开 API 探针放在本地忽略目录（`docs/superpowers/_probes/`），不进入交付物。本文记录本轮新增证据及其对矩阵分类的影响，不声明所有版本、代理、平台组合已通过。

## 1. COM_RESET_CONNECTION（0x1F）实测：服务端双模式可用

| 观察项 | MySQL 模式 | Oracle 模式 |
| --- | --- | --- |
| 0x1F 返回 | OK（status=2，warnings=0），连续两次成功 | OK（status=0），连续两次成功 |
| 重置后 `SELECT 1 [FROM dual]` | 成功 | 成功 |
| 默认库 / 客户端字符集 | `DATABASE()=risdb`、`@@character_set_client=utf8mb4` 保持 | — |
| autocommit | 重置后为 1 | — |
| 重置前 prepare 的语句句柄 | 再执行返回 1243/HY000 | 再执行返回 600/HY000 |
| 驱动初始化过的会话设置 | 被服务端恢复默认 | `SESSIONTIMEZONE` 从 `+00:00` 变为 `+08:00`（服务端默认） |

结论：4.4.2.2 双模式都实现了 COM_RESET_CONNECTION，语义是“连接级会话全量重置 + 保留物理连接”。它也清除了驱动初始化写入的 `time_zone`，因此若使用，必须重放连接基线。

## 2. 为什么 `ResetFull` 仍以淘汰物理连接实现

Go `database/sql` 为 `*sql.Stmt` 保存 (物理连接, 服务端语句 id) 绑定；同一物理连接再次被取用时，它会直接发送旧的 COM_STMT_EXECUTE。实验把 `ResetFull` 临时改为发送 0x1F 后：`PrepareContext` 的语句在重置后仍以旧 id 执行（对端脚本捕获到旧 id 的 COM_STMT_EXECUTE），而目标对该句柄返回 1243。要恢复正确性，只能让 `ResetSession` 返回 `driver.ErrBadConn`（即淘汰连接），或要求调用方在归还连接前关闭全部 `*sql.Stmt`，后者不是可接受的库契约。

因此 D09 保持：默认 Managed 只修复驱动管理的事务/autocommit 状态；Full 淘汰物理连接；不使用 COM_RESET_CONNECTION。这是标准库语句所有权造成的客户端边界，不是服务端能力不足。若将来 `database/sql` 提供语句失效通知，可重新评估。

## 3. C02：真实 PREPARE 1295 与回退入口

MySQL 模式枚举 44 条语句做独立 server PREPARE（SHOW WARNINGS/SHOW COUNT(*) WARNINGS/SHOW VARIABLES/SHOW TABLES/SHOW DATABASES/SET autocommit/SET NAMES/SET SESSION/USE/事务/SAVEPOINT/锁/DDL/EXPLAIN/DESC/ANALYZE/OPTIMIZE/FLUSH/KILL/HELP/DO/查询/INSERT/REPLACE/TRUNCATE/GRANT/SET GLOBAL/XA/多语句）：

- 除 1146（对象不存在）与 1235（多语句 prepare 不支持）外，全部成功获得语句 id，该列表内没有 1295。
- 补充 26 条过程/事件/复制/权限等语句后，`CREATE PROCEDURE` 与 `DROP PROCEDURE` 返回 **1295**；`CREATE FUNCTION`、`CREATE TRIGGER`、`CREATE VIEW`、`CREATE EVENT` 可正常 prepare。
- 带占位符的同名变体（`LOCK/ANALYZE/TRUNCATE/DESCRIBE/SHOW/USE/RENAME/GRANT/CREATE USER/CALL/SAVEPOINT/XA/HANDLER/PREPARE/EXECUTE/SET GLOBAL/LOAD DATA/CREATE PROCEDURE/DROP PROCEDURE/CREATE EVENT/DROP EVENT`）全部返回 1064，没有“带参数且 1295”的语句。
- 结论：1295 只出现在无参数命令上；这类命令在没有参数时走文本协议，因此有参数的自动 prepare 回退在本目标不可达。显式 `sql.Prepare` 路径已扩展为同一 1295 回退，并实库验证 `CREATE/DROP PROCEDURE`（`obtest prepare-fallback`，2026-11-03 第二套目标）。

## 4. Oracle warning 详情：目标没有可用明细入口

Oracle 模式执行 `SHOW WARNINGS`、`SHOW COUNT(*) WARNINGS` 均返回 900/42000；`DBA_ERRORS` 返回 942/42S02。与实现一致：Oracle 模式只能从 OK/EOF 得到 warning 计数，无法读取明细；C11 保持客户端范围，不做猜测式实现。

## 5. 本轮验收与集成基线

- `cmd/obtest` 直连：MySQL **36 pass / 0 fail / 3 skip / 8 not applicable**；Oracle **40 pass / 0 fail / 2 skip / 5 not applicable**。合计与既有 76/0/5/13 一致。跳过项：MySQL XA SUSPEND/JOIN（服务端 1398）、Oracle NCLOB（902）、两模式 ChangeUser（缺第二身份）。
- `go test -tags integration ./...`：已运行到的包全部通过；`internal/statement` 68s、`internal/valuecodec` 全量较慢。Geometry 16 例通过，其中 8 个 SRID 4326 拒绝各约 9 秒，是服务端 SRS 查找耗时，不是驱动挂起或读超时缺陷。
- 本轮未覆盖：OBProxy 双端点、TLS/mTLS、第二身份/授权、托管压缩端点、SRID 4326 字典、命名时区部署。

## 6. 对矩阵的影响

- 兼容性矩阵 §2 的 reset 行：由“native reset unimplemented”更新为“服务端可用，但标准库语句绑定使驱动保持淘汰语义”。
- 选项映射 `useResetConnection` 行同步该边界。
- 缺失矩阵 C02/C11 补充本轮证据；U01/U02/U03 没有新的成功 wire 证据，分类不变。
