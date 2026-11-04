# ChangeUser 第二身份验收（2026-11-03）

范围：OceanBase 4.4.2.2 直连 MySQL 兼容模式。为闭环 A02/D30 的身份切换验收，经用户授权，用当前 root 账号创建**一次性**测试身份（仅测试库只读），验收后删除。凭据只经进程环境变量传递，未写入仓库。

## 一次性身份

- `CREATE USER '<user>'@'%' IDENTIFIED BY '<random>'`，随后 `GRANT SELECT ON <test-db>.*`（只读，无 DDL）。
- 登录必须使用 `<user>@<tenant>`：OceanBase 直连按 `user@tenant` 认证，仅写用户名会返回 1045。
- 验收结束后执行 `DROP USER IF EXISTS '<user>'@'%'`；仓库与文档均无凭据、无真实用户名。

## 验收结果

| 检查 | 结果 |
| --- | --- |
| `TestChangeUserSessions/mysql`（`OB_TEST_REQUIRE=1`） | pass：原身份→第二身份切换、旧 prepared 语句失效（ErrResource）、切换后查询与初始化基线、原池隔离 |
| `cmd/obtest` 的 `change-user` + `session`（`require_all=true`） | 2 pass / 0 fail / 0 skip |
| 全量 MySQL obtest（含身份 fixture） | **37 pass / 0 fail / 2 skip / 8 not applicable**；跳过仍为 XA SUSPEND/JOIN（服务端 1398） |
| 新增 `TestChangeUserFailure`（错误密码） | 服务端 1045/42000；pinned 连接弃用（`driver.ErrBadConn`）；池可重新建连 |
| 集成 + `-race` | pass |

`TestChangeUserFailure` 是本次新增的真实库用例（`internal/control/integration/change_user_failure_integration_test.go`）：有第二身份 fixture 时执行，缺 fixture 时按既有规则 skip。

## 未闭环与边界

- Oracle 租户账号 `RISDB` 没有 CREATE USER 权限（1031/HY000），无法创建 Oracle 第二身份；Oracle 的 ChangeUser/ProxyUser 授权路径仍归 A02/ENV 外部前提，不能用业务账号改权限来制造通过。
- OBProxy 端点暂不可用，四目标验收（P-MP/P-OP）仍缺。
- 一次性用户已删除；如后续需要复跑身份验收，需要外部提供第二个身份或重新授权创建临时账号。
