package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// MetadataKind selects one fixed metadata result schema, documented in the architecture.
type MetadataKind uint8

const (
	metadataDual           = "dual"
	metadataColumnName     = "c.COLUMN_NAME"
	metadataKeyTable       = "k.TABLE_NAME"
	metadataConstraintName = "k.CONSTRAINT_NAME"
	metadataPrivilegeTable = "p.TABLE_NAME"
	metadataGrantee        = "p.GRANTEE"
)

// Metadata kinds. Views are selected through MetadataTables and TableTypes.
const (
	MetadataCatalogs MetadataKind = iota + 1
	MetadataSchemas
	MetadataTables
	MetadataSynonyms
	MetadataColumns
	MetadataPrimaryKeys
	MetadataForeignKeys
	MetadataIndexes
	MetadataTablePrivileges
	MetadataColumnPrivileges
	MetadataRoutines
	MetadataRoutineParameters
	MetadataTypes
	MetadataTypeAttributes
	MetadataCollectionElements
	MetadataServerInfo
	MetadataStatementParameters
)

// MetadataFilter uses case-preserving LIKE patterns with '/' as the escape.
// Empty patterns select all accessible objects. Catalog is an exact match.
// Query is used only for MetadataStatementParameters and is prepared, never executed.
type MetadataFilter struct {
	Kind                    MetadataKind
	Catalog                 string
	SchemaPattern           string
	NamePattern             string
	ColumnPattern           string
	ReferencedSchemaPattern string
	ReferencedNamePattern   string
	TableTypes              []string
	Query                   string
}

type metadataColumn struct {
	name    string
	numeric bool
}
type metadataQuery struct {
	query   string
	args    []driver.NamedValue
	columns []metadataColumn
}
type metadataRequest struct{ filter MetadataFilter }

// errResponseActive rejects an internal command before the current response has ended.
var errResponseActive = errors.New("oceanbase: an unread response prevents an internal command")

