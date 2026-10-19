package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// BlobTarget identifies one BLOB column by its complete primary key.
// Names are individual dictionary identifiers, without SQL quoting or qualification.
type BlobTarget struct {
	Schema string
	Table  string
	Column string
	Key    map[string]any
}

type tableBlobOpenRequest struct {
	session Session
	target  BlobTarget
	result  **resourceHandle
}

type tableBlobResource struct {
	table, column, predicate string
	args                     []driver.NamedValue
	locking                  bool
	capacity                 int64
}

// OpenTableBlob opens a BLOB without materializing its contents. SQL NULL returns nil, nil.
// MySQL writes require a caller-owned, writable *sql.Tx. Reads on *sql.Conn have no cross-call snapshot.
// Oracle names must match dictionary spelling; MySQL column names are resolved without case sensitivity.
func OpenTableBlob(ctx context.Context, session Session, target BlobTarget) (*Blob, error) {
	if !validSession(session) {
		return nil, ErrResource
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := normalizeBlobTarget(target)
	if err != nil {
		return nil, err
	}
	var h *resourceHandle
	_, err = session.ExecContext(ctx, "", tableBlobOpenRequest{session: session, target: target, result: &h})
	if err != nil || h == nil {
		return nil, err
	}
	return &Blob{handle: h}, nil
}

func validBlobIdentifier(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsRune(name, 0)
}

func normalizeBlobTarget(t BlobTarget) (BlobTarget, error) {
	if !validBlobIdentifier(t.Table) || !validBlobIdentifier(t.Column) || t.Schema != "" && !validBlobIdentifier(t.Schema) || len(t.Key) == 0 || len(t.Key) > maxTextColumns {
		return t, ErrLOB
	}
	key := make(map[string]any, len(t.Key))
	for name, v := range t.Key {
		if !validBlobIdentifier(name) {
			return t, ErrLOB
		}
		value, err := convertArgument(v)
		if err != nil {
			return t, err
		}
		switch v := value.(type) {
		case []byte:
			if v == nil {
				return t, ErrLOB
			}
			value = slices.Clone(v)
		case Raw:
			if v == nil {
				return t, ErrLOB
			}
			value = slices.Clone(v)
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return t, ErrLOB
			}
		case string, int64, uint64, bool, time.Time, Number, NString, RowID, TimestampTZ, TimestampLTZ, IntervalYM, IntervalDS:
		default:
			return t, fmt.Errorf("%w: unsupported or NULL primary key value", ErrLOB)
		}
		key[name] = value
	}
	t.Key = key
	return t, nil
}

const blobBaseTable = "BASE TABLE"
const blobLockingEngine = "InnoDB"

const mysqlBlobDictionarySQL = "SELECT t.TABLE_SCHEMA,t.TABLE_NAME,t.TABLE_TYPE,t.ENGINE,c.COLUMN_NAME,c.DATA_TYPE,c.EXTRA,k.ORDINAL_POSITION FROM information_schema.TABLES t JOIN information_schema.COLUMNS c ON c.TABLE_SCHEMA=t.TABLE_SCHEMA AND c.TABLE_NAME=t.TABLE_NAME LEFT JOIN information_schema.KEY_COLUMN_USAGE k ON k.TABLE_SCHEMA=c.TABLE_SCHEMA AND k.TABLE_NAME=c.TABLE_NAME AND k.COLUMN_NAME=c.COLUMN_NAME AND k.CONSTRAINT_NAME='PRIMARY' WHERE t.TABLE_SCHEMA = ? AND t.TABLE_NAME = ? ORDER BY c.ORDINAL_POSITION"

const oracleBlobDictionarySQL = "SELECT t.OWNER,t.TABLE_NAME,'BASE TABLE',NULL,c.COLUMN_NAME,c.DATA_TYPE,c.VIRTUAL_COLUMN,p.POSITION FROM ALL_TABLES t JOIN ALL_OBJECTS o ON o.OWNER=t.OWNER AND o.OBJECT_NAME=t.TABLE_NAME AND o.OBJECT_TYPE='TABLE' AND o.SUBOBJECT_NAME IS NULL JOIN ALL_TAB_COLS c ON c.OWNER=t.OWNER AND c.TABLE_NAME=t.TABLE_NAME LEFT JOIN (SELECT k.OWNER,k.TABLE_NAME,c.COLUMN_NAME,c.POSITION FROM ALL_CONSTRAINTS k JOIN ALL_CONS_COLUMNS c ON c.OWNER=k.OWNER AND c.TABLE_NAME=k.TABLE_NAME AND c.CONSTRAINT_NAME=k.CONSTRAINT_NAME WHERE k.CONSTRAINT_TYPE='P') p ON p.OWNER=c.OWNER AND p.TABLE_NAME=c.TABLE_NAME AND p.COLUMN_NAME=c.COLUMN_NAME WHERE t.OWNER = ? AND t.TABLE_NAME = ? AND c.HIDDEN_COLUMN='NO' ORDER BY c.COLUMN_ID"

