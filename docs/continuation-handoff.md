# 换机续跑交接说明

更新日期：2026-11-04。本文件用于在另一台机器上继续本仓库的实库验收与缺口推进。仓库不保存任何 DSN、口令、地址或证书；换机后需重新提供连接信息（见下）。

## 1. 当前状态（2026-11-04）

- 本地可验证项已完成：全量单元测试通过；ordinary 与 integration-tag `golangci-lint` 均 0 issues；实库全量集成套件全部 `ok`；双目标 `obtest` 结果为 **79 pass / 0 fail / 3 skip / 14 not applicable**（MySQL 38/0/2/8，Oracle 41/0/1/6）。
- 剩余项均为环境或目标版本阻塞，已在 [缺失矩阵](go-driver-completeness.md) 与 [开发进度](go-driver-development-progress.md) 记录：
  - **U01–U03 / B16**：原生 PL RECORD / ANYDATA / LOB 协议形状在 4.4.2.2 目标返回 `600/0A000 (-4007)`，RECORD 五种常规元数据布局返回 `600/HY000`；需要支持相应 wire 的目标或官方成功 wire 依据，未确认是版本问题，归“依据不足 U + 指定目标拒绝 B16”。
  - **B19**：命名时区 `America/New_York` 返回 1882/HY000；显式 `-04:00` 等偏移可用。
  - **B22**：Oracle `ALL_CONSTRAINTS` 引用过滤 join 在目标上约 9.7s 字面量 / >10s 绑定，实库测试跳过该组合，保留绑定实现。
  - **ENV01–ENV05**：TLS/mTLS、required compression、OBProxy/OCP/多节点、Linux/macOS 原生传输、托管 CI；2026-11-03 已确认暂无法提供。
  - **V11**：原生 Linux/macOS 运行与托管 CI 未验收；Windows/386、linux/amd64、linux/arm64、darwin/arm64 交叉编译与 CI tag vet 已通过。
- 2026-11-03 的受限确认见缺失矩阵“环境确认”段与进度文档同条目。

## 2. 换机后需要的环境变量（只列名称）

仓库不保存值；在新机器上通过环境变量提供。`OB_TEST_PROFILE` 为空或 `all` 时遍历四个目标，未配置 DSN 的目标跳过；`OB_TEST_REQUIRE=1` 时缺少 fixture 直接失败而不是跳过。

| 变量 | 用途 |
| --- | --- |
| `OB_MYSQL_DSN` / `OB_ORACLE_DSN` | 直连 MySQL / Oracle 模式目标 |
| `OBPROXY_MYSQL_DSN` / `OBPROXY_ORACLE_DSN` | 经 OBProxy 的同一目标（有代理时） |
| `OB_MYSQL_CHANGE_USER_DSN` / `OB_ORACLE_CHANGE_USER_DSN` | 第二身份（ChangeUser 验收） |
| `OB_MYSQL_RESTRICTED_DSN` / `OB_ORACLE_RESTRICTED_DSN` | 最小权限身份（元数据可见性） |
| `OB_ORACLE_PROXY_DSN` / `OB_ORACLE_PROXY_AUTH_USER` / `OB_ORACLE_PROXY_SESSION_USER` | Oracle 代理登录（认证用户与目标用户） |
| `OB_TEST_PROFILE` | 目标子集：`mysql`、`oracle`、`proxy-mysql`、`proxy-oracle` |
| `OB_TEST_REQUIRE` | `1` 时缺少 fixture 视为失败 |
| `OB_TEST_STABILITY_ROUNDS` / `OB_TEST_STABILITY_TIMEOUT` | 耐力测试轮数（1–20000，默认 16）与超时（默认 3m，≤1h） |

DSN 形式：`oceanbase://user%40tenant:password@host:2881/db?mode=mysql|oracle`；口令中的 `#`/`!`/`@` 等需 URL 转义（如 `%23`/`%21`/`%40`）。

## 3. 换机后的验证命令

```powershell
# 单元测试与 lint
go test ./... -count=1
golangci-lint run ./...
golangci-lint run --build-tags integration ./...

# 实库全量集成（设置上表变量后）
go test -tags integration ./... -count=1 -timeout 30m

# 双目标验收工具（配置文件示例见 docs/go-driver-test-tool.md）
go run ./cmd/obtest -config obtest-dual.json -report obtest-report.json
# 期望：pass=79 fail=0 skip=3 not_applicable=14

# 平台矩阵（无需实库）
$env:GOOS='windows'; $env:GOARCH='386'; go test ./... -count=1
$env:GOOS='linux'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go build -tags integration ./...
$env:GOOS='darwin'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go vet -tags "gssapi,gssapitest,obproxy,obproxytest" ./...
```

## 4. 2026-11-03 创建的一次性 fixture（仅名称，不含口令）

这些账号只用于本次验收；换机重跑可复用，或按下方 SQL 清理。口令未记录在仓库，换机时需重新设置并通过上表变量提供。

| 目标 | fixture | 授权/用途 |
| --- | --- | --- |
| MySQL | 用户 `risdbt2`（租户内） | 对测试库 SELECT-only；ChangeUser 第二身份、restricted 元数据 |
| Oracle | 用户 `RISDBT2` | ChangeUser 第二身份 |
| Oracle | 用户 `RISDB_PROXY` + `ALTER USER <目标用户> GRANT CONNECT THROUGH RISDB_PROXY` | 代理登录（初始 ProxyUser） |
| Oracle | 用户 `RISDBRESTRICTED` | 仅 `CREATE SESSION`；restricted 元数据可见性 |

清理示例（需相应权限，确认无其他用途后执行）：

```sql
-- MySQL
DROP USER IF EXISTS 'risdbt2'@'%';
-- Oracle（SYS）
DROP USER RISDBT2 CASCADE;
DROP USER RISDBRESTRICTED CASCADE;
DROP USER RISDB_PROXY CASCADE;
```

注意：`COM_CHANGE_USER` 在本 4.4.2.2 目标上未应用代理目标（B21），代理重认证段落在实库测试中跳过。

## 5. 已知环境限制

- Windows `-race` 无法启动（`exit status 0xc0000139`），已有记录；Linux/macOS 运行与托管 CI 未验收。
- Go 1.27 的 closing mutex 在“事务连接被淘汰后、事务未 Rollback 时 Ping”可能死锁；实库事务中断测试先 Rollback 再 Ping（见进度文档 D09 条目）。
- Oracle 元数据引用过滤 join 在本目标超时（B22），实库测试跳过该组合，绑定实现保留。
- 服务器目标版本 4.4.2.2；U01–U03 所需更新目标暂不可提供（2026-11-03 确认）。
