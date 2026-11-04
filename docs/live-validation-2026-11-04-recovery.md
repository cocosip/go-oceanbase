# 恢复后直连复验（2026-11-04 recovery）

## 1. 目标与范围

- 目标：修复后的 OceanBase 4.4.2.2 直连（MySQL 模式 `risdb`、Oracle 模式 `RISDB`），单端点，**未启用 TLS**，无 OBProxy/OCP/多节点。
- 凭据只经环境变量 `OB_MYSQL_DSN` / `OB_ORACLE_DSN` 传入；仓库、报告与日志不出现 DSN、口令或地址。
- 目的：补齐[待实库验证清单](pending-live-verification.md) §2 的两个场景、§3 的疑点与 §4 的全量基线，并复跑集成套件。

## 2. 全量验收工具

命令（配置 JSON 的 `targets` 只引用 `dsn_env`）：

```powershell
go run ./cmd/obtest -config <config.json> -report <new-path>.json
```

首次运行：**111 pass / 1 fail / 5 skip / 27 not applicable**。唯一失败为 `mysql-direct identifier-length`（场景假设错误，见 §3）。修正场景后全量复跑：

| 目标 | pass | fail | skip | not applicable |
| --- | --- | --- | --- | --- |
| mysql-direct | 54 | 0 | 3 | 15 |
| oracle-direct | 58 | 0 | 2 | 12 |
| 合计 | **112** | **0** | **5** | **27** |

与[待实库验证清单](pending-live-verification.md) §4 的预期完全一致。skip 全部为目标侧拒绝或缺失夹具：

- `mysql-direct xa-suspend` / `xa-join`：服务器拒绝 XA END SUSPEND / 重关联（1398）；
- `oracle-direct native-nclob`：服务器以 OBE-00902 拒绝 NCLOB（BLOB/CLOB 另行覆盖）；
- 两模式 `change-user`：缺第二身份 DSN（`change_user_dsn_env`），场景按设计跳过。

`identifier-length`、`topology-sources` 两个此前未实库验证的场景均通过。

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
- 修正后 `identifier-length` 两模式实库通过，全量复跑达成 §2 的 112/0/5/27。

## 4. 三个疑点结论

| 疑点 | 结论 |
| --- | --- |
| 1. MySQL 长表名是否被字典截断 | 否；是服务器大小写折叠，场景已修正，驱动无需改动。见 §3。 |
| 2. `ALL_COL_PRIVS` 权限列名 | 实际列名为 `PRIVILEGE`（`USER_COL_PRIVS`/`ALL_COL_PRIVS` 均可解析）。OceanBase 文档该页的 `PRIVILLEGE` 是笔误。`ALL_COL_PRIVS` 列为 `GRANTOR GRANTEE OWNER TABLE_NAME COLUMN_NAME PRIVILEGE GRANTABLE`，`ALL_TAB_PRIVS` 列为 `GRANTOR GRANTEE TABLE_SCHEMA TABLE_NAME PRIVILEGE GRANTABLE HIERARCHY`，与驱动投影一致。 |
| 3. 权限字典 0 行是否符合预期 | 仍未验证。当前 `RISDB` 无 `CREATE USER` 权限（1031），无法建立被显式授予表/列权限的第二账号；需要使用者提供第二账号或授权后补测。 |

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
- B21 代理重认证、`change-user` 第二身份、显式授权的表/列权限行（§4 疑点 3）；
- 依赖 verified-TLS 的 GSSAPI 路径与 Linux 侧 D30/D31/D32 子项。

这些条目均需对应部署或第二账号，不改变本次 112/0/5/27 的直连基线。

## 7. 复现

1. 设置 `OB_MYSQL_DSN`、`OB_ORACLE_DSN`（DSN 只进环境变量）；
2. `go run ./cmd/obtest -config <config.json> -check`；
3. `go run ./cmd/obtest -config <config.json> -report <new-path>.json`；
4. `go test -tags=integration ./... -count=1 -timeout 60m`（同一环境变量）。

配置文件中 `targets` 使用 `dsn_env`，不要写内联 DSN。
