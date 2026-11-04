# 恢复后直连复验（2026-11-04 recovery）

## 1. 目标与范围

- 目标：修复后的 OceanBase 4.4.2.2 直连（MySQL 模式 `risdb`、Oracle 模式 `RISDB`），单端点，**未启用 TLS**，无 OBProxy/OCP/多节点。
- 凭据只经环境变量 `OB_MYSQL_DSN` / `OB_ORACLE_DSN` / `OB_MYSQL_CHANGE_DSN` / `OB_ORACLE_CHANGE_DSN` 传入；仓库、报告与日志不出现 DSN、口令或地址。
- 第二身份：经使用者提供的 Oracle `SYS` 授权后创建 `OBGO_SECOND`（Oracle）与由 MySQL root 创建 `obgo_second`（MySQL），仅用于 `change-user` 验收；口令只进环境变量。
- 目的：补齐[待实库验证清单](pending-live-verification.md) §2 的两个场景、§3 的疑点与 §4 的全量基线，并复跑集成套件。

## 2. 全量验收工具

命令（配置 JSON 的 `targets` 只引用 `dsn_env`）：

```powershell
go run ./cmd/obtest -config <config.json> -report <new-path>.json
```

首次运行：**111 pass / 1 fail / 5 skip / 27 not applicable**。唯一失败为 `mysql-direct identifier-length`（场景假设错误，见 §3）。修正场景后全量复跑 112/0/5/27；第二身份就绪后（见 §2.1）最终复跑：

| 目标 | pass | fail | skip | not applicable |
| --- | --- | --- | --- | --- |
| mysql-direct | 55 | 0 | 2 | 15 |
| oracle-direct | 59 | 0 | 1 | 12 |
| 合计 | **114** | **0** | **3** | **27** |

与[待实库验证清单](pending-live-verification.md) §4 的预期完全一致。skip 全部为目标侧拒绝：

- `mysql-direct xa-suspend` / `xa-join`：服务器拒绝 XA END SUSPEND / 重关联（1398）；
- `oracle-direct native-nclob`：服务器以 OBE-00902 拒绝 NCLOB（BLOB/CLOB 另行覆盖）。

`identifier-length`、`topology-sources` 两个此前未实库验证的场景均通过；两模式 `change-user` 在第二身份就绪后由 skip 转为 pass。

## 2.1 第二身份与权限探测（2026-11-04 后续）

- 身份创建：使用 Oracle `SYS` 账号执行 `CREATE USER OBGO_SECOND ...` + `GRANT CREATE SESSION`；MySQL 模式由 `root` 执行 `CREATE USER 'obgo_second'@'%'` + `GRANT SELECT ON risdb.*`。直接连接时必须带租户后缀（`user@tenant`），否则服务端返回 1045 `using password: NO`。
- `change-user`：配置 `change_user_dsn_env` 后两模式各 **1 pass**；验证了身份切换、旧 prepared 语句 `ErrResource`、切换后查询与原池身份隔离/恢复。
- 表级显式授权：`GRANT SELECT ON RISDB.OBGO_PRIV_T TO OBGO_SECOND` 成功；受让账号 `USER_TAB_PRIVS` 返回 `OBGO_SECOND RISDB OBGO_PRIV_T RISDB SELECT NO`（列 `GRANTEE OWNER TABLE_NAME GRANTOR PRIVILEGE GRANTABLE HIERARCHY`）。验证后已 `DROP TABLE RISDB.OBGO_PRIV_T`，授权随之消失。
- 列级授权：`GRANT SELECT (ID) ON ...` / `GRANT SELECT(ID) ON ...` → OBE-00969；`GRANT SELECT ON RISDB.OBGO_PRIV_T(ID)` → OBE-00900。4.4.2.2 Oracle 模式不支持列级授权，`USER_COL_PRIVS` 因此恒为空，属于服务端限制。
- 字典可见性：在同一目标上，SYS 执行 `ALL_TAB_PRIVS` 只返回其自身的授权行（本目标 8 行系统授权），看不到授予 `OBGO_SECOND` 的行；受让账号的 `USER_TAB_PRIVS` 可见。跨用户权限验证应以受让账号的 `USER_*` 视图为准。

## 3. identifier-length 失败与修复

现象：`mysql-direct identifier-length` 报 `long table name was truncated in the dictionary`；`oracle-direct` 通过。

纯 SQL 对照（MySQL 模式，64 字符表名）：