// Metadata queries the dictionary of a pinned *sql.Conn or *sql.Tx.
// The caller owns the returned Rows and must consume or close them.
func Metadata(ctx context.Context, session Session, filter MetadataFilter) (*sql.Rows, error) {
	if !validSession(session) {
		return nil, fmt.Errorf("oceanbase: a non-nil *sql.Conn or *sql.Tx is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return session.QueryContext(ctx, "", metadataRequest{filter: filter})
}

func metadataPlan(mode Mode, version string, filter MetadataFilter) (metadataQuery, error) {
	if mode != ModeMySQL && mode != ModeOracle {
		return metadataQuery{}, ErrUnsupported
	}
	if filter.Kind == MetadataStatementParameters {
		if strings.TrimSpace(filter.Query) == "" || filter.Catalog != "" || filter.SchemaPattern != "" || filter.NamePattern != "" || filter.ColumnPattern != "" || filter.ReferencedSchemaPattern != "" || filter.ReferencedNamePattern != "" || len(filter.TableTypes) > 0 {
			return metadataQuery{}, fmt.Errorf("%w: statement metadata requires only Query", ErrBind)
		}
		return metadataQuery{query: filter.Query, columns: metadataColumns(filter.Kind)}, nil
	}
	if filter.Query != "" || filter.Kind < MetadataCatalogs || filter.Kind > MetadataServerInfo {
		return metadataQuery{}, fmt.Errorf("%w: metadata kind or query", ErrBind)
	}
	if mode == ModeOracle {
		return oracleMetadataPlan(filter)
	}
	return mysqlMetadataPlan(version, filter)
}

type metadataSource struct {
	from, where, order                                  string
	schema, name, column, refSchema, refName, tableType string
	expressions                                         []string
}

func metadataColumns(kind MetadataKind) []metadataColumn {
	var names string
	switch kind {
	case MetadataCatalogs:
		names = "catalog"
	case MetadataSchemas:
		names = "catalog schema"
	case MetadataTables:
		names = "catalog schema name type remarks"
	case MetadataSynonyms:
		names = "catalog schema name target_schema target_name database_link"
	case MetadataColumns:
		names = "catalog schema table_name name #ordinal_position data_type #length #precision #scale #nullable default_value remarks type_owner"
	case MetadataPrimaryKeys:
		names = "catalog schema table_name name column_name #ordinal_position"
	case MetadataForeignKeys:
		names = "catalog schema table_name name column_name #ordinal_position referenced_catalog referenced_schema referenced_table referenced_column update_rule delete_rule deferrable initially_deferred"
	case MetadataIndexes:
		names = "catalog schema table_name name #non_unique column_name #ordinal_position ordering #cardinality type"
	case MetadataTablePrivileges:
		names = "catalog schema table_name grantor grantee privilege #grantable"
	case MetadataColumnPrivileges:
		names = "catalog schema table_name column_name grantor grantee privilege #grantable"
	case MetadataRoutines:
		names = "catalog schema name specific_name package_name type return_type"
	case MetadataRoutineParameters:
		names = "catalog schema routine_name specific_name package_name name #ordinal_position direction data_type #length #precision #scale type_owner type_name #data_level"
	case MetadataTypes:
		names = "catalog schema name type #attribute_count #final #instantiable"
	case MetadataTypeAttributes:
		names = "catalog schema type_name name #ordinal_position data_type type_owner #length #precision #scale"
	case MetadataCollectionElements:
		names = "catalog schema name collection_type #upper_bound element_type element_owner #length #precision #scale"
	case MetadataServerInfo:
		names = "oceanbase_version compatibility_mode #parameter_dictionary #type_dictionary"
	case MetadataStatementParameters:
		names = "#physical_ordinal #logical_ordinal name direction data_type #nullable #length #precision #scale #protocol_type #protocol_direction"
	}
	fields := strings.Fields(names)
	columns := make([]metadataColumn, len(fields))
	for i, name := range fields {
		columns[i] = metadataColumn{strings.TrimPrefix(name, "#"), strings.HasPrefix(name, "#")}
	}
	return columns
}

// Only source expressions are SQL; every caller-controlled value stays in args.
func buildMetadataQuery(mode Mode, f MetadataFilter, s metadataSource) (metadataQuery, error) {
	if len(f.TableTypes) > 4 {
		return metadataQuery{}, fmt.Errorf("%w: at most four metadata table types", ErrBind)
	}
	p := metadataQuery{columns: metadataColumns(f.Kind)}
	if len(p.columns) != len(s.expressions) {
		return p, fmt.Errorf("oceanbase: invalid metadata projection")
	}
	projection := make([]string, len(p.columns))
	for i, c := range p.columns {
		projection[i] = s.expressions[i] + ` AS "` + c.name + `"`
	}
	p.query = "SELECT " + strings.Join(projection, ",") + " FROM " + s.from + " WHERE 1=1"
	if s.where != "" {
		p.query += " AND (" + s.where + ")"
	}
	add := func(expr, value string, like bool) error {
		if value == "" {
			return nil
		}
		if expr == "" {
			return fmt.Errorf("%w: filter is not applicable to metadata kind", ErrBind)
		}
		p.args = append(p.args, driver.NamedValue{Ordinal: len(p.args) + 1, Value: value})
		p.query += " AND " + expr
		if like {
			p.query += " LIKE ? ESCAPE '/'"
		} else {
			p.query += " = ?"
		}
		return nil
	}
	catalog := "'def'"
	if mode == ModeOracle {
		catalog = sqlNullLiteral
	}
	for _, pair := range [][2]string{{catalog, f.Catalog}, {s.schema, f.SchemaPattern}, {s.name, f.NamePattern}, {s.column, f.ColumnPattern}, {s.refSchema, f.ReferencedSchemaPattern}, {s.refName, f.ReferencedNamePattern}} {
		if err := add(pair[0], pair[1], pair[0] != catalog); err != nil {
			return metadataQuery{}, err
		}
	}
	if len(f.TableTypes) > 0 {
		if s.tableType == "" {
			return metadataQuery{}, fmt.Errorf("%w: table types require table metadata", ErrBind)
		}
		p.query += " AND " + s.tableType + " IN ("
		for i, typ := range f.TableTypes {
			switch typ {
			case "TABLE", "VIEW", "SYNONYM", "MATERIALIZED VIEW":
			default:
				return metadataQuery{}, fmt.Errorf("%w: metadata table type", ErrBind)
			}
			if i > 0 {
				p.query += ","
			}
			p.query += "?"
			p.args = append(p.args, driver.NamedValue{Ordinal: len(p.args) + 1, Value: typ})
		}
		p.query += ")"
	}
	if s.order != "" {
		p.query += " ORDER BY " + s.order
	}
	return p, nil
}

var obMetadataVersion = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+)\.([0-9]+)\.[0-9]+\.[0-9]+(?:[^0-9.]|$)`)

func hasParameterDictionary(version string) bool {
	match := obMetadataVersion.FindStringSubmatch(version)
	if match == nil {
		return false
	}
	major, err := strconv.ParseUint(match[1], 10, 32)
	if err != nil {
		return false
	}
	minor, err := strconv.ParseUint(match[2], 10, 32)
	return err == nil && (major > 4 || major == 4 && minor >= 1)
}

// metadataRows normalizes dictionary scalars to one mode-independent result schema.
type metadataRows struct {
	driver.Rows
	columns []metadataColumn
}

func (r *metadataRows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, c := range r.columns {
		names[i] = c.name
	}
	return names
}
func (r *metadataRows) Next(values []driver.Value) error {
	if err := r.Rows.Next(values); err != nil {
		return err
	}
	for i, column := range r.columns {
		if values[i] == nil {
			continue
		}
		if column.numeric {
			switch v := values[i].(type) {
			case int64:
			case string:
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					return fmt.Errorf("oceanbase: invalid numeric metadata column %s", column.name)
				}
				values[i] = n
			default:
				return fmt.Errorf("oceanbase: invalid numeric metadata column %s", column.name)
			}
		} else {
			switch v := values[i].(type) {
			case string:
			case []byte:
				values[i] = string(v)
			default:
				return fmt.Errorf("oceanbase: invalid text metadata column %s", column.name)
			}
		}
	}
	return nil
}
func (r *metadataRows) ColumnTypeDatabaseTypeName(i int) string {
	if r.columns[i].numeric {
		return "BIGINT"
	}
	return "VARCHAR"
}
func (r *metadataRows) ColumnTypeScanType(i int) reflect.Type {
	if r.columns[i].numeric {
		return reflect.TypeFor[int64]()
	}
	return reflect.TypeFor[string]()
}
func (*metadataRows) ColumnTypeNullable(int) (bool, bool) { return true, true }

func (c *conn) queryMetadata(ctx context.Context, f MetadataFilter) (driver.Rows, error) {
	if f.Kind == MetadataStatementParameters {
		return c.describeParameters(ctx, f)
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	rows, err := c.metadataLocked(ctx, f)
	if err != nil {
		c.unlock()
		return nil, err
	}
	rows.keepGate = false
	if rows.released {
		c.unlock()
	}
	return &metadataRows{Rows: rows, columns: metadataColumns(f.Kind)}, nil
}

// metadataLocked shares the public SQL plan, never calls database/sql, and leaves mu owned.
func (c *conn) metadataLocked(ctx context.Context, f MetadataFilter) (*textRows, error) {
	if c.responseActive {
		return nil, errResponseActive
	}
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	plan, err := metadataPlan(c.session.mode, c.session.obVersion, f)
	if err != nil {
		return nil, err
	}
	if f.Kind == MetadataStatementParameters {
		return nil, ErrUnsupported
	}
	query, err := c.interpolate(plan.query, plan.args)
	if err != nil {
		return nil, err
	}
	rows, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, query...), false, nil, true)
	if err != nil {
		return nil, err
	}
	if len(rows.columns) != len(plan.columns) {
		closeErr := rows.Close()
		return nil, errors.Join(fmt.Errorf("oceanbase: metadata projection count differs from plan"), closeErr)
	}
	return rows, nil
}

type parameterRows struct {
	values [][]driver.Value
	index  int
}

func (*parameterRows) Columns() []string {
	return (&metadataRows{columns: metadataColumns(MetadataStatementParameters)}).Columns()
}
func (r *parameterRows) Close() error { r.values = nil; return nil }
func (r *parameterRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func (c *conn) describeParameters(ctx context.Context, f MetadataFilter) (driver.Rows, error) {
	plan, err := metadataPlan(c.session.mode, c.session.obVersion, f)
	if err != nil {
		return nil, err
	}
	s, err := c.prepareServer(ctx, plan.query)
	if err != nil {
		return nil, err
	}
	if err = s.Close(); err != nil {
		return nil, err
	}
	values := make([][]driver.Value, len(s.parameters))
	for i, column := range s.parameters {
		slot := s.plan.slots[i]
		row := []driver.Value{int64(i + 1), int64(slot.index + 1), nil, nil, nil, nil, nil, nil, nil, int64(column.Type), int64(column.InOut)}
		if slot.name != "" {
			row[2] = slot.name
		}
		if column.Type != 6 {
			if name := textTypeName(column, c.session.mode); name != "" {
				row[4] = name
			}
		}
		if c.session.mode == ModeOracle {
			row[5] = int64(0)
			if column.Flags&1 == 0 {
				row[5] = int64(1)
			}
		}
		if n, ok := columnLength(column, c.session.mode); ok {
			row[6] = n
		}
		if p, scale, ok := columnPrecisionScale(column, c.session.mode); ok {
			row[7], row[8] = p, scale
		}
		values[i] = row
	}
	return &metadataRows{Rows: &parameterRows{values: values}, columns: plan.columns}, nil
}
