package catalog

import (
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

const (
	metadataTypeTinyInt   = "TINYINT"
	metadataTypeSmallInt  = "SMALLINT"
	metadataTypeMediumInt = "MEDIUMINT"
	metadataTypeYear      = "YEAR"
	metadataTypeEnum      = "ENUM"
	metadataTypeSet       = "SET"
)

// BuiltinTypeInfo describes SQL families, not JDBC constants or per-column Go scan types.
// Unknown/server-dependent limits stay NULL; this is not a server capability probe.
func BuiltinTypeInfo(mode config.Mode) [][]driver.Value {
	var rows [][]driver.Value
	add := func(name, category string, precision, minScale, maxScale, radix driver.Value, prefix, suffix, params driver.Value, auto driver.Value) {
		rows = append(rows, []driver.Value{name, category, precision, minScale, maxScale, radix, prefix, suffix, params, auto})
	}
	if mode == config.ModeMySQL {
		for _, n := range []struct {
			name      string
			precision int64
		}{{metadataTypeTinyInt, 3}, {metadataTypeSmallInt, 5}, {metadataTypeMediumInt, 7}, {"INT", 10}, {valueformat.TypeNameBigInt, 19}} {
			add(n.name, "numeric", n.precision, int64(0), int64(0), int64(10), nil, nil, nil, int64(1))
		}
		add(valueformat.TypeNameDecimal, "numeric", int64(65), int64(0), int64(30), int64(10), nil, nil, "precision,scale", int64(0))
		add(valueformat.TypeNameFloat, "numeric", int64(24), nil, nil, int64(2), nil, nil, nil, int64(0))
		add(valueformat.TypeNameDouble, "numeric", int64(53), nil, nil, int64(2), nil, nil, nil, int64(0))
		add("BIT", "binary", int64(64), nil, nil, int64(2), "b'", "'", "length", int64(0))
		for _, name := range []string{valueformat.TypeNameChar, valueformat.TypeNameVarchar, "TINYTEXT", "TEXT", "MEDIUMTEXT", "LONGTEXT", metadataTypeEnum, metadataTypeSet, "JSON"} {
			var params driver.Value
			if name == valueformat.TypeNameChar || name == valueformat.TypeNameVarchar {
				params = "length"
			}
			if name == metadataTypeEnum || name == metadataTypeSet {
				params = "values"
			}
			add(name, "character", nil, nil, nil, nil, "'", "'", params, int64(0))
		}
		for _, name := range []string{valueformat.TypeNameBinary, valueformat.TypeNameVarbinary, "TINYBLOB", valueformat.TypeNameBlob, "MEDIUMBLOB", "LONGBLOB"} {
			var params driver.Value
			if name == valueformat.TypeNameBinary || name == valueformat.TypeNameVarbinary {
				params = "length"
			}
			add(name, "binary", nil, nil, nil, nil, "X'", "'", params, int64(0))
		}
		for _, name := range []string{valueformat.TypeNameDate, valueformat.TypeNameTime, valueformat.TypeNameDateTime, valueformat.TypeNameTimestamp, metadataTypeYear} {
			var lo, hi, params driver.Value
			if name == valueformat.TypeNameTime || name == valueformat.TypeNameDateTime || name == valueformat.TypeNameTimestamp {
				lo, hi, params = int64(0), int64(6), "fractional_seconds_precision"
			}
			add(name, "datetime", nil, lo, hi, nil, "'", "'", params, int64(0))
		}
		return rows
	}
	add(valueformat.TypeNameNumber, "numeric", int64(38), int64(-84), int64(127), int64(10), nil, nil, "precision,scale", nil)
	add(valueformat.TypeNameFloat, "numeric", int64(126), nil, nil, int64(2), nil, nil, "binary_precision", nil)
	add(valueformat.TypeNameBinaryFloat, "numeric", int64(24), nil, nil, int64(2), nil, nil, nil, nil)
	add(valueformat.TypeNameBinaryDouble, "numeric", int64(53), nil, nil, int64(2), nil, nil, nil, nil)
	for _, name := range []string{valueformat.TypeNameChar, valueformat.TypeNameVarchar2, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2} {
		add(name, "character", nil, nil, nil, nil, "'", "'", "length", nil)
	}
	add(valueformat.TypeNameRaw, "binary", nil, nil, nil, nil, "'", "'", "length", nil)
	add(valueformat.TypeNameRowID, "rowid", nil, nil, nil, nil, "'", "'", nil, nil)
	add(valueformat.TypeNameIntervalYM, "interval", int64(9), nil, nil, int64(10), "INTERVAL '", "' YEAR TO MONTH", "leading_precision", nil)
	add(valueformat.TypeNameIntervalDS, "interval", int64(9), int64(0), int64(9), int64(10), "INTERVAL '", "' DAY TO SECOND", "leading_precision,fractional_seconds_precision", nil)
	add(valueformat.TypeNameDate, "datetime", nil, nil, nil, nil, "DATE '", "'", nil, nil)
	for _, name := range []string{valueformat.TypeNameTimestamp, valueformat.TypeNameTimestampTZ, valueformat.TypeNameTimestampLTZ} {
		add(name, "datetime", nil, int64(0), int64(9), nil, "TIMESTAMP '", "'", "fractional_seconds_precision", nil)
	}
	for _, name := range []string{valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob} {
		add(name, "lob", nil, nil, nil, nil, nil, nil, nil, nil)
	}
	return rows
}