```sql
CREATE TABLE <64 字符大写名> (id INT);
SELECT TABLE_NAME, LENGTH(TABLE_NAME) FROM information_schema.TABLES
 WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=<同一名称>;
```

结果：字典返回**同样 64 字符**但**已折叠为小写**的名称。即 MySQL 模式下服务器按 `lower_case_table_names=1` 语义折叠表标识符，**不是截断**。

结论与处理：

- 驱动 `MetadataTables` 返回服务器给出的名称，行为正确，不需要修改；
- 验收场景原来的"逐字符相等"假设错误。已改为：长度必须一致，且 MySQL 模式允许大小写折叠匹配（Oracle 保持精确匹配）；并新增离线夹具 `folded-table` 固定该行为；
- 修正后 `identifier-length` 两模式实库通过，全量复跑先达成 112/0/5/27，第二身份就绪后 §2 的 114/0/3/27。

## 4. 三个疑点结论

| 疑点 | 结论 |
| --- | --- |
| 1. MySQL 长表名是否被字典截断 | 否；是服务器大小写折叠，场景已修正，驱动无需改动。见 §3。 |
| 2. `ALL_COL_PRIVS` 权限列名 | 实际列名为 `PRIVILEGE`（`USER_COL_PRIVS`/`ALL_COL_PRIVS` 均可解析）。OceanBase 文档该页的 `PRIVILLEGE` 是笔误。`ALL_COL_PRIVS` 列为 `GRANTOR GRANTEE OWNER TABLE_NAME COLUMN_NAME PRIVILEGE GRANTABLE`，`ALL_TAB_PRIVS` 列为 `GRANTOR GRANTEE TABLE_SCHEMA TABLE_NAME PRIVILEGE GRANTABLE HIERARCHY`，与驱动投影一致。 |
| 3. 权限字典 0 行是否符合预期 | 表级已验：`GRANT SELECT ... TO OBGO_SECOND` 后受让账号 `USER_TAB_PRIVS` 返回非空且 grantor/grantee/grantable 正确。列级：4.4.2.2 Oracle 模式拒绝列级授权语法（OBE-00969/OBE-00900），`USER_COL_PRIVS` 恒为空，属服务端限制而非驱动缺口。跨用户查询 `ALL_TAB_PRIVS` 在本目标只显示自身授权行，应以受让账号 `USER_*` 视图验证。见 §2.1。 |

## 5. 集成套件

命令（两模式 DSN 由环境变量提供，未配置 OBProxy/OCP/TLS）：

```powershell
go test -tags=integration ./... -count=1 -timeout 60m
```

结果：**全部包 `ok`，exit 0**，无失败。未配置的代理/OCP/TLS/平台组合按既有规则在用例内部跳过，不计为通过；其验收边界仍见[兼容性矩阵](go-driver-compatibility-matrix.md)。

耐久补充：`OB_TEST_STABILITY_ROUNDS=500` 运行 `TestConcurrentPoolTransactions`（两模式）：4 个 worker × 500 轮 = 每个模式 2000 事务、1000 提交；`pool_open=4`、`in_use=0`、goroutines=5，两模式均 PASS（proxy 配置缺失按规则跳过）。

## 6. 仍未覆盖

- ENV01 / V01 TLS/mTLS：目标未开 TLS，缺真实证书/主机名/轮换端点；
- ENV02 压缩协商、ENV03 ODP/多节点/OCP、ENV04 插件/KDC、ENV05/D28 客户端 Linux 平台传输；
- B21 代理重认证、显式授权的权限行（表级已验，列级为服务端不支持，见 §2.1）；
- 依赖 verified-TLS 的 GSSAPI 路径与 Linux 侧 D30/D31/D32 子项。

这些条目均需对应部署，不改变本次 114/0/3/27 的直连基线。

## 7. 复现

1. 设置 `OB_MYSQL_DSN`、`OB_ORACLE_DSN`（DSN 只进环境变量）；
2. 需要 `change-user` 时再设置 `OB_MYSQL_CHANGE_DSN`、`OB_ORACLE_CHANGE_DSN`，并在配置的 `targets[]` 中写 `change_user_dsn_env`；
3. `go run ./cmd/obtest -config <config.json> -check`；
4. `go run ./cmd/obtest -config <config.json> -report <new-path>.json`；
5. `go test -tags=integration ./... -count=1 -timeout 60m`（同一环境变量）。

配置文件中 `targets` 使用 `dsn_env`，不要写内联 DSN。
