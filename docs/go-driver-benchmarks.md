# Go 驱动本地性能基准

## 1. 范围与读数

`BenchmarkDriver` 在单个物理连接上，通过进程内 `net.Pipe` 协议对端运行两种 OceanBase 兼容模式，共16个场景。入口见 [driver_benchmark_test.go](../driver_benchmark_test.go)。没有连接数据库，不执行真实 SQL，也不包含网络往返或服务端执行、存储和锁开销。

计时和分配统计包含 database/sql、驱动、对端响应构造、协议收发、调度及结果断言。因此 B/op 和 allocs/op **不是纯驱动分配量**，ns/op 也不是数据库延迟。当前夹具采用标准包流和单连接；不覆盖 TLS、压缩、OB20、多连接并发或真实代理性能。短样本仅用于后续在相同夹具下复测，不设置性能通过阈值，不据此判断两工具链性能高低。

## 2. 场景与计时边界

每个场景先完成握手、初始化、Ping 和一轮预热；这些以及最终清理不计时。随后执行 N 轮，检查协议命令、参数、结果及资源关闭。第二次物理拨号立即失败，避免自动换线掩盖夹具错误。

| 场景 | 一次 op 的操作与断言 | 计时说明 |
| --- | --- | --- |
| crud | INSERT、UPDATE、SELECT、DELETE 各一次；修改各影响1行，查询值42 | 四次完整调用及断言 |
| prepare-per-call | 参数化 INSERT；检查参数和影响1行 | 每轮 prepare/execute/close |
| prepare-reuse | 显式 sql.Stmt 的 INSERT；检查影响1行 | 仅 execute；prepare/最终 close 在计时外 |
| cache-hit | 隐式预处理缓存命中，StatementCacheSize=1、ReuseParameterTypes=true | 预热后复用语句/参数类型，检查省略类型头 |
| batch-sequential | 每轮8项 INSERT，核对策略、逐项状态和影响行数 | 每项 prepare/execute/close，含结果分配与断言 |
| batch-multisend | 每轮8项 INSERT，核对真实 MultiSend 策略及逐项结果 | 每批共享 prepare；含分块收发和 close |
| lob | 打开、读取并关闭16KiB BLOB，核对所有字节 | MySQL 表内 BLOB 查询字典/长度/分块；Oracle inline locator 物化读取 |
| fetch | 同一游标每轮返回32个 int64，核对0–31及关闭 Rows | 游标 prepare/execute/最终 close 在计时外；Oracle 含 offset 扩展、元数据和尾 OK |

LOB 两模式后端不同，不能把其差值解释为模式性能优劣；当前也不覆盖远端 locator 流式读取/写入。fetch 对端每次重发同一批数据，只测解码/扫描，不能证明跨批游标位置或数据一致性。batch 的 rows/op=8、fetch 的 rows/op=32 是固定工作量，不是独立测量的吞吐。LOB 的 MB/s 以16KiB有效负载计算；fetch 以32×8字节有效数值计算，均不代表实际协议带宽。

## 3. 重现与正确性检查

```sh
GOTOOLCHAIN=go1.25.0 go test . -run '^TestDriverBenchmarkFixtures$' -count=30
GOTOOLCHAIN=go1.25.0 go test . -run '^$' -bench '^BenchmarkDriver$' -benchmem -benchtime=100x -count=3
GOTOOLCHAIN=go1.26.0 go test . -run '^TestDriverBenchmarkFixtures$' -count=30
GOTOOLCHAIN=go1.26.0 go test . -run '^$' -bench '^BenchmarkDriver$' -benchmem -benchtime=100x -count=3
```

以上为 POSIX shell 写法；PowerShell 先设置 `$env:GOTOOLCHAIN='go1.25.0'` 或 `'go1.26.0'`，再执行对应 go 命令。无需 DSN、凭据或数据库。普通 `go test ./...` 也会运行相同夹具的预热及每项两轮操作，CI 默认检查返回值和清理；不会默认采集性能结果。

每个夹具从建连起有2分钟总预算；对端保留到 DB.Close 完成，再关闭传输并等待 goroutine 退出，最后取消 context。testing.TB.Context 在 Cleanup 前取消，因此夹具使用 WithoutCancel 后自行施加预算。长时间 benchtime 必须与该预算匹配；资源稳定性测试需独立设计，不能直接延长这个单连接短基准并宣称完成验收。

