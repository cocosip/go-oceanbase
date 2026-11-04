# Additional charset and Oracle NUL validation, 2026-11-03

This report records focused Go checks against the available direct MySQL and Oracle OceanBase 4.4.2.2 targets, without TLS. Credentials and endpoint configuration are supplied only through process environment variables. This is finite, targeted evidence; proxy, TLS and other versions remain separate validation work.

## U06: seven additional strict charsets

The driver now implements CP850, HP8, MACROMAN, SWE7, UJIS, CP932 and EUCJPMS, including handshake IDs, fourteen general/binary result collation IDs and existing configuration acceptance. No dependency or public option was added.

Mappings use the [OceanBase v4.4.1_CE charset tables](https://github.com/oceanbase/oceanbase/tree/v4.4.1_CE/deps/oblib/src/lib/charset). Existing x/text codecs are reused where mappings match; numeric differences and contiguous private-use ranges preserve OceanBase mappings. Independent decoder-table hashes and reversible BMP encoder hashes cover all seven mappings. Invalid/truncated sequences, undefined slots and lossy Unicode aliases reject. Sample tests failed before implementation, then passed.

| Check | Result |
| --- | --- |
| `TestAdditionalCharsetMappings`, `TestAdditionalCharsetStrictness`, `TestAdditionalCharsetServerTables` | Seven charsets pass exact mappings, full reference hashes and rejection checks |
| Config and authentication peer tests | New names/IDs accepted; representable identities pass; SWE7 identities containing ASCII `@` safely reject |
| `TestMySQLCharsets` | All 18 connection charsets pass; each exercises server/client preparation and ordinary/NO_BACKSLASH_ESCAPES SQL modes. Six are newly added; SWE7 connection login is excluded for the supplied identity |
| `TestMySQLAdditionalCharsetColumns` | 14/14 charset/preparation cases pass: seven charsets, server/client, non-ASCII storage, frozen HEX bytes, SQL NULL, text/parameter queries, raw bytes with explicit binary conversion, result decoding and independent UTF8 observation |

Japanese samples include kana, JIS0212, vendor extensions and private-use characters. SWE7 column/result samples include all six Swedish replacement letters. These checks do not claim every possible server conversion or Oracle charset initialization works.

### SWE7 authentication boundary

SWE7 cannot represent ASCII `@`, backslash and several other punctuation characters. The supplied tenant-qualified identity therefore cannot log in with SWE7 as the connection charset. The driver retains strict identity encoding and does not silently authenticate under a different charset. Representable SWE7 identities have local peer coverage; live SWE7 column/result tests use a UTF8 connection. A live SWE7 authentication success is not claimed.

### Native bytes conversion boundary

On a UTF8 connection, a native bytes parameter containing `82` produced:

| Expression | Result |
| --- | --- |
| `HEX(?)` | `82` |
| `CHARSET(?)` | `utf8mb4` |
| `HEX(CONVERT(? USING cp850))` | `3F` |
| `HEX(CONVERT(UNHEX(?) USING cp850))`, with hex string `82` | `82` |
| `HEX(CONVERT(CAST(? AS BINARY) USING cp850))` | `82` |

The driver sent the exact bytes. The direct CONVERT expression applies the parameter's UTF8 collation on this target. Tests make binary conversion explicit; no driver SQL rewrite or broad claim about bytes conversion is introduced.

## U08: long Oracle client-prepared NUL values

The previous `('a'||CHR(0)||'b')` rendering passed 12003 bytes, but failed at 98304 and 196608 bytes with server error 1489/HY000. This was a reproducible concatenation limit, not a timeout. The literal now uses `REPLACE('a01b','01',CHR(0))`, choosing an absent marker that cannot overlap itself. Ordinary strings keep their existing quoted form. Quote doubling, charset expansion and complete encoded-byte budgets remain enforced before allocation; there is no CLOB coercion, retry, truncation or replay.

| Check | Result |
| --- | --- |
| Local literal regressions | Marker collisions, leading/trailing/adjacent NUL, insertion boundaries, quotes, exact byte limits and one-byte-short limits pass |
| Mixed-charset runtime budget regression | GBK NUL expression passes at the exact message limit and rejects one byte short |
| `TestOracleClientLongNUL` | 6/6 pass: 12003/98304/196608 bytes, single/double SELECT scripts, repeated named input, exact complete results, complete result consumption and same-connection Ping; 10-second operation deadlines |
| `TestOracleClientLongNULStorage` | Three lengths, each with NUL and equal-length plain-text controls, pass multi-statement CLOB inserts and independent connection checks of full length and content |

The independent reader initially requested 8192 characters from DBMS_LOB.SUBSTR but received 8191. Diagnostic requests of 2000/8191/8192/10000 returned 2000/8191/8191/8191, each matching the expected prefix. The permanent verification uses 2000-character chunks and compares every byte of every NUL/control row, respecting the function's charset-dependent return limit.

These results cover only the listed lengths, shapes and target. Larger statements, other server versions, native complex parameter formats and deployment acceptance remain open; U02/U03 native RECORD/ANYDATA support is not implied.

## U08 continuation: the 1 MiB client-literal boundary

The exact client-literal boundary on the direct Oracle 4.4.2.2 target is 1 MiB (1,048,576 bytes):

| Probe | Result |
| --- | --- |
| `REPLACE(literal, marker, CHR(0))` at exactly 1,048,576 bytes | Returns the expected NUL-bearing value |
| Same form at 1,048,577 / 1,049,600 / 1,100,000 / 1,572,864 / 3,145,728 bytes | Server 600/22001, arguments `-5098`, "Varchar value is too long for the column" |
| Pure SQL literal of 3 MiB without NUL substitution | Succeeds, and a 3 MiB `REPLACE` with an absent marker also succeeds |
| Server preparation (`PrepareServer`) with 1.5/2/3 MiB NUL values | Passes via segmented long data (positional and named) |

The rejection is the target's VARCHAR2 literal limit for the NUL-bearing `REPLACE` result, not the SQL text size. The driver keeps the current rendering: silently switching to server preparation would violate the explicit `PrepareClient` contract, and truncating or retrying would change data. Callers that need more than 1 MiB of text with NUL bytes should use server preparation (long data) or split the value.

Verification added for the boundary: `TestOracleClientLongNUL` now also runs single and double scripts at exactly 1,048,576 bytes; `TestOracleClientLongNULStorageBoundary` stores a 1 MiB NUL CLOB through client preparation and independently checks length, `INSTR` NUL position, and the 2000-byte prefix and suffix. Full byte-for-byte chunk comparison stays at 12,003/98,304/196,608 bytes to keep the suite runtime bounded.

## Review and completion checks

An independent code review found one stale mixed-charset budget fixture, which was updated to the new REPLACE expression while retaining exact-limit/one-byte-short assertions. No remaining production-code finding was reported. A separate exhaustive model checked marker reconstruction and byte budgets over 320001 short inputs.

The full `go test ./... -count=1 -timeout 5m`, `go build ./...`, `go vet ./...`, integration-tag compilation, and ordinary/integration `golangci-lint run` passed. Focused live XMLTYPE, client constructors, prepared/native RETURNING and server/client scalar calls also passed after the literal change. Windows named-pipe tests ran outside the filesystem sandbox. Private configuration scanning found zero matches in repository files; `git diff --check` passed. These local checks do not replace the deployment acceptance boundaries above.
