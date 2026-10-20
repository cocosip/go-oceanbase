package catalog

import (
	"context"
	"database/sql/driver"
	"math"
	"sort"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// Target contains individual dictionary names and already normalized key values.
type Target struct {
	Schema, Table, Column string
	Key                   map[string]any
}

// TableBlobPlan contains resolved SQL identifiers and a complete primary key.
type TableBlobPlan struct {
	Table, Column, Predicate string
	Args                     []driver.NamedValue
	Locking                  bool
	Capacity                 int64
}

// From returns the table and primary-key predicate suffix.
func (p *TableBlobPlan) From() string { return " FROM " + p.Table + " WHERE " + p.Predicate }

// DictionaryPlan retains the original eight-column table/key dictionary query.
func DictionaryPlan(mode config.Mode, schema, table string) Query {
	query := MySQLBlobDictionarySQL
	if mode == config.ModeOracle {
		query = OracleBlobDictionarySQL
	}
	return Query{SQL: query, Args: []driver.NamedValue{{Ordinal: 1, Value: schema}, {Ordinal: 2, Value: table}}}
}

// LookupTableBlob reads table metadata and resolves a writable BLOB plan.
func LookupTableBlob(ctx context.Context, op *session.Operation, target Target) (*TableBlobPlan, error) {
	epoch := op.Epoch()
	if target.Schema == "" {
		query := "SELECT DATABASE()"
		if op.Protocol().Mode == config.ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL"
		}
		result, err := QueryRows(ctx, op, Query{SQL: query})
		if err != nil {
			return nil, err
		}
		if op.Epoch() != epoch {
			return nil, dberrors.ErrResource
		}
		value, err := Scalar(result)
		if err != nil {
			return nil, err
		}
		target.Schema = Text(value)
		if !valueformat.Identifier(target.Schema) {
			return nil, dberrors.ErrLOB
		}
	}
	result, err := QueryRows(ctx, op, DictionaryPlan(op.Protocol().Mode, target.Schema, target.Table))
	if err != nil {
		return nil, err
	}
	if op.Epoch() != epoch {
		return nil, dberrors.ErrResource
	}
	return ResolveTableBlob(op.Protocol().Mode, target, []Result{result})
}

const blobBaseTable = "BASE TABLE"
const blobLockingEngine = "InnoDB"

// MySQLBlobDictionarySQL is the fixed MySQL table, column and primary-key query.
const MySQLBlobDictionarySQL = "SELECT t.TABLE_SCHEMA,t.TABLE_NAME,t.TABLE_TYPE,t.ENGINE,c.COLUMN_NAME,c.DATA_TYPE,c.EXTRA,k.ORDINAL_POSITION FROM information_schema.TABLES t JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=t.TABLE_SCHEMA AND c.TABLE_NAME=t.TABLE_NAME LEFT JOIN information_schema.KEY_COLUMN_USAGE k ON k.TABLE_SCHEMA=c.TABLE_SCHEMA AND k.TABLE_NAME=c.TABLE_NAME AND k.COLUMN_NAME=c.COLUMN_NAME AND k.CONSTRAINT_NAME='PRIMARY' WHERE t.TABLE_SCHEMA = ? AND t.TABLE_NAME = ? ORDER BY c.ORDINAL_POSITION"

// OracleBlobDictionarySQL is the fixed Oracle table, column and primary-key query.
const OracleBlobDictionarySQL = "SELECT t.OWNER,t.TABLE_NAME,'BASE TABLE',NULL,c.COLUMN_NAME,c.DATA_TYPE,c.VIRTUAL_COLUMN,p.POSITION FROM ALL_TABLES t JOIN ALL_OBJECTS o ON o.OWNER=t.OWNER AND o.OBJECT_NAME=t.TABLE_NAME AND o.OBJECT_TYPE='TABLE' AND o.SUBOBJECT_NAME IS NULL JOIN ALL_TAB_COLS c ON c.OWNER=t.OWNER AND c.TABLE_NAME=t.TABLE_NAME LEFT JOIN (SELECT k.OWNER,k.TABLE_NAME,c.COLUMN_NAME,c.POSITION FROM ALL_CONSTRAINTS k JOIN ALL_CONS_COLUMNS c ON c.OWNER=k.OWNER AND c.TABLE_NAME=k.TABLE_NAME AND c.CONSTRAINT_NAME=k.CONSTRAINT_NAME WHERE k.CONSTRAINT_TYPE='P') p ON p.OWNER=c.OWNER AND p.TABLE_NAME=c.TABLE_NAME AND p.COLUMN_NAME=c.COLUMN_NAME WHERE t.OWNER = ? AND t.TABLE_NAME = ? AND c.HIDDEN_COLUMN='NO' ORDER BY c.COLUMN_ID"

