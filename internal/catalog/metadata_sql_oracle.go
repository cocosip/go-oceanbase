package catalog

import (
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/metadata"
)

// OraclePlan builds the Oracle dictionary query and bounded filter values.
func OraclePlan(f metadata.MetadataFilter) (Query, error) {
	s := metadataSource{}
	switch f.Kind {
	case metadata.MetadataCatalogs:
		s.from = metadataDual
		s.where = "1=0"
		s.expressions = []string{"CAST(NULL AS VARCHAR2(128))"}
	case metadata.MetadataSchemas:
		s.from = "ALL_USERS u"
		s.schema = "u.USERNAME"
		s.order = s.schema
		s.expressions = []string{sqlNullLiteral, s.schema}
	case metadata.MetadataTables:
		s.from = "ALL_OBJECTS o LEFT JOIN ALL_TAB_COMMENTS c ON c.OWNER=o.OWNER AND c.TABLE_NAME=o.OBJECT_NAME AND c.TABLE_TYPE=o.OBJECT_TYPE"
		s.where = "o.OBJECT_TYPE IN ('TABLE','VIEW','SYNONYM','MATERIALIZED VIEW') AND o.SUBOBJECT_NAME IS NULL"
		s.schema = "o.OWNER"
		s.name = "o.OBJECT_NAME"
		s.tableType = "o.OBJECT_TYPE"
		s.order = "o.OWNER,o.OBJECT_NAME,o.OBJECT_TYPE"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, s.tableType, "c.COMMENTS"}
	case metadata.MetadataSynonyms:
		s.from = "ALL_SYNONYMS s"
		s.schema = "s.OWNER"
		s.name = "s.SYNONYM_NAME"
		s.order = "s.OWNER,s.SYNONYM_NAME"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "s.TABLE_OWNER", "s.TABLE_NAME", "s.DB_LINK"}
	case metadata.MetadataColumns:
		s.from = "(SELECT a.*,ROW_NUMBER() OVER(PARTITION BY a.OWNER,a.TABLE_NAME ORDER BY a.COLUMN_ID) AS ORDINAL_POSITION FROM ALL_TAB_COLS a WHERE a.HIDDEN_COLUMN='NO') c LEFT JOIN ALL_COL_COMMENTS m ON m.OWNER=c.OWNER AND m.TABLE_NAME=c.TABLE_NAME AND m.COLUMN_NAME=c.COLUMN_NAME"
		s.where = "c.HIDDEN_COLUMN='NO'"
		s.schema = "c.OWNER"
		s.name = "c.TABLE_NAME"
		s.column = metadataColumnName
		s.order = "c.OWNER,c.TABLE_NAME,c.COLUMN_ID"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, s.column, "c.ORDINAL_POSITION", "c.DATA_TYPE", "CASE WHEN c.CHAR_USED='C' THEN c.CHAR_LENGTH ELSE c.DATA_LENGTH END", "c.DATA_PRECISION", "c.DATA_SCALE", "CASE c.NULLABLE WHEN 'Y' THEN 1 WHEN 'N' THEN 0 END", "c.DATA_DEFAULT", "m.COMMENTS", "c.DATA_TYPE_OWNER"}
		s.expressions = append(s.expressions, sqlNullLiteral, "CASE c.VIRTUAL_COLUMN WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END")
	case metadata.MetadataPrimaryKeys:
		s.from = "ALL_CONSTRAINTS k JOIN ALL_CONS_COLUMNS c ON c.OWNER=k.OWNER AND c.CONSTRAINT_NAME=k.CONSTRAINT_NAME AND c.TABLE_NAME=k.TABLE_NAME"
		s.where = "k.CONSTRAINT_TYPE='P'"
		s.schema = "k.OWNER"
		s.name = metadataKeyTable
		s.column = metadataColumnName
		s.order = "k.OWNER,k.TABLE_NAME,c.POSITION"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, metadataConstraintName, s.column, "c.POSITION"}
	case metadata.MetadataForeignKeys:
		s.from = "ALL_CONSTRAINTS k JOIN ALL_CONS_COLUMNS c ON c.OWNER=k.OWNER AND c.CONSTRAINT_NAME=k.CONSTRAINT_NAME AND c.TABLE_NAME=k.TABLE_NAME JOIN ALL_CONSTRAINTS r ON r.OWNER=k.R_OWNER AND r.CONSTRAINT_NAME=k.R_CONSTRAINT_NAME JOIN ALL_CONS_COLUMNS rc ON rc.OWNER=r.OWNER AND rc.CONSTRAINT_NAME=r.CONSTRAINT_NAME AND rc.TABLE_NAME=r.TABLE_NAME AND rc.POSITION=c.POSITION"
		s.where = "k.CONSTRAINT_TYPE='R'"
		s.schema = "k.OWNER"
		s.name = metadataKeyTable
		s.column = metadataColumnName
		s.refSchema = "r.OWNER"
		s.refName = "r.TABLE_NAME"
		s.order = "k.OWNER,k.TABLE_NAME,k.CONSTRAINT_NAME,c.POSITION"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, metadataConstraintName, s.column, "c.POSITION", sqlNullLiteral, s.refSchema, s.refName, "rc.COLUMN_NAME", sqlNullLiteral, "k.DELETE_RULE", "k.DEFERRABLE", "k.DEFERRED"}
	case metadata.MetadataIndexes:
		s.from = "ALL_INDEXES i JOIN ALL_IND_COLUMNS c ON c.INDEX_OWNER=i.OWNER AND c.INDEX_NAME=i.INDEX_NAME AND c.TABLE_OWNER=i.TABLE_OWNER AND c.TABLE_NAME=i.TABLE_NAME"
		s.schema = "i.TABLE_OWNER"
		s.name = "i.TABLE_NAME"
		s.column = metadataColumnName
		s.order = "i.TABLE_OWNER,i.TABLE_NAME,i.INDEX_NAME,c.COLUMN_POSITION"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "i.INDEX_NAME", "CASE i.UNIQUENESS WHEN 'UNIQUE' THEN 0 WHEN 'NONUNIQUE' THEN 1 END", s.column, "c.COLUMN_POSITION", "c.DESCEND", "i.DISTINCT_KEYS", "i.INDEX_TYPE"}
	case metadata.MetadataTablePrivileges:
		s.from = "ALL_TAB_PRIVS p"
		s.schema = "p.TABLE_SCHEMA"
		s.name = metadataPrivilegeTable
		s.order = "p.TABLE_SCHEMA,p.TABLE_NAME,p.GRANTEE,p.PRIVILEGE"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "p.GRANTOR", metadataGrantee, "p.PRIVILEGE", "CASE p.GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END"}
	case metadata.MetadataColumnPrivileges:
		s.from = "ALL_COL_PRIVS p"
		s.schema = "p.OWNER"
		s.name = metadataPrivilegeTable
		s.column = "p.COLUMN_NAME"
		s.order = "p.OWNER,p.TABLE_NAME,p.COLUMN_NAME,p.GRANTEE,p.PRIVILEGE"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, s.column, "p.GRANTOR", metadataGrantee, "p.PRIVILEGE", "CASE p.GRANTABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END"}
	case metadata.MetadataRoutines:
		s.from = "ALL_PROCEDURES p"
		s.where = "p.OBJECT_TYPE IN ('PROCEDURE','FUNCTION') OR p.OBJECT_TYPE='PACKAGE' AND p.PROCEDURE_NAME IS NOT NULL"
		s.schema = "p.OWNER"
		s.name = "COALESCE(p.PROCEDURE_NAME,p.OBJECT_NAME)"
		s.order = "p.OWNER,p.OBJECT_NAME,p.SUBPROGRAM_ID"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "TO_CHAR(p.OBJECT_ID)||':'||TO_CHAR(p.SUBPROGRAM_ID)", "CASE WHEN p.OBJECT_TYPE='PACKAGE' THEN p.OBJECT_NAME END", "CASE WHEN EXISTS (SELECT 1 FROM ALL_ARGUMENTS a WHERE a.OWNER=p.OWNER AND a.OBJECT_ID=p.OBJECT_ID AND a.SUBPROGRAM_ID=p.SUBPROGRAM_ID AND a.POSITION=0 AND a.DATA_LEVEL=0) THEN 'FUNCTION' ELSE 'PROCEDURE' END", "(SELECT a.DATA_TYPE FROM ALL_ARGUMENTS a WHERE a.OWNER=p.OWNER AND a.OBJECT_ID=p.OBJECT_ID AND a.SUBPROGRAM_ID=p.SUBPROGRAM_ID AND a.POSITION=0 AND a.DATA_LEVEL=0)"}
	case metadata.MetadataRoutineParameters:
		s.from = "ALL_ARGUMENTS a"
		s.schema = "a.OWNER"
		s.name = "a.OBJECT_NAME"
		s.column = "a.ARGUMENT_NAME"
		s.order = "a.OWNER,a.OBJECT_ID,a.SUBPROGRAM_ID,a.SEQUENCE"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "TO_CHAR(a.OBJECT_ID)||':'||TO_CHAR(a.SUBPROGRAM_ID)", "a.PACKAGE_NAME", s.column, "a.POSITION", "CASE WHEN a.POSITION=0 AND a.DATA_LEVEL=0 THEN 'RETURN' WHEN a.IN_OUT='IN/OUT' THEN 'INOUT' ELSE a.IN_OUT END", "a.DATA_TYPE", "a.DATA_LENGTH", "a.DATA_PRECISION", "a.DATA_SCALE", "a.TYPE_OWNER", "a.TYPE_NAME", "a.DATA_LEVEL"}
	case metadata.MetadataTypes:
		s.from = "ALL_TYPES t"
		s.schema = "t.OWNER"
		s.name = "t.TYPE_NAME"
		s.order = "t.OWNER,t.TYPE_NAME"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "t.TYPECODE", "t.ATTRIBUTES", "CASE t.FINAL WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END", "CASE t.INSTANTIABLE WHEN 'YES' THEN 1 WHEN 'NO' THEN 0 END"}
	case metadata.MetadataTypeAttributes:
		s.from = "ALL_TYPE_ATTRS a"
		s.schema = "a.OWNER"
		s.name = "a.TYPE_NAME"
		s.column = "a.ATTR_NAME"
		s.order = "a.OWNER,a.TYPE_NAME,a.ATTR_NO"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, s.column, "a.ATTR_NO", "a.ATTR_TYPE_NAME", "a.ATTR_TYPE_OWNER", "a.LENGTH", "a.NUMBER_PRECISION", "a.SCALE"}
	case metadata.MetadataCollectionElements:
		s.from = "ALL_COLL_TYPES c"
		s.schema = "c.OWNER"
		s.name = "c.TYPE_NAME"
		s.order = "c.OWNER,c.TYPE_NAME"
		s.expressions = []string{sqlNullLiteral, s.schema, s.name, "c.COLL_TYPE", "c.UPPER_BOUND", "c.ELEM_TYPE_NAME", "c.ELEM_TYPE_OWNER", "c.LENGTH", "c.NUMBER_PRECISION", "c.SCALE"}
	case metadata.MetadataServerInfo:
		s.from = metadataDual
		s.expressions = []string{"ob_version()", "'ORACLE'", "1", "1"}
	default:
		return Query{}, fmt.Errorf("%w: metadata kind %d in Oracle compatibility mode", dberrors.ErrUnsupported, f.Kind)
	}
	return buildMetadataQuery(config.ModeOracle, f, s)
}
