# Live database validation record

This is the single authoritative record of live-database evidence for the Go driver. It merges the formerly separate dated reports (2026-10-19 through 2026-11-04). Connection settings are never stored here: DSNs, accounts and passwords are supplied only through process environment variables. Every result is finite, targeted evidence for the stated target/version; nothing here claims acceptance of unexercised deployments.

## Current baseline (2026-11-04 recovery)

Target: repaired OceanBase 4.4.2.2 direct endpoints (MySQL mode `risdb`, Oracle mode `RISDB`), single endpoint, **no TLS**, no OBProxy/OCP/multi-node. Tool: `cmd/obtest` with `dsn_env` targets plus `change_user_dsn_env` second identities.

| Target | pass | fail | skip | not applicable |
| --- | --- | --- | --- | --- |
| mysql-direct | 55 | 0 | 2 | 15 |
| oracle-direct | 59 | 0 | 1 | 12 |
| **Total** | **114** | **0** | **3** | **27** |

Skips are server rejections only: MySQL `xa-suspend`/`xa-join` (1398) and Oracle `native-nclob` (902). Both-mode `change-user` passes with the second identities. The full integration suite (`go test -tags=integration ./... -count=1`) is `ok`, and `TestChangeUserFailure`/`GuardSessions`/`ResourceInvalidation`/`Sessions` pass for both direct modes (proxy profiles skipped as unconfigured).

### Second identities and privilege probes

- Created `OBGO_SECOND` in the Oracle tenant through the supplied `SYS` account (`CREATE SESSION`) and `obgo_second` in the MySQL tenant through `root` (`SELECT` on the test schema). Direct logins must carry the tenant suffix (`user@tenant`); without it the server answers 1045 `using password: NO`.
- Table-level `GRANT SELECT` appears in the grantee's `USER_TAB_PRIVS` (`GRANTEE OWNER TABLE_NAME GRANTOR PRIVILEGE GRANTABLE HIERARCHY`). The probe table was dropped afterwards.
- Column-level grants are rejected by 4.4.2.2 Oracle mode (`GRANT SELECT (ID) ON ...` and `GRANT SELECT(ID) ON ...` → OBE-00969; `GRANT SELECT ON t(ID)` → OBE-00900), so `USER_COL_PRIVS` stays empty by server design.
- `ALL_TAB_PRIVS` on this target only shows the session's own grants; cross-user verification uses the grantee's `USER_*` views.

### identifier-length failure and repair

The first post-recovery `mysql-direct identifier-length` run failed with "long table name was truncated in the dictionary". Pure-SQL control returned the same 64-character name lower-cased (`lower_case_table_names=1` semantics), not truncated. The acceptance scenario assumed byte equality; it now requires equal length with MySQL case folding (Oracle stays exact) and has an offline `folded-table` fixture. The driver needed no change.

### Integration and durability

`go test -tags=integration ./... -count=1` is green. `OB_TEST_STABILITY_ROUNDS=500` `TestConcurrentPoolTransactions` passed both modes: 4 workers × 500 rounds = 2,000 transactions and 1,000 commits per mode, `pool_open=4`, `in_use=0`.

### Remaining environment boundaries

