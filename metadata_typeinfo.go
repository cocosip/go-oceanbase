package oceanbase

import "database/sql/driver"

const (
	metadataTypeTinyInt   = "TINYINT"
	metadataTypeSmallInt  = "SMALLINT"
	metadataTypeMediumInt = "MEDIUMINT"
	metadataTypeYear      = "YEAR"
	metadataTypeEnum      = "ENUM"
	metadataTypeSet       = "SET"
)

// builtinTypeInfo describes SQL families, not JDBC constants or per-column Go scan types.
// Unknown/server-dependent limits stay NULL; this is not a server capability probe.
func builtinTypeInfo(mode Mode) [][]driver.Value {
	var rows [][]driver.Value
	add := func(name, category string, precision, minScale, maxScale, radix driver.Value, prefix, suffix, params driver.Value, auto driver.Value) {
		rows = append(rows, []driver.Value{name, category, precision, minScale, maxScale, radix, prefix, suffix, params, auto})
	}
	if mode == ModeMySQL {
		for _, n := range []struct {
			name      string
			precision int64
		}{{metadataTypeTinyInt, 3}, {metadataTypeSmallInt, 5}, {metadataTypeMediumInt, 7}, {"INT", 10}, {typeNameBigInt, 19}} {
			add(n.name, "numeric", n.precision, int64(0), int64(0), int64(10), nil, nil, nil, int64(1))
		}
		add(typeNameDecimal, "numeric", int64(65), int64(0), int64(30), int64(10), nil, nil, "precision,scale", int64(0))
		add(typeNameFloat, "numeric", int64(24), nil, nil, int64(2), nil, nil, nil, int64(0))
		add(typeNameDouble, "numeric", int64(53), nil, nil, int64(2), nil, nil, nil, int64(0))
		add("BIT", "binary", int64(64), nil, nil, int64(2), "b'", "'", "length", int64(0))
		for _, name := range []string{typeNameChar, typeNameVarchar, "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT", metadataTypeEnum, metadataTypeSet, "JSON"} {
			var params driver.Value
			if name == typeNameChar || name == typeNameVarchar {
				params = "length"
			}
			if name == metadataTypeEnum || name == metadataTypeSet {
				params = "values"
			}
			add(name, "character", nil, nil, nil, nil, "'", "'", params, int64(0))
		}
		for _, name := range []string{typeNameBinary, typeNameVarbinary, "TINYBLOB", typeNameBlob, "MEDIUMBLOB", "LONGBLOB"} {
			var params driver.Value
			if name == typeNameBinary || name == typeNameVarbinary {
				params = "length"
			}
			add(name, "binary", nil, nil, nil, nil, "X'", "'", params, int64(0))
		}
		for _, name := range []string{typeNameDate, typeNameTime, typeNameDateTime, typeNameTimestamp, metadataTypeYear} {
			var lo, hi, params driver.Value
			if name == typeNameTime || name == typeNameDateTime || name == typeNameTimestamp {
				lo, hi, params = int64(0), int64(6), "fractional_seconds_precision"
			}
			add(name, "datetime", nil, lo, hi, nil, "'", "'", params, int64(0))
		}
		return rows
	}
	add(typeNameNumber, "numeric", int64(38), int64(-84), int64(127), int64(10), nil, nil, "precision,scale", nil)
	add(typeNameFloat, "numeric", int64(126), nil, nil, int64(2), nil, nil, "binary_precision", nil)
	add(typeNameBinaryFloat, "numeric", int64(24), nil, nil, int64(2), nil, nil, nil, nil)
	add(typeNameBinaryDouble, "numeric", int64(53), nil, nil, int64(2), nil, nil, nil, nil)
	for _, name := range []string{typeNameChar, typeNameVarchar2, typeNameNChar, typeNameNVarchar2} {
		add(name, "character", nil, nil, nil, nil, "'", "'", "length", nil)
	}
	add(typeNameRaw, "binary", nil, nil, nil, nil, "'", "'", "length", nil)
	add(typeNameRowID, "rowid", nil, nil, nil, nil, "'", "'", nil, nil)
	add(typeNameIntervalYM, "interval", int64(9), nil, nil, int64(10), "INTERVAL '", "' YEAR TO MONTH", "leading_precision", nil)
	add(typeNameIntervalDS, "interval", int64(9), int64(0), int64(9), int64(10), "INTERVAL '", "' DAY TO SECOND", "leading_precision,fractional_seconds_precision", nil)
	add(typeNameDate, "datetime", nil, nil, nil, nil, "DATE '", "'", nil, nil)
	for _, name := range []string{typeNameTimestamp, typeNameTimestampTZ, typeNameTimestampLTZ} {
		add(name, "datetime", nil, int64(0), int64(9), nil, "TIMESTAMP '", "'", "fractional_seconds_precision", nil)
	}
	for _, name := range []string{typeNameBlob, typeNameClob, typeNameNClob} {
		add(name, "lob", nil, nil, nil, nil, nil, nil, nil, nil)
	}
	return rows
}