// ResolveTableBlob validates dictionary rows and builds a table BLOB plan.
func ResolveTableBlob(mode config.Mode, t Target, sets []Result) (*TableBlobPlan, error) {
	if len(sets) != 1 || len(sets[0].Columns) != 8 || len(sets[0].Rows) == 0 {
		return nil, dberrors.ErrLOB
	}
	rows := sets[0].Rows
	schema, table := Text(rows[0][0]), Text(rows[0][1])
	if !valueformat.Identifier(schema) || !valueformat.Identifier(table) {
		return nil, dberrors.ErrLOB
	}
	lob := &TableBlobPlan{Table: sqltext.QuoteIdentifier(mode, schema) + "." + sqltext.QuoteIdentifier(mode, table), Locking: strings.EqualFold(Text(rows[0][3]), blobLockingEngine)}
	type keyColumn struct {
		name    string
		ordinal int64
		value   any
	}
	var keys []keyColumn
	used := make(map[string]bool)
	for _, row := range rows {
		if Text(row[0]) != schema || Text(row[1]) != table || Text(row[2]) != blobBaseTable {
			return nil, dberrors.ErrLOB
		}
		name, typ := Text(row[4]), strings.ToUpper(Text(row[5]))
		if !valueformat.Identifier(name) {
			return nil, dberrors.ErrLOB
		}
		if MatchIdentifier(mode, t.Column, name) {
			generated := strings.ToUpper(Text(row[6]))
			if !ValidTableBlobColumn(mode, typ, generated, row[7]) || lob.Column != "" {
				return nil, dberrors.ErrLOB
			}
			lob.Column = sqltext.QuoteIdentifier(mode, name)
			lob.Capacity = TableBlobCapacity(typ)
		}
		if row[7] == nil {
			continue
		}
		ordinal, err := Length(row[7])
		if err != nil || ordinal < 1 || !TableBlobKeyType(mode, typ) {
			return nil, dberrors.ErrLOB
		}
		value, err := ResolveKey(mode, name, t.Key, used)
		if err != nil {
			return nil, err
		}
		keys = append(keys, keyColumn{name, ordinal, value})
	}
	if lob.Column == "" || len(keys) == 0 || len(used) != len(t.Key) {
		return nil, dberrors.ErrLOB
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ordinal < keys[j].ordinal })
	var predicates []string
	for i, key := range keys {
		if key.ordinal != int64(i+1) {
			return nil, dberrors.ErrLOB
		}
		predicates = append(predicates, sqltext.QuoteIdentifier(mode, key.name)+" = ?")
		lob.Args = append(lob.Args, driver.NamedValue{Ordinal: i + 1, Value: key.value})
	}
	lob.Predicate = strings.Join(predicates, " AND ")
	return lob, nil
}

// ValidTableBlobColumn checks that the requested BLOB column is writable.
func ValidTableBlobColumn(mode config.Mode, typ, generated string, primary driver.Value) bool {
	binary := typ == valueformat.TypeNameBlob || mode == config.ModeMySQL && TableBlobCapacity(typ) != 0
	computed := generated == "YES" || strings.Contains(generated, "VIRTUAL GENERATED") || strings.Contains(generated, "STORED GENERATED")
	return binary && primary == nil && !computed
}

// TableBlobCapacity returns a supported BLOB type byte capacity.
func TableBlobCapacity(typ string) int64 {
	switch typ {
	case "TINYBLOB":
		return 255
	case valueformat.TypeNameBlob:
		return 65535
	case "MEDIUMBLOB":
		return 16777215
	case "LONGBLOB":
		return math.MaxUint32
	}
	return 0
}

// MatchIdentifier applies mode-specific dictionary identifier matching.
func MatchIdentifier(mode config.Mode, input, name string) bool {
	return input == name || mode == config.ModeMySQL && strings.EqualFold(input, name)
}

// ResolveKey finds one unambiguous normalized primary-key value.
func ResolveKey(mode config.Mode, name string, key map[string]any, used map[string]bool) (any, error) {
	var selected any
	for input, value := range key {
		if MatchIdentifier(mode, input, name) {
			if selected != nil || used[input] {
				return nil, dberrors.ErrLOB
			}
			used[input], selected = true, value
		}
	}
	if selected == nil {
		return nil, dberrors.ErrLOB
	}
	return selected, nil
}

// TableBlobKeyType reports whether a primary-key dictionary type is supported.
func TableBlobKeyType(mode config.Mode, typ string) bool {
	if strings.HasPrefix(typ, "TIMESTAMP") {
		return true
	}
	switch typ {
	case valueformat.TypeNameDate, valueformat.TypeNameChar, valueformat.TypeNameFloat:
		return true
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", valueformat.TypeNameInteger, valueformat.TypeNameBigInt, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric, valueformat.TypeNameDouble, "REAL", "BIT", "YEAR", valueformat.TypeNameDateTime, valueformat.TypeNameTime, valueformat.TypeNameVarchar, valueformat.TypeNameBinary, valueformat.TypeNameVarbinary, "ENUM", "SET":
		return mode == config.ModeMySQL
	case "NUMBER", "BINARY_FLOAT", "BINARY_DOUBLE", "VARCHAR2", "NVARCHAR2", "NCHAR", "RAW", "ROWID", "UROWID", "INTERVAL YEAR TO MONTH", "INTERVAL DAY TO SECOND":
		return mode == config.ModeOracle
	}
	return false
}
