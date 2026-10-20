package catalog

import (
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

// MySQLPlan builds the MySQL dictionary query and bounded filter values.
func MySQLPlan(version string, f metadata.MetadataFilter, procedureOnly bool) (Query, error) {
	s := metadataSource{}
	switch f.Kind {
	case metadata.MetadataCatalogs:
		s.from = metadataDual
		s.expressions = []string{"'def'"}
	case metadata.MetadataSchemas:
		s.from = "information_schema.SCHEMATA s"
		s.schema = "s.SCHEMA_NAME"
		s.order = s.schema
		s.expressions = []string{"s.CATALOG_NAME", s.schema}
	case metadata.MetadataTables:
		s.from = "information_schema.TABLES t"
		s.schema = "t.TABLE_SCHEMA"
		s.name = "t.TABLE_NAME"
		s.order = "t.TABLE_SCHEMA,t.TABLE_NAME"
		s.tableType = "CASE t.TABLE_TYPE WHEN 'BASE TABLE' THEN 'TABLE' WHEN 'SYSTEM VIEW' THEN 'VIEW' ELSE t.TABLE_TYPE END"
		s.expressions = []string{"t.TABLE_CATALOG", s.schema, s.name, s.tableType, "t.TABLE_COMMENT"}
	case metadata.MetadataColumns:
		s.from = "information_schema.COLUMNS c"
		s.schema = "c.TABLE_SCHEMA"
		s.name = "c.TABLE_NAME"
		s.column = metadataColumnName
		s.order = "c.TABLE_SCHEMA,c.TABLE_NAME,c.ORDINAL_POSITION"
		s.expressions = []string{"c.TABLE_CATALOG", s.schema, s.name, s.column, "c.ORDINAL_POSITION", "c.DATA_TYPE", "c.CHARACTER_MAXIMUM_LENGTH", "c.NUMERIC_PRECISION", "CASE WHEN c.DATA_TYPE IN ('time','datetime','timestamp') THEN c.DATETIME_PRECISION ELSE c.NUMERIC_SCALE END", "CASE c.IS_NULLABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END", "c.COLUMN_DEFAULT", "c.COLUMN_COMMENT", sqlNullLiteral}
		s.expressions = append(s.expressions, "CASE WHEN c.EXTRA IS NULL THEN NULL WHEN LOWER(c.EXTRA) LIKE '%auto_increment%' THEN 1 ELSE 0 END", "CASE WHEN c.EXTRA IS NULL THEN NULL WHEN UPPER(c.EXTRA) IN ('VIRTUAL','PERSISTENT') OR UPPER(c.EXTRA) LIKE '%VIRTUAL GENERATED%' OR UPPER(c.EXTRA) LIKE '%STORED GENERATED%' THEN 1 ELSE 0 END")
	case metadata.MetadataPrimaryKeys:
		s.from = "information_schema.KEY_COLUMN_USAGE k JOIN information_schema.TABLE_CONSTRAINTS t ON t.CONSTRAINT_CATALOG=k.CONSTRAINT_CATALOG AND t.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND t.TABLE_NAME=k.TABLE_NAME AND t.CONSTRAINT_NAME=k.CONSTRAINT_NAME"
		s.where = "t.CONSTRAINT_TYPE='PRIMARY KEY'"
		s.schema = "k.TABLE_SCHEMA"
		s.name = metadataKeyTable
		s.column = "k.COLUMN_NAME"
		s.order = "k.TABLE_SCHEMA,k.TABLE_NAME,k.ORDINAL_POSITION"
		s.expressions = []string{"k.TABLE_CATALOG", s.schema, s.name, metadataConstraintName, s.column, "k.ORDINAL_POSITION"}
	case metadata.MetadataForeignKeys:
		s.from = "information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON r.CONSTRAINT_CATALOG=k.CONSTRAINT_CATALOG AND r.CONSTRAINT_SCHEMA=k.CONSTRAINT_SCHEMA AND r.TABLE_NAME=k.TABLE_NAME AND r.CONSTRAINT_NAME=k.CONSTRAINT_NAME"
		s.schema = "k.TABLE_SCHEMA"
		s.name = metadataKeyTable
		s.column = "k.COLUMN_NAME"
		s.refSchema = "k.REFERENCED_TABLE_SCHEMA"
		s.refName = "k.REFERENCED_TABLE_NAME"
		s.order = "k.TABLE_SCHEMA,k.TABLE_NAME,k.CONSTRAINT_NAME,k.ORDINAL_POSITION"
		s.expressions = []string{"k.TABLE_CATALOG", s.schema, s.name, metadataConstraintName, s.column, "k.ORDINAL_POSITION", "r.UNIQUE_CONSTRAINT_CATALOG", s.refSchema, s.refName, "k.REFERENCED_COLUMN_NAME", "r.UPDATE_RULE", "r.DELETE_RULE", sqlNullLiteral, sqlNullLiteral}
	case metadata.MetadataIndexes:
		s.from = "information_schema.STATISTICS i"
		s.schema = "i.TABLE_SCHEMA"
		s.name = "i.TABLE_NAME"
		s.column = "i.COLUMN_NAME"
		s.order = "i.TABLE_SCHEMA,i.TABLE_NAME,i.INDEX_NAME,i.SEQ_IN_INDEX"
		s.expressions = []string{"i.TABLE_CATALOG", s.schema, s.name, "i.INDEX_NAME", "i.NON_UNIQUE", s.column, "i.SEQ_IN_INDEX", "CASE i.COLLATION WHEN 'A' THEN 'ASC' WHEN 'D' THEN 'DESC' END", "i.CARDINALITY", "i.INDEX_TYPE"}
	case metadata.MetadataTablePrivileges, metadata.MetadataColumnPrivileges:
		s.from = "information_schema.TABLE_PRIVILEGES p"
		s.schema = "p.TABLE_SCHEMA"
		s.name = metadataPrivilegeTable
		s.order = "p.TABLE_SCHEMA,p.TABLE_NAME,p.GRANTEE,p.PRIVILEGE_TYPE"
		s.expressions = []string{"p.TABLE_CATALOG", s.schema, s.name, sqlNullLiteral, metadataGrantee, "p.PRIVILEGE_TYPE", "CASE p.IS_GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END"}
		if f.Kind == metadata.MetadataColumnPrivileges {
			s.from = "information_schema.COLUMN_PRIVILEGES p"
			s.column = "p.COLUMN_NAME"
			s.expressions = []string{"p.TABLE_CATALOG", s.schema, s.name, s.column, sqlNullLiteral, metadataGrantee, "p.PRIVILEGE_TYPE", "CASE p.IS_GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END"}
		}
	case metadata.MetadataRoutines:
		s.from = "information_schema.ROUTINES r"
		s.schema = "r.ROUTINE_SCHEMA"
		s.name = "r.ROUTINE_NAME"
		s.order = "r.ROUTINE_SCHEMA,r.ROUTINE_NAME,r.SPECIFIC_NAME"
		s.expressions = []string{"r.ROUTINE_CATALOG", s.schema, s.name, "r.SPECIFIC_NAME", sqlNullLiteral, "r.ROUTINE_TYPE", "r.DATA_TYPE"}
	case metadata.MetadataRoutineParameters:
		if !HasParameterDictionary(version) {
			if _, _, err := ValidateRoutineFallback(f); err != nil {
				return Query{}, err
			}
			return Query{Columns: Columns(f.Kind)}, nil
		}
		s.from = "information_schema.PARAMETERS p JOIN information_schema.ROUTINES r ON r.SPECIFIC_NAME=p.SPECIFIC_NAME AND r.ROUTINE_SCHEMA=p.SPECIFIC_SCHEMA AND r.ROUTINE_CATALOG=p.SPECIFIC_CATALOG AND r.ROUTINE_TYPE=p.ROUTINE_TYPE"
		if procedureOnly {
			s.where = "p.ROUTINE_TYPE='PROCEDURE'"
		}
		s.schema = "p.SPECIFIC_SCHEMA"
		s.name = "r.ROUTINE_NAME"
		s.column = "p.PARAMETER_NAME"
		s.order = "p.SPECIFIC_SCHEMA,r.ROUTINE_NAME,p.SPECIFIC_NAME,p.ORDINAL_POSITION"
		s.expressions = []string{"p.SPECIFIC_CATALOG", s.schema, s.name, "p.SPECIFIC_NAME", sqlNullLiteral, s.column, "p.ORDINAL_POSITION", "CASE WHEN p.ORDINAL_POSITION=0 THEN 'RETURN' ELSE p.PARAMETER_MODE END", "p.DATA_TYPE", "p.CHARACTER_MAXIMUM_LENGTH", "p.NUMERIC_PRECISION", "CASE WHEN p.DATA_TYPE IN ('time','datetime','timestamp') THEN p.DATETIME_PRECISION ELSE p.NUMERIC_SCALE END", sqlNullLiteral, sqlNullLiteral, sqlNullLiteral}
	case metadata.MetadataServerInfo:
		s.from = metadataDual
		parameters := "0"
		if HasParameterDictionary(version) {
			parameters = "1"
		}
		s.expressions = []string{"ob_version()", "'MYSQL'", parameters, "0"}
	default:
		return Query{}, fmt.Errorf("%w: metadata kind %d in MySQL compatibility mode", dberrors.ErrUnsupported, f.Kind)
	}
	return buildMetadataQuery(config.ModeMySQL, f, s)
}
