# 第二套直连目标实库复验（2026-11-03）

环境：独立提供的 OceanBase 4.4.2.2 直连 MySQL/Oracle 双模式，Windows amd64、Go 1.27.2；连接信息只经进程环境变量传入，本文不保存地址、账号、密码或 DSN。端点未开启 SSL，无 OBProxy。一次性第二身份（MySQL/Oracle）与 Oracle 代理账号/授权仅用于本次验收。

## 结果

| 项 | 结果 |
| --- | --- |
| 离线基线 | `go build ./...`、`go vet ./...`、全量 `go test ./...`、`golangci-lint run ./...`（0 issues）；linux/amd64、linux/arm64、darwin/arm64、windows/386 及 integration/gssapi/obproxy tag 交叉编译通过 |
| `cmd/obtest` 双目标 | **79 pass / 0 fail / 3 skip / 14 not applicable**（MySQL 38/0/2/8；Oracle 41/0/1/6）。非零退出仅来自既有 skip 保留策略 |
| 全量集成测试 | `go test -tags integration ./...` 在双模式全部 `ok`，无 FAIL |
| ChangeUser | 一次性第二身份下 MySQL 与 Oracle 均通过：`obtest` change-user 2 pass；`TestChangeUserSessions`、`TestChangeUserFailure` 双模式通过 |
| ProxyUser | Oracle 初始代理登录通过（`CURRENT_USER`=目标、`PROXY_USER`=认证代理账号）；`COM_CHANGE_USER` 重认证后代理目标未生效，驱动已对该确切版本在发送前拒绝（B21），见下节 |
| 受限账号元数据 | MySQL SELECT-only 与 Oracle `CREATE SESSION`+按表授权身份下，`TestMetadataRestrictedVisibility` 双模式通过：只看到被授权表的 1 表/3 列/1 主键，读取不可见 schema 返回 0 行 |
| 元数据过滤组合 | `TestMetadataCatalog` 双模式通过：`ColumnPattern` exact/前缀、表名 `%`/`_` 通配、kind 不适用过滤与非法表类型的 `ErrBind`；MySQL 另验证外键引用 schema/name 过滤；Oracle 引用连接约 9.7s 并超默认 10s 超时（B22），该组合在 Oracle 跳过 |
| Prepare 回退 | `obtest prepare-fallback`：`CREATE/DROP PROCEDURE` 的独立 server PREPARE 返回 1295；关闭 `EmulateUnsupportedPstmts` 时保留 1295，开启后显式 `sql.Stmt` 回退为 client 插值并真实建/删过程；带占位符变体为 1064 |
| G 项 | G01/G03/G04/G05a/G07a/G07b 指定子集在集成套件中复验通过 |
| U 项与目标拒绝 | `TestOraclePLTypeReadiness` 仍按契约断言：RECORD/ANYDATA 原生 OUT 返回 600/0A000（B16）；匿名 LOB 头 600/HY000（B18）；NCLOB DDL 902（S01）；MySQL XA SUSPEND/JOIN 1398（B01） |
| 收据丢失 | 双模式普通 COMMIT、MySQL XA PREPARE/COMMIT 与新增的 **Oracle combined-PL XA PREPARE/COMMIT**（保留 OUT 行、丢弃尾部 native OK）均通过；`ErrXAOutcomeUnknown`/`ErrCommitUnknown` 分类、独立恢复与效果可见性断言通过 |
| 其余 | XA 1PC/2PC/rollback/recovery（Oracle 含 suspend/join）、敏感游标、LOB、复杂类型、LOCAL INFILE、warnings、cancel、metadata、批处理在双模式通过 |

## 新发现：Oracle 代理身份在 COM_CHANGE_USER 上不生效

- 初始登录：`User`=代理账号、`ProxyUser`=目标时，服务端返回 `CURRENT_USER`/`SESSION_USER`=目标、`PROXY_USER`=代理账号，符合既有契约。
- 同一会话再用相同凭据调用 `ChangeUser`：`CURRENT_USER`/`SESSION_USER` 变为代理账号，`PROXY_USER` 仍为代理账号；服务端返回成功，未报错，也未应用代理目标。
- 驱动确实在该报文重传 `__ob_client_proxy_user_name`（`internal/control/change_user.go` + `internal/connect/protocol.go`，本地 peer 用例覆盖）。因此这不是"客户端未发送"，而是该 4.4.2.2 目标的 `COM_CHANGE_USER` 行为，记录为 **B21**（指定目标静默差异），不升级为已确认客户端缺实现。
- 影响：在 4.4.2.2 上用 `ChangeUser` 切换代理目标时，实际会话身份是认证账号，而驱动按请求记录目标身份。**已实现防护**：该确切版本在发送前返回 `ErrUnsupported`（`TestChangeUserRejectsProxyTargetOnIgnoringVersion`），`TestProxyUserSessions` 在该目标跳过重认证段并保留初始登录断言；其他版本仍按未验证处理。

## 未覆盖（仍为 ENV/V 边界）

TLS/mTLS（端点未开 SSL）、压缩（端点不提供 required compression）、OBProxy/多节点/OCP/TNS、Linux/macOS 原生运行、Oracle combined-XA 收据丢失、named-region 时区初始化（1882）。这些不是本次客户端缺陷证据。

## 证据与命令

- `go test -tags integration ./... -count=1`（双 DSN 进程环境变量）
- `go run ./cmd/obtest -config <local-config> -report <local-report>`（双目标，含 change-user fixture 的复跑）
- `go vet ./...`、`golangci-lint run ./...`、§结果表列的交叉编译组合
- 本地配置与 JSON 报告只保存在临时目录，不提交；一次性身份/授权由验收方在目标环境管理，本仓库不保存连接信息或凭据。
