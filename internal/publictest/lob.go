package publictest

import (
	"encoding/binary"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// LOBFreeSQL is the expected temporary BLOB cleanup call.
const LOBFreeSQL = "DECLARE l BLOB := ?; BEGIN IF DBMS_LOB.ISTEMPORARY(l) = 1 THEN DBMS_LOB.FREETEMPORARY(l); END IF; ? := l; END;"

// LOBTrimSQL is the expected native trim call.
const LOBTrimSQL = "CALL DBMS_LOB.TRIM(?, ?)"

// LOBInteger encodes an integer in native LOB protocol fixtures.
func LOBInteger(n int64) wire.Parameter {
	return wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, uint64(n))}
}

// LOBUnits counts the server character units used by CLOB protocol fixtures.
func LOBUnits(text string, national bool) int64 {
	n := int64(utf8.RuneCountInString(text))
	if national {
		for _, r := range text {
			if r > 0xffff {
				n++
			}
		}
	}
	return n
}