func (c *conn) execOpenTableBlob(ctx context.Context, request tableBlobOpenRequest) (_ driver.Result, err error) {
	if !validSession(request.session) || request.result == nil {
		return nil, ErrResource
	}
	if err = c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	var selected *resourceHandle
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		err = errors.Join(err, c.cleanupResourcesLocked(cleanup, false))
		if err == nil {
			err = ctx.Err()
		}
		if err != nil && selected != nil {
			if record, e := c.resourceRecordLocked(selected); e == nil {
				err = errors.Join(err, c.releaseResourceLocked(cleanup, selected.id, record))
			}
		}
		if err == nil {
			*request.result = selected
		}
	}()
	if c.closed.Load() {
		return nil, ErrResource
	}
	t := request.target
	if t.Schema == "" {
		query := "SELECT DATABASE()"
		if c.session.mode == ModeOracle {
			query = "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL"
		}
		sets, e := c.tableBlobQueryLocked(ctx, query, nil, false)
		if e != nil {
			return nil, e
		}
		value, e := tableBlobScalar(sets)
		if e != nil {
			return nil, e
		}
		t.Schema = tableBlobText(value)
		if !validBlobIdentifier(t.Schema) {
			return nil, ErrLOB
		}
	}
	query := mysqlBlobDictionarySQL
	if c.session.mode == ModeOracle {
		query = oracleBlobDictionarySQL
	}
	sets, err := c.tableBlobQueryLocked(ctx, query, namedValues([]driver.Value{t.Schema, t.Table}), false)
	if err != nil {
		return nil, err
	}
	lob, err := resolveTableBlob(c.session.mode, t, sets)
	if err != nil {
		return nil, err
	}
	if c.session.mode == ModeOracle {
		query = "SELECT " + lob.column + lob.from()
		if _, transaction := request.session.(*sql.Tx); transaction && c.activeTx != nil && !c.activeTx.readOnly && c.session.ok.Status&8192 == 0 {
			query += " FOR UPDATE"
		}
		sets, err = c.tableBlobQueryLocked(ctx, query, lob.args, true)
		if err != nil {
			return nil, err
		}
		selected, err = c.selectLOBRowsLocked(request.session, 210, sets)
		return result{known: true}, err
	}
	value, err := c.tableBlobValueLocked(ctx, lob, "OCTET_LENGTH("+lob.column+")", nil, false)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return result{known: true}, nil
	}
	if _, err = tableBlobLength(value); err != nil {
		return nil, err
	}
	selected, err = c.registerResourceLocked(request.session, nil)
	if err != nil {
		return nil, err
	}
	c.resources.records[selected.id].tableBlob = lob
	err = c.publishResourceLocked(selected)
	return result{known: true}, err
}

// Every internal response finishes under the existing gate, without reentering database/sql.
func (c *conn) tableBlobQueryLocked(ctx context.Context, query string, args []driver.NamedValue, capture bool) ([]lobRowset, error) {
	if c.responseActive {
		return nil, errResponseActive
	}
	if c.closed.Load() {
		return nil, ErrResource
	}
	query, err := c.interpolate(query, args)
	if err != nil {
		return nil, err
	}
	if capture {
		ctx = context.WithValue(ctx, lobOpenContextKey{}, &lobOpenState{})
	}
	key := c.resourceKeyLocked()
	r, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, query...), false, nil, true)
	if err != nil {
		return nil, err
	}
	r.holdRelease = true
	sets, err := r.collectLOBResponse(ctx)
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	if c.resources.key != key {
		return nil, r.fail(ErrResource, false)
	}
	r.release()
	return sets, r.err
}

