package valueformat

import (
	"strings"
	"unicode/utf8"
)

// Shared dictionary type names.
const (
	TypeNameRefCursor    = "REF CURSOR"
	TypeNameObject       = "OBJECT"
	TypeNameArray        = "ARRAY"
	TypeNameNumeric      = "NUMERIC"
	TypeNameInteger      = "INTEGER"
	TypeNameTime         = "TIME"
	TypeNameBoolean      = "BOOLEAN"
	TypeNameBinary       = "BINARY"
	TypeNameVarbinary    = "VARBINARY"
	TypeNameDecimal      = "DECIMAL"
	TypeNameBigInt       = "BIGINT"
	TypeNameDateTime     = "DATETIME"
	TypeNameIntervalDS   = "INTERVAL DAY TO SECOND"
	TypeNameBinaryFloat  = "BINARY_FLOAT"
	TypeNameBinaryDouble = "BINARY_DOUBLE"
	TypeNameVarchar2     = "VARCHAR2"
	TypeNameRaw          = "RAW"
	TypeNameTimestampTZ  = "TIMESTAMP WITH TIME ZONE"
	TypeNameTimestampLTZ = "TIMESTAMP WITH LOCAL TIME ZONE"
	TypeNameIntervalYM   = "INTERVAL YEAR TO MONTH"
	TypeNameNVarchar2    = "NVARCHAR2"
	TypeNameNChar        = "NCHAR"
	TypeNameRowID        = "ROWID"
	TypeNameFloat        = "FLOAT"
	TypeNameDouble       = "DOUBLE"
	TypeNameVarchar      = "VARCHAR"

	TypeNameDate      = "DATE"
	TypeNameNumber    = "NUMBER"
	TypeNameTimestamp = "TIMESTAMP"
	TypeNameChar      = "CHAR"
)

// Shared LOB dictionary and complex representation names.
const (
	TypeNameBlob  = "BLOB"
	TypeNameClob  = "CLOB"
	TypeNameNClob = "NCLOB"
	ComplexObject = "object"
	ComplexArray  = "array"
)

// Identifier accepts dictionary spelling without a NUL byte.
func Identifier(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}