## 4. 本地样本

采集日期：2026-10-17。Windows/amd64，Intel Core i5-9400 CPU @ 2.90GHz，默认 GOMAXPROCS=6；Go1.25.0 和 Go1.26.0。每项100次操作、同进程采样3次；下表各指标分别取3个样本的中位数，未作显著性分析。操作和断言定义以本文件及对应提交中的夹具为准。

| 模式/场景 | Go | ns/op | B/op | allocs/op |
| --- | --- | --- | --- | --- |
| mysql-compatible/crud | 1.25.0 | 136,698 | 20,794 | 377 |
| mysql-compatible/prepare-per-call | 1.25.0 | 85,686 | 11,815 | 218 |
| mysql-compatible/prepare-reuse | 1.25.0 | 27,962 | 4,231 | 76 |
| mysql-compatible/cache-hit | 1.25.0 | 25,425 | 4,959 | 85 |
| mysql-compatible/batch-sequential | 1.25.0 | 683,746 | 93,005 | 1,733 |
| mysql-compatible/batch-multisend | 1.25.0 | 206,191 | 31,889 | 634 |
| mysql-compatible/lob | 1.25.0 | 326,405 | 108,900 | 841 |
| mysql-compatible/fetch | 1.25.0 | 278,536 | 42,609 | 842 |
| oracle-compatible/crud | 1.25.0 | 129,897 | 20,748 | 376 |
| oracle-compatible/prepare-per-call | 1.25.0 | 85,814 | 12,688 | 229 |
| oracle-compatible/prepare-reuse | 1.25.0 | 25,351 | 4,231 | 76 |
| oracle-compatible/cache-hit | 1.25.0 | 29,112 | 5,431 | 92 |
| oracle-compatible/batch-sequential | 1.25.0 | 695,274 | 100,046 | 1,829 |
| oracle-compatible/batch-multisend | 1.25.0 | 210,359 | 36,520 | 701 |
| oracle-compatible/lob | 1.25.0 | 95,775 | 99,273 | 191 |
| oracle-compatible/fetch | 1.25.0 | 320,905 | 47,359 | 939 |
| mysql-compatible/crud | 1.26.0 | 140,431 | 20,875 | 377 |
| mysql-compatible/prepare-per-call | 1.26.0 | 81,428 | 11,816 | 218 |
| mysql-compatible/prepare-reuse | 1.26.0 | 19,949 | 4,231 | 76 |
| mysql-compatible/cache-hit | 1.26.0 | 26,670 | 4,968 | 86 |
| mysql-compatible/batch-sequential | 1.26.0 | 696,520 | 93,030 | 1,733 |
| mysql-compatible/batch-multisend | 1.26.0 | 208,289 | 31,845 | 634 |
| mysql-compatible/lob | 1.26.0 | 352,196 | 108,741 | 838 |
| mysql-compatible/fetch | 1.26.0 | 278,709 | 42,582 | 842 |
| oracle-compatible/crud | 1.26.0 | 133,644 | 20,769 | 376 |
| oracle-compatible/prepare-per-call | 1.26.0 | 91,360 | 12,690 | 229 |
| oracle-compatible/prepare-reuse | 1.26.0 | 27,104 | 4,231 | 76 |
| oracle-compatible/cache-hit | 1.26.0 | 29,375 | 5,430 | 92 |
| oracle-compatible/batch-sequential | 1.26.0 | 713,457 | 100,065 | 1,829 |
| oracle-compatible/batch-multisend | 1.26.0 | 216,202 | 36,561 | 701 |
| oracle-compatible/lob | 1.26.0 | 102,149 | 99,270 | 190 |
| oracle-compatible/fetch | 1.26.0 | 319,728 | 47,362 | 939 |

## 5. 尚待验证

真实两模式/直连与 OBProxy 的 CRUD、缓存、原生批量优化、LOB 流式和游标性能、网络与多连接负载、长期内存/句柄/goroutine 曲线仍未执行。Linux 实际运行、远程 CI 和成功 race 证据仍缺。D32 不因此完成；实现28/32、验收0/32不变，详见[开发进度](go-driver-development-progress.md)。