func tableBlobScalar(sets []lobRowset) (driver.Value, error) {
	if len(sets) != 1 || len(sets[0].columns) != 1 || len(sets[0].rows) > 1 {
		return nil, ErrLOB
	}
	if len(sets[0].rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return sets[0].rows[0][0], nil
}

func tableBlobText(v driver.Value) string {
	switch v := v.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

func quoteBlobIdentifier(mode Mode, name string) string {
	quote := "`"
	if mode == ModeOracle {
		quote = `"`
	}
	return quote + strings.ReplaceAll(name, quote, quote+quote) + quote
}

func resolveTableBlob(mode Mode, t BlobTarget, sets []lobRowset) (*tableBlobResource, error) {
	if len(sets) != 1 || len(sets[0].columns) != 8 || len(sets[0].rows) == 0 {
		return nil, ErrLOB
	}
	rows := sets[0].rows
	schema, table := tableBlobText(rows[0][0]), tableBlobText(rows[0][1])
	if !validBlobIdentifier(schema) || !validBlobIdentifier(table) {
		return nil, ErrLOB
	}
	lob := &tableBlobResource{table: quoteBlobIdentifier(mode, schema) + "." + quoteBlobIdentifier(mode, table), locking: strings.EqualFold(tableBlobText(rows[0][3]), blobLockingEngine)}
	type keyColumn struct {
		name    string
		ordinal int64
		value   any
	}
	var keys []keyColumn
	used := make(map[string]bool)
	for _, row := range rows {
		if tableBlobText(row[0]) != schema || tableBlobText(row[1]) != table || tableBlobText(row[2]) != blobBaseTable {
			return nil, ErrLOB
		}
		name, typ := tableBlobText(row[4]), strings.ToUpper(tableBlobText(row[5]))
		if !validBlobIdentifier(name) {
			return nil, ErrLOB
		}
		if matchBlobIdentifier(mode, t.Column, name) {
			generated := strings.ToUpper(tableBlobText(row[6]))
			if !validTableBlobColumn(mode, typ, generated, row[7]) || lob.column != "" {
				return nil, ErrLOB
			}
			lob.column = quoteBlobIdentifier(mode, name)
			lob.capacity = tableBlobCapacity(typ)
		}
		if row[7] == nil {
			continue
		}
		ordinal, err := tableBlobLength(row[7])
		if err != nil || ordinal < 1 || !tableBlobKeyType(mode, typ) {
			return nil, ErrLOB
		}
		value, err := resolveBlobKey(mode, name, t.Key, used)
		if err != nil {
			return nil, err
		}
		keys = append(keys, keyColumn{name, ordinal, value})
	}
	if lob.column == "" || len(keys) == 0 || len(used) != len(t.Key) {
		return nil, ErrLOB
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].ordinal < keys[j].ordinal })
	var predicates []string
	for i, key := range keys {
		if key.ordinal != int64(i+1) {
			return nil, ErrLOB
		}
		predicates = append(predicates, quoteBlobIdentifier(mode, key.name)+" = ?")
		lob.args = append(lob.args, driver.NamedValue{Ordinal: i + 1, Value: key.value})
	}
	lob.predicate = strings.Join(predicates, " AND ")
	return lob, nil
}

func validTableBlobColumn(mode Mode, typ, generated string, primary driver.Value) bool {
	binary := typ == typeNameBlob || mode == ModeMySQL && tableBlobCapacity(typ) != 0
	computed := generated == "YES" || strings.Contains(generated, "VIRTUAL GENERATED") || strings.Contains(generated, "STORED GENERATED")
	return binary && primary == nil && !computed
}

func tableBlobCapacity(typ string) int64 {
	switch typ {
	case "TINYBLOB":
		return 255
	case typeNameBlob:
		return 65535
	case "MEDIUMBLOB":
		return 16777215
	case "LONGBLOB":
		return math.MaxUint32
	}
	return 0
}

func matchBlobIdentifier(mode Mode, input, name string) bool {
	return input == name || mode == ModeMySQL && strings.EqualFold(input, name)
}

func resolveBlobKey(mode Mode, name string, key map[string]any, used map[string]bool) (any, error) {
	var selected any
	for input, value := range key {
		if matchBlobIdentifier(mode, input, name) {
			if selected != nil || used[input] {
				return nil, ErrLOB
			}
			used[input], selected = true, value
		}
	}
	if selected == nil {
		return nil, ErrLOB
	}
	return selected, nil
}

func tableBlobKeyType(mode Mode, typ string) bool {
	if strings.HasPrefix(typ, "TIMESTAMP") {
		return true
	}
	switch typ {
	case typeNameDate, typeNameChar, typeNameFloat:
		return true
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", typeNameInteger, typeNameBigInt, typeNameDecimal, typeNameNumeric, typeNameDouble, "REAL", "BIT", "YEAR", typeNameDateTime, typeNameTime, typeNameVarchar, typeNameBinary, typeNameVarbinary, "ENUM", "SET":
		return mode == ModeMySQL
	case "NUMBER", "BINARY_FLOAT", "BINARY_DOUBLE", "VARCHAR2", "NVARCHAR2", "NCHAR", "RAW", "ROWID", "UROWID", "INTERVAL YEAR TO MONTH", "INTERVAL DAY TO SECOND":
		return mode == ModeOracle
	}
	return false
}

func tableBlobLength(value driver.Value) (int64, error) {
	if b, ok := value.([]byte); ok {
		value = string(b)
	}
	n, err := lobAmount(value)
	if err != nil || n < 0 {
		return 0, ErrLOB
	}
	return n, nil
}

func (l *tableBlobResource) from() string { return " FROM " + l.table + " WHERE " + l.predicate }
