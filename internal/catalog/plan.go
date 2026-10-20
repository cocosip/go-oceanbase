// Package catalog owns dictionary plans and command-scoped descriptors.
package catalog

import (
	"database/sql/driver"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

const sqlNullLiteral = "NULL"
const (
	metadataDual           = "dual"
	metadataColumnName     = "c.COLUMN_NAME"
	metadataKeyTable       = "k.TABLE_NAME"
	metadataConstraintName = "k.CONSTRAINT_NAME"
	metadataPrivilegeTable = "p.TABLE_NAME"
	metadataGrantee        = "p.GRANTEE"
)

// Column describes one fixed metadata projection.
type Column struct {
	Name    string
	Numeric bool
}

// Query contains dictionary SQL, bind values and its public projection.
type Query struct {
	SQL     string
	Args    []driver.NamedValue
	Columns []Column
}

// Plan selects a supported metadata dictionary projection.
func Plan(mode config.Mode, version string, filter metadata.MetadataFilter) (Query, error) {
	if mode != config.ModeMySQL && mode != config.ModeOracle {
		return Query{}, dberrors.ErrUnsupported
	}
	if filter.Kind == metadata.MetadataTypeInfo {
		if filter.Catalog != "" || filter.SchemaPattern != "" || filter.NamePattern != "" || filter.ColumnPattern != "" || filter.ReferencedSchemaPattern != "" || filter.ReferencedNamePattern != "" || len(filter.TableTypes) != 0 || filter.Query != "" {
			return Query{}, fmt.Errorf("%w: type information does not accept filters", dberrors.ErrBind)
		}
		return Query{Columns: Columns(filter.Kind)}, nil
	}
	if filter.Kind == metadata.MetadataStatementParameters {
		if strings.TrimSpace(filter.Query) == "" || filter.Catalog != "" || filter.SchemaPattern != "" || filter.NamePattern != "" || filter.ColumnPattern != "" || filter.ReferencedSchemaPattern != "" || filter.ReferencedNamePattern != "" || len(filter.TableTypes) > 0 {
			return Query{}, fmt.Errorf("%w: statement metadata requires only Query", dberrors.ErrBind)
		}
		return Query{SQL: filter.Query, Columns: Columns(filter.Kind)}, nil
	}
	if filter.Query != "" || filter.Kind < metadata.MetadataCatalogs || filter.Kind > metadata.MetadataServerInfo {
		return Query{}, fmt.Errorf("%w: metadata kind or query", dberrors.ErrBind)
	}
	if mode == config.ModeOracle {
		return OraclePlan(filter)
	}
	return MySQLPlan(version, filter, false)
}

type metadataSource struct {
	from, where, order                                  string
	schema, name, column, refSchema, refName, tableType string
	expressions                                         []string
}

// Columns returns the fixed projection for a metadata kind.
func Columns(kind metadata.MetadataKind) []Column {
	var names string
	switch kind {
	case metadata.MetadataCatalogs:
		names = "catalog"
	case metadata.MetadataSchemas:
		names = "catalog schema"
	case metadata.MetadataTables:
		names = "catalog schema name type remarks"
	case metadata.MetadataSynonyms:
		names = "catalog schema name target_schema target_name database_link"
	case metadata.MetadataColumns:
		names = "catalog schema table_name name #ordinal_position data_type #length #precision #scale #nullable default_value remarks type_owner #auto_increment #generated"
	case metadata.MetadataPrimaryKeys:
		names = "catalog schema table_name name column_name #ordinal_position"
	case metadata.MetadataForeignKeys:
		names = "catalog schema table_name name column_name #ordinal_position referenced_catalog referenced_schema referenced_table referenced_column update_rule delete_rule deferrable initially_deferred"
	case metadata.MetadataIndexes:
		names = "catalog schema table_name name #non_unique column_name #ordinal_position ordering #cardinality type"
	case metadata.MetadataTablePrivileges:
		names = "catalog schema table_name grantor grantee privilege #grantable"
	case metadata.MetadataColumnPrivileges:
		names = "catalog schema table_name column_name grantor grantee privilege #grantable"
	case metadata.MetadataRoutines:
		names = "catalog schema name specific_name package_name type return_type"
	case metadata.MetadataRoutineParameters:
		names = "catalog schema routine_name specific_name package_name name #ordinal_position direction data_type #length #precision #scale type_owner type_name #data_level"
	case metadata.MetadataTypes:
		names = "catalog schema name type #attribute_count #final #instantiable"
	case metadata.MetadataTypeAttributes:
		names = "catalog schema type_name name #ordinal_position data_type type_owner #length #precision #scale"
	case metadata.MetadataCollectionElements:
		names = "catalog schema name collection_type #upper_bound element_type element_owner #length #precision #scale"
	case metadata.MetadataServerInfo:
		names = "oceanbase_version compatibility_mode #parameter_dictionary #type_dictionary"
	case metadata.MetadataStatementParameters:
		names = "#physical_ordinal #logical_ordinal name direction data_type #nullable #length #precision #scale #protocol_type #protocol_direction"
	case metadata.MetadataTypeInfo:
		names = "type_name category #precision #minimum_scale #maximum_scale #radix literal_prefix literal_suffix create_parameters #auto_increment"
	}
	fields := strings.Fields(names)
	columns := make([]Column, len(fields))
	for i, name := range fields {
		columns[i] = Column{strings.TrimPrefix(name, "#"), strings.HasPrefix(name, "#")}
	}
	return columns
}

// Only source expressions are SQL; every caller-controlled value stays in args.
func buildMetadataQuery(mode config.Mode, f metadata.MetadataFilter, s metadataSource) (Query, error) {
	if len(f.TableTypes) > 4 {
		return Query{}, fmt.Errorf("%w: at most four metadata table types", dberrors.ErrBind)
	}
	p := Query{Columns: Columns(f.Kind)}
	if len(p.Columns) != len(s.expressions) {
		return p, fmt.Errorf("oceanbase: invalid metadata projection")
	}
	projection := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		projection[i] = s.expressions[i] + ` AS "` + c.Name + `"`
	}
	p.SQL = "SELECT " + strings.Join(projection, ",") + " FROM " + s.from + " WHERE 1=1"
	if s.where != "" {
		p.SQL += " AND (" + s.where + ")"
	}
	add := func(expr, value string, like bool) error {
		if value == "" {
			return nil
		}
		if expr == "" {
			return fmt.Errorf("%w: filter is not applicable to metadata kind", dberrors.ErrBind)
		}
		p.Args = append(p.Args, driver.NamedValue{Ordinal: len(p.Args) + 1, Value: value})
		p.SQL += " AND " + expr
		if like {
			p.SQL += " LIKE ? ESCAPE '/'"
		} else {
			p.SQL += " = ?"
		}
		return nil
	}
	catalog := "'def'"
	if mode == config.ModeOracle {
		catalog = sqlNullLiteral
	}
	for _, pair := range [][2]string{{catalog, f.Catalog}, {s.schema, f.SchemaPattern}, {s.name, f.NamePattern}, {s.column, f.ColumnPattern}, {s.refSchema, f.ReferencedSchemaPattern}, {s.refName, f.ReferencedNamePattern}} {
		if err := add(pair[0], pair[1], pair[0] != catalog); err != nil {
			return Query{}, err
		}
	}
	if len(f.TableTypes) > 0 {
		if s.tableType == "" {
			return Query{}, fmt.Errorf("%w: table types require table metadata", dberrors.ErrBind)
		}
		p.SQL += " AND " + s.tableType + " IN ("
		for i, typ := range f.TableTypes {
			switch typ {
			case "TABLE", "VIEW", "SYNONYM", "MATERIALIZED VIEW":
			default:
				return Query{}, fmt.Errorf("%w: metadata table type", dberrors.ErrBind)
			}
			if i > 0 {
				p.SQL += ","
			}
			p.SQL += "?"
			p.Args = append(p.Args, driver.NamedValue{Ordinal: len(p.Args) + 1, Value: typ})
		}
		p.SQL += ")"
	}
	if s.order != "" {
		p.SQL += " ORDER BY " + s.order
	}
	return p, nil
}

var obMetadataVersion = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+)\.([0-9]+)\.[0-9]+\.[0-9]+(?:[^0-9.]|$)`)

// HasParameterDictionary reports whether the server version has a reliable parameter dictionary.
func HasParameterDictionary(version string) bool {
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
