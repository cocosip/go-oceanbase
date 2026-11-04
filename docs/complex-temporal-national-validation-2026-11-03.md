# Native temporal and national complex parameters

Target: the supplied Oracle-compatible OceanBase 4.4.2.2 tenant, plaintext direct connection. Connection settings remain in process environment variables only.

## Implemented and verified

Native complex TIMESTAMP members use the existing 12-byte Oracle wall-time representation with a length prefix. Collection element headers 202 (TIMESTAMP), 207 (NVARCHAR2) and 208 (NCHAR), including anonymous result descriptors, are implemented. Named TIMESTAMP collections retain their dictionary identity in parameter metadata so that direct INOUT calls preserve declared fractional precision.

Complex NCHAR/NVARCHAR2 **input** uses the connection character set, as the server's complex deserializer requires. Previously selecting UTF16 as `Session.NCharset` corrupted those inputs. Ordinary scalar `NString` and complex **output** retain their national character-set behavior. Client constructors still use `UNISTR` and can represent national characters absent from the connection character set.

`TestOracleComplexTemporalNational` ran 15 fixture configurations with **zero failures and zero skips**:

| Shape | Coverage and result |
| --- | --- |
| TIMESTAMP(9) collections, named and anonymous input | Five repeated prepared executions: non-NULL plus NULL element, empty, all-NULL elements, root NULL, then non-NULL again; independent stored value/count checks and native output checks pass |
| Named TIMESTAMP(9) INOUT | Server moves the input into the second element; all nine fractional digits survive, matching the independent SQL constructor control |
| Anonymous TIMESTAMP INOUT | Explicitly asserts the target's six-digit result, `.123457` for `.123456789`; this is a restricted result, **not** nanosecond acceptance |
| NVARCHAR2 and NCHAR collections | Named/anonymous × default settings, UTF8 connection with UTF16 national setting, and GBK connection with UTF16 national setting; IN, OUT and INOUT pass with Unicode, NULL and empty cases |
| Nested OBJECT TIMESTAMP(9) | Native insert and independent prepared binary read preserve string/time.Time wall fields, nanoseconds and NULL members |

Each collection configuration verifies connection reuse. Fixtures are dropped in reverse dependency order. The focused test does not change grants or server configuration.

## Precision boundary and remaining scope

The anonymous collection header carries an element type code but no accuracy. On this target, direct anonymous TIMESTAMP INOUT rounds to six fractional digits. In the same fixture the independent server constructor control and the named native collection both preserve nine. Applications requiring nanoseconds in this call shape must provide the named collection `Type`; no client truncation, CAST wrapper or replay is performed.

The server source reference is [ObMPStmtExecute at v4.4.1_CE](https://github.com/oceanbase/oceanbase/blob/v4.4.1_CE/src/observer/mysql/obmp_stmt_execute.cpp), particularly `parse_request_type` and `decode_type_info`; complex national payload decoding is in [ObPLDataType::deserialize](https://github.com/oceanbase/oceanbase/blob/v4.4.1_CE/src/pl/ob_pl_type.cpp). This reference explains the wire shape but is not the enterprise target's exact build source.

This closes the demonstrated temporal/national input gaps in U01. Anonymous LOB headers/locator input, native NCLOB, TZ/LTZ collection extensions, PL RECORD and ANYDATA remain separate. No TLS, proxy, alternate identity or additional platform acceptance is implied.