TLS/mTLS, required compression, OBProxy/multi-node/OCP, plugins/KDC, B21 proxy reauthentication and Linux client transports cannot be exercised on the supplied endpoints; see the [compatibility matrix §0](go-driver-compatibility-matrix.md#0-server-side-unavailable-verification-release-boundary) and [pending verification](pending-live-verification.md). They are deployment boundaries, not Go implementation gaps.

### Reproduction

```sh
go run ./cmd/obtest -config <config.json> -check
go run ./cmd/obtest -config <config.json> -report <unused-path>.json
go test -tags=integration ./... -count=1 -timeout 60m
```

The config lists targets by `dsn_env` (and optionally `change_user_dsn_env`); never inline DSNs.

## History at a glance

| Date | Target / tool | Result | Unique conclusion |
| --- | --- | --- | --- |
| 2026-10-19 | MySQL 4.4.2.2, `obtest` + integration | initial 30/6/1/7; retest 33/0/4/7 | First MySQL target; six failures reproduced and fixed |
| 2026-10-19 | Oracle 4.4.2.2, `obtest` + integration | 15/21/3/5 → 35/1/4/5 | Ten shared-driver defects fixed |
| 2026-10-20 | Both modes, release review | MySQL 35/0/2/8, Oracle 38/0/2/5 | Sensitive cursor refresh fixed; Connector/J 2.4.14 alignment |
| 2026-10-24 | Both modes, binary double | 5 configurations/mode, 67 inputs/mode | Type-257 corruption fixed; capability bit 4 requested/acknowledged |
| 2026-11-03 | First direct target (H03) | G subsets pass | B14 silent row loss avoided; SDO repair; extended types |
| 2026-11-03 | Second direct target | 79/0/3/14 (MySQL 38/0/2/8, Oracle 41/0/1/6) | B21 proxy reauthentication discovered; restricted metadata |
| 2026-11-03 | Native reset + probes | 76/0/5/13 acceptance | COM_RESET works, eviction kept; C02 1295 only for argument-less commands |
| 2026-11-03 | Charsets + Oracle NUL | focused suites | Seven more strict charsets; 1 MiB client-literal boundary |
| 2026-11-03 | Temporal/national complex | 15 configurations, 0 failures | Named collections keep 9 digits; anonymous header rounds to 6 |
| 2026-11-04 | Third direct target | 70 scenarios: 108/0/5/27 | 23 scenarios first live pass; B16/B17/B11/B01/S01 reproduced |
| 2026-11-04 | Recovery + second identity | **114/0/3/27** | ChangeUser passes both modes; table grants verified |

## 2026-10-19 MySQL baseline

OceanBase MySQL mode, `ob_version()=4.4.2.2`; Windows amd64, Go 1.27.2, TCP, utf8mb4, TLS disabled. The first partial run scored 30 pass / 6 fail / 1 skip / 7 not applicable. Reproduced failures and repairs:

- `metadata`: test consumers still scanned the old 13-column result; fixtures and consumers now use the 15-column contract (auto-increment/generated flags included).
- `session`: quoted numeric `wait_timeout` returned 1232; numeric system variables now use normalized integer literals, strings keep quoting.
- `local-import`: `LOAD DATA` implicitly committed, surviving `ROLLBACK`; import now refuses local/manual transactions and XA-used sessions before sending SQL or reading input, preserving earlier DML for rollback.
- `batch-rewrite`: initialization depended on a variable absent on the target (1193); the driver falls back only for that error and never counts fallback as native rewrite.
- `xa-suspend`/`xa-join`: the server rejects reassociation with 1398; only this exact flag rejection is classified as unavailable.
- Packet-capability rejections are skipped explicitly; strict mode still fails required missing capabilities.

Retest: 33 pass / 0 fail / 4 skip / 7 not applicable (skips: native rewrite, XA suspend/join, change-user without a second identity). The full MySQL integration package exited 0 with 12 explicitly skipped children (rewrite, RETURNING, discovery/failover, five packet modes, change-user, two XA variants). Two more defects were fixed afterwards: multi-statement import bypassed the transaction guard, and numeric-looking string assignment corrupted `ob_trace_info` values.

## 2026-10-19 Oracle baseline

Initial run: 15 pass / 21 fail / 3 skip / 5 not applicable. Ten shared-driver defects were fixed: combined-execution checksum, FLT metadata split across frames, real ROWID leading marker, LTZ interpolation precision, 4.4 locator header `0x00018401`, FREETEMPORARY guarding, `ALL_TAB_COLS` for table BLOB targeting, dictionary ordinal computation, numeric session variables in Oracle mode, XA recovery XID version byte, and managed-transaction prepare metadata bits.

Final standalone selection: 35 pass / 1 fail / 4 skip / 5 not applicable. The remaining failure was sensitive-cursor visibility; it was fixed on 2026-10-20. Oracle XA passed all six workflows (unlike MySQL's 1398 rejection); cancellation surfaces 600/70100/-5065; NCLOB (902), CURSOR(subquery) (902) and nested-table storage (600/-4007) are target restrictions.

## 2026-10-20 release validation

Release decision: no reviewed release-blocking code gap within the documented scope; the multi-target acceptance total remained 0/32 and no tag/package was published.

Changes: Oracle sensitive cursors now refresh by ROWID (opening snapshot semantics, positional ordering and hidden column preserved); LOCAL import transaction safety shared by both modes; MIT license; four-session concurrent transaction test; correct learned OBJECT/collection examples.

Live evidence: `obtest` MySQL 35/0/2/8 and Oracle 38/0/2/5; integration events MySQL 212/0/6 and Oracle 200/0/9; a four-session workload completed 24,000 transactions per mode with pool open=4/in-use=0; race and minimum-Go/no-cgo suites passed.

Official Connector/J 2.4.14 alignment reproduced MySQL XA SUSPEND/JOIN 1398, Oracle NCLOB 902, nested-table column 600/-4007 and storage clause 900; for `SELECT CURSOR(...)`, prepared returned 902 and JDBC text decoding threw while Go returned `ErrUnsupported` (not generalized to all CURSOR forms). Compression default/threshold gaps were corrected; no additional server version was required.

## 2026-10-24 binary double

Type 257 (`01 01`) was interpreted as TINYINT, truncating the float payload; Oracle reproduced MaxFloat64 → `-1` (ordinary) and 603 (combined). The fix restores type 5, zero flags and eight IEEE-754 bytes, and logs in with capability bit 4; the server acknowledged 7 (locator-v2 1 + metadata 2 + binary-double 4). Five configurations per mode × 67 inputs passed, including ±MaxFloat64, subnormals, float32 and NULL; independent `TO_CHAR` readback parsed back to the exact float64 bits. Oracle normalizes negative zero (also reproduced with literal SQL). Older servers without capability 4 remain unverified.

## 2026-11-03 first direct target (H03)

Targeted MySQL and Oracle integration tests passed on a supplied 4.4.2.2 target. G01/G03/G04/G05a/G07a/G07b subsets were re-exercised; B14 silent row loss was avoided by disabling the optional general combined optimization for exactly 4.4.2.2 while keeping required OUT/RETURNING; the SDO repair and extended types passed; PL prerequisites and B-class rejections (RECORD/ANYDATA, anonymous LOB headers, NCLOB, MySQL XA) were re-confirmed. No new client gap was recorded.

## 2026-11-03 second direct target

Independent 4.4.2.2 direct target (Windows amd64, Go 1.27.2; no TLS/OBProxy). Acceptance: **79 pass / 0 fail / 3 skip / 14 not applicable** (MySQL 38/0/2/8, Oracle 41/0/1/6); full integration suite `ok`; G subsets re-verified.

- ChangeUser passed in both modes with disposable second identities; `TestChangeUserSessions`/`TestChangeUserFailure` green.
- Oracle initial ProxyUser login applied the target (`CURRENT_USER`=target, `PROXY_USER`=proxy), but `COM_CHANGE_USER` succeeded while reverting `CURRENT_USER` to the proxy account and never applied the target. The driver does resend `__ob_client_proxy_user_name`, so this is the target's behavior (**B21**); the driver rejects the proxy target on exactly this version before sending.
- Restricted identities passed metadata visibility (one table / three columns / one primary key; invisible schema returns zero rows). Metadata filters passed; Oracle referenced-column filtering took about 9.7 s and exceeded the default 10 s timeout (**B22**), so that combination is skipped.
- Stability: `TestConcurrentPoolTransactions` with 2,000 rounds per mode (8,000 transactions, 4,000 commits) passed with `pool_open=4`, `in_use=0`. Prepare fallback: 1295 for `CREATE/DROP PROCEDURE`; with `EmulateUnsupportedPstmts` the explicit statement falls back to client interpolation. Receipt-loss handling passed for plain COMMIT, MySQL XA and Oracle combined-PL XA.

## 2026-11-03 native reset and probes

`COM_RESET_CONNECTION` (0x1F) succeeds in both modes and resets session settings (including the driver's `time_zone` baseline), but `ResetFull` keeps evicting the physical connection: `database/sql` reuses statement ids after an in-place reset, and the server then answers 1243/HY000 (MySQL) or 600/HY000 (Oracle). This is a standard-library statement-ownership boundary, not a server capability gap.

C02: 44 enumerated MySQL statements plus 26 procedure/event/replication statements were server-prepared; only `CREATE/DROP PROCEDURE` returned 1295, and every argument-bearing variant returned 1064, so automatic prepare fallback is unreachable on this target; explicit `sql.Prepare` gained the same fallback. Oracle warnings: `SHOW WARNINGS`/`SHOW COUNT(*) WARNINGS` return 900, `DBA_ERRORS` returns 942, so only the count exists (C11 stays client-scope). Acceptance this round: 76 pass / 0 fail / 5 skip / 13 not applicable.

## 2026-11-03 additional charsets and Oracle NUL

Implemented CP850, HP8, MACROMAN, SWE7, UJIS, CP932 and EUCJPMS (handshake ids, 14 result collation ids, strict OceanBase mappings verified by decoder-table and reversible encoder hashes). `TestMySQLCharsets` passed all 18 connection charsets; `TestMySQLAdditionalCharsetColumns` passed 14/14 column cases including frozen HEX bytes and UTF-8 observation. SWE7 cannot represent `@`, so the tenant-qualified identity cannot log in with SWE7; the driver does not downgrade identity encoding. Native bytes conversion applies the parameter's UTF8 collation on this target, so binary conversion must be explicit.

Long Oracle client-prepared NUL literals now use a collision-free `REPLACE('a01b','01',CHR(0))` marker. 12,003/98,304/196,608-byte values passed byte-for-byte; the target rejects the NUL-bearing literal above exactly 1 MiB (600/22001, -5098), which is a VARCHAR2 literal limit rather than SQL size; server preparation works beyond it. The driver keeps its rendering and refuses to silently switch strategy, truncate or retry.

## 2026-11-03 temporal and national complex parameters

Native complex TIMESTAMP members use the 12-byte wall-time layout; anonymous result descriptors for types 202/207/208 were implemented, and named TIMESTAMP collections keep dictionary identity so INOUT preserves precision. Complex NCHAR/NVARCHAR2 input must use the connection charset; scalar `NString` and complex output keep national-charset behavior. `TestOracleComplexTemporalNational` ran 15 configurations with zero failures/skips (TIMESTAMP(9) collections, named/anonymous INOUT, NVARCHAR2/NCHAR collections, nested OBJECT wall fields). Anonymous TIMESTAMP INOUT rounds to six fractional digits on this target while named collections preserve nine; callers needing nanoseconds must supply the named `Type`.

## 2026-11-04 third direct target

Independent 4.4.2.2 target (Go 1.27.3, Windows/amd64; no second identity, TLS or OBProxy). The run with the then-registered 71 scenarios (one later-withdrawn Java-only scenario) reached 110 pass / 0 fail / 5 skip / 27 not applicable; after withdrawal the registered baseline is 72 scenarios, recorded as 108/0/5/27 for the 70 scenarios executed before recovery. Skips: MySQL XA suspend/join (B01), Oracle NCLOB (S01), change-user without a second identity.

Twenty-three scenarios passed live for the first time: `extended-types`, `geometry`, `oracle-spatial`, `oracle-urowid`, `oracle-xml`, `oracle-number-scale`, `mysql-charset`, `oracle-binary-float`, `mysql-utf16le`, `oracle-synonym`, `mysql-enum-set`, `mysql-generated-columns`, `char-padding`, `empty-lob`, `metadata-patterns`, `oracle-identity`, `metadata-indexes`, `metadata-routines`, `statement-parameters`, `metadata-type-info`, `views`, `floats` (both modes), plus offline `identifier-length` then live-verified after recovery. The full integration suite passed; B16/B17/B11/B01/S01 were reproduced. After this run the target started returning `wire: read after 0 bytes: EOF` on every statement while TCP 2881 still accepted connections; the server was later repaired and the results above were superseded by the current baseline.

## Historical detail policy

Per-target scripts, raw reports (`obtest-run-*.json`), probe programs and local logs were kept out of the repository; this record keeps their conclusions, counts and reproduction boundaries. Earlier deleted reports are superseded by the sections above and by the [pending verification list](pending-live-verification.md). For current capability status see the [capability matrix](go-driver-capabilities.md), [missing-feature matrix](go-driver-completeness.md) and [compatibility matrix](go-driver-compatibility-matrix.md).
