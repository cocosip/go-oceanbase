# Binary-double live regression — 2026-10-24

The direct Oracle and MySQL tenants both report OceanBase **4.4.2.2**. This
verification used Windows amd64 / Go 1.27.2 and the corrected working tree after
`695a8f2`. That commit alone is defective and must not be treated as a fix.
Connection strings and credentials are omitted.

## Root cause and correction

The ordinary execute parameter declaration is one byte of type followed by one
byte of flags. Writing 257 sends `01 01`, so the server interprets the parameter
as TINYINT and consumes only the first byte of its floating-point payload.
The pre-correction live run independently reproduced MaxFloat64 becoming `-1`
in ordinary and explicit prepared execution, and server error 603 / HY000 in
combined execution.

The correction restores type **5**, zero flags and eight IEEE-754 bytes. Login
requests `OB_CLIENT_SUPPORT_JDBC_BINARY_DOUBLE` (mask **4**) in
`__ob_client_attribute_capability_flag`, retaining locator-v2 (1) and optional
new metadata (2). A direct Oracle authentication probe requested **7** and read
**7** in the server's `__ob_client_capability_flag` acknowledgment.
The server then binds DOUBLE directly rather than first converting it to NUMBER.
The unnecessary array remapping from 257 to 5 is removed.

## Live results

`TestBinaryDoubleRange` checks each inserted value and its following string
parameter. It reads floats through a parameterized binary query and independently
reads the four originally corrupted values with unparameterized `TO_CHAR(V)` in
Oracle mode.

| Execution path | Oracle | MySQL |
| --- | --- | --- |
| ExecContext, ordinary server execute | Pass, 14 inputs | Pass, 14 inputs |
| PrepareContext / Stmt.ExecContext | Pass, 14 inputs | Pass, 14 inputs |
| Combined prepare/execute enabled | Pass, 14 inputs, combined command | Pass, 14 inputs, ordinary prepared fallback |
| Explicit preparation with combined execution enabled | Pass, 14 inputs, combined command | Pass, 14 inputs, ordinary prepared fallback |
| ExecBatch array strategy inside a transaction | Pass, 11 inputs, native array binding asserted | Pass, 11 inputs, sequential fallback asserted |

The 67 inputs per mode include `1.5`, `1.1`, positive/negative MaxFloat64,
`1e200`, positive/negative smallest subnormal, negative zero, float32 and NULL.
Scalar paths additionally cover pointers and valid/invalid `sql.NullFloat64`,
including type reuse across NULL transitions. Wrapper values intentionally cause
batch fallback, so the native-array fixture uses scalar values and nil.

These are five test configurations per mode. The combined command is enabled
only in Oracle mode; the two MySQL configurations with that option enabled
exercise ordinary server-prepared execution, not a distinct combined protocol.

Oracle's independent stored-text results are:

| Input | Server TO_CHAR(V) |
| --- | --- |
| 1.5 | 1.5E+000 |
| 1.1 | 1.1000000000000001E+000 |
| math.MaxFloat64 | 1.7976931348623157E+308 |
| 1e200 | 9.9999999999999997E+199 |

Each text result parses back to the exact input float64 bits. The mixed
integer/float/string INSERTs no longer corrupt following parameters or produce
malformed-packet errors in these tests.

The following existing live regressions also passed separately in both modes:
`TestAllScalarTypes` (text/server/client paths), `TestPrepareRoundTrip`,
`TestPrepareEquivalence`, `TestBatchArrayBindingExecution`, `TestBatchExecution`
and `TestSmoke`.

## Negative zero and compatibility limits

Oracle `SELECT DUMP(?) FROM dual` reports negative zero as
`Typ=12 Len=8: 0,0,0,0,0,0,0,128`, proving the bound sign bit reaches the server.
After insertion, `DUMP(V)` reports positive zero. Inserting
`TO_BINARY_DOUBLE('-0')` directly, without a bound float, produces the same stored
positive zero. The test checks that bound storage matches this literal control;
it does not claim Oracle preserves the stored negative-zero sign bit. MySQL
preserves negative zero in this regression.

Older servers that do not acknowledge mask 4 retain the ordinary type-5 legacy
conversion path and can still reject values outside NUMBER range. No older
server or OBProxy was available in this run. Local protocol tests cover absent
acknowledgment and acknowledgments with/without metadata. NaN/Inf encoding policy
is covered locally, not by this live selection. This is focused driver regression
evidence, not a rerun of the consumer project's complete GORM acceptance matrix
or proof that its separate NCLOB/collection-column restrictions are resolved.

## Reproduction and cleanup

Set `OB_ORACLE_DSN` and `OB_MYSQL_DSN` privately, then run once per profile:

```powershell
$env:OB_TEST_REQUIRE = '1'
$env:OB_TEST_PROFILE = 'oracle' # repeat with mysql
go test -tags=integration ./internal/valuecodec ./internal/statement ./internal/batch/integration ./internal/integrationtest -run '^(TestBinaryDoubleRange|TestAllScalarTypes|TestPrepareRoundTrip|TestPrepareEquivalence|TestBatchArrayBindingExecution|TestBatchExecution|TestSmoke)$' -count=1 -v -timeout 3m
```

The float fixture logs its unique table name and drops it through a fresh
connection, even when the tested connection fails. The two leftover tables from
the pre-correction combined-execution failures were identified by run timestamp
and ID/NUMBER + V/BINARY_DOUBLE structure and removed explicitly. No application
tables, grants or tenant settings were changed.

Local raw evidence is retained in ignored `.cache/float64-live-oracle-20261024.log`
(pre-correction), `.cache/float64-corrected-oracle-20261024.log`,
`.cache/float64-corrected-mysql-20261024.log` and
`.cache/float-diagnosis-20261024.log`. After the test-helper split, the focused
five-configuration tests passed again in both modes; final logs are
`.cache/float64-final-oracle-20261024.log` and
`.cache/float64-final-mysql-20261024.log`. The final catalog inventory in
`.cache/float-cleanup-20261024.log` reports zero OBGD test tables in both schemas.

## Local checks

`go build ./...`, full `go test ./... -count=1 -timeout 5m`, and ordinary plus
integration-tagged `golangci-lint run` passed; both lint selections report
**0 issues**. Go module-cache and lint-cache persistence emitted Windows access
denied warnings, with successful command exit codes. An independent read-only
review found no additional runtime correctness issues.

After correcting the MySQL coverage description, the complete focused live
selection above, build, full unit suite and both lint selections passed again
before submission. Both Oracle and MySQL login probes requested/acknowledged
**7**; final inventories again found zero OBGD test tables. These logs use the
ignored `.cache/float-presubmit-` prefix (`oracle.log`, `mysql.log`, `login.log`,
`cleanup.log`, `build.log`, `unit.log`, `lint.log`, `integration-lint.log`).
