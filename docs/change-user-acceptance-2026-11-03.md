# ChangeUser 第二身份验收（2026-11-03）

范围：OceanBase 4.4.2.2 直连，MySQL 与 Oracle 两种兼容模式。为闭环 A02/D30 的身份切换验收，经用户授权创建**一次性**第二身份（MySQL 由 root 创建、Oracle 由 SYS 创建），验收后删除。凭据只经进程环境变量传递，未写入仓库。

## 一次性身份

- MySQL：`CREATE USER '<user>'@'%' IDENTIFIED BY '<random>'`，`GRANT SELECT ON <test-db>.*`（只读）；直连必须使用 `user@tenant`，只写用户名会返回 1045。
- Oracle：`CREATE USER <USER> IDENTIFIED BY "<random>"`，`GRANT CREATE SESSION TO <USER>`；业务账号 `RISDB` 只有 CONNECT/RESOURCE，不具备 CREATE USER，因此由租户管理员 SYS 创建。
- 验收结束后两种身份均删除；仓库与文档无凭据、无真实用户名。

## 验收结果

| 检查 | 结果 |
| --- | --- |
| `TestChangeUserSessions/mysql`（`OB_TEST_REQUIRE=1`） | pass：原身份→第二身份切换、旧 prepared 语句失效（ErrResource）、切换后查询与初始化基线、原池隔离 |
| `cmd/obtest` 的 `change-user` + `session`（`require_all=true`） | 2 pass / 0 fail / 0 skip |
| 全量 MySQL obtest（含身份 fixture） | **37 pass / 0 fail / 2 skip / 8 not applicable**；跳过仍为 XA SUSPEND/JOIN（服务端 1398） |
| `TestChangeUserFailure/mysql` | 服务端 **1045/42000**；pinned 连接弃用（`driver.ErrBadConn`）；池可重新建连 |
| `TestChangeUserSessions/oracle` | pass：身份切换、旧语句失效、切换后查询与初始化基线、原池隔离 |
| `TestChangeUserFailure/oracle` | 服务端 **600/42000**（OceanBase 把 OBE-01017 映射为内部 600）；pinned 连接弃用；池恢复 |
| 集成 + `-race` | pass |

`TestChangeUserFailure` 是新增的真实库用例（`internal/control/integration/change_user_failure_integration_test.go`）：有第二身份 fixture 时执行，缺 fixture 时按既有规则 skip；错误码按模式断言（MySQL 1045、Oracle 600），服务端映射变化会显式失败。

## 未闭环与边界

- Oracle 业务账号无建用户权限属预期最小权限；后续复跑身份验收仍需管理员创建一次性身份。
- ProxyUser：驱动把 `proxyUser` 作为连接属性发送；真实的 O7L / `GRANT CONNECT THROUGH` 授权与目标身份校验仍缺 fixture，Oracle 模式 ProxyUser 未验收。
- OBProxy 端点暂不可用，四目标验收（P-MP/P-OP）仍缺。
- 一次性用户已删除；如后续需要复跑身份验收，需要外部提供第二个身份或管理员重新授权创建临时账号。
