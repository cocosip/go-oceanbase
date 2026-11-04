package catalog

import (
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/metadata"
)

// The projections below are pinned against the OceanBase dictionary docs because
// several Oracle-visible views use OceanBase spellings (ALL_TAB_PRIVS exposes
// TABLE_SCHEMA, ALL_TYPE_ATTRS/ALL_COLL_TYPES expose NUMBER_PRECISION) that
// differ from Oracle's. Privilege projections are pinned in
// metadata_privilege_test.go.
func TestOracleDictionaryShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind metadata.MetadataKind
		want []string
	}{
		{"schemas", metadata.MetadataSchemas, []string{"FROM ALL_USERS u", `u.USERNAME AS "schema"`}},
		{"tables", metadata.MetadataTables, []string{"FROM ALL_OBJECTS o LEFT JOIN ALL_TAB_COMMENTS c", "c.TABLE_TYPE=o.OBJECT_TYPE", "o.SUBOBJECT_NAME IS NULL"}},
		{"synonyms", metadata.MetadataSynonyms, []string{"FROM ALL_SYNONYMS s", `s.TABLE_OWNER AS "target_schema"`, `s.DB_LINK AS "database_link"`}},
		{"columns", metadata.MetadataColumns, []string{"FROM ALL_TAB_COLS a", "ROW_NUMBER() OVER(PARTITION BY a.OWNER,a.TABLE_NAME", "c.CHAR_USED='C' THEN c.CHAR_LENGTH", `c.DATA_TYPE_OWNER AS "type_owner"`, "'%SEQUENCE.NEXTVAL%'", "c.VIRTUAL_COLUMN WHEN 'YES'"}},
		{"primary-keys", metadata.MetadataPrimaryKeys, []string{"FROM ALL_CONSTRAINTS k JOIN ALL_CONS_COLUMNS c", "k.CONSTRAINT_TYPE='P'"}},
		{"foreign-keys", metadata.MetadataForeignKeys, []string{"r.OWNER=k.R_OWNER", "k.CONSTRAINT_TYPE='R'", `k.DELETE_RULE AS "delete_rule"`}},
		{"indexes", metadata.MetadataIndexes, []string{"FROM ALL_INDEXES i JOIN ALL_IND_COLUMNS c", `c.DESCEND AS "ordering"`, `i.DISTINCT_KEYS AS "cardinality"`, `i.INDEX_TYPE AS "type"`}},
		{"routines", metadata.MetadataRoutines, []string{"FROM ALL_PROCEDURES p", "COALESCE(p.PROCEDURE_NAME,p.OBJECT_NAME)", "ALL_ARGUMENTS a WHERE a.OWNER=p.OWNER", `AS "return_type"`}},
		{"routine-parameters", metadata.MetadataRoutineParameters, []string{"FROM ALL_ARGUMENTS a", "a.IN_OUT='IN/OUT' THEN 'INOUT'", `a.DATA_LEVEL AS "data_level"`}},
		{"types", metadata.MetadataTypes, []string{"FROM ALL_TYPES t", `t.ATTRIBUTES AS "attribute_count"`, "t.FINAL WHEN 'YES'"}},
		{"type-attributes", metadata.MetadataTypeAttributes, []string{"FROM ALL_TYPE_ATTRS a", `a.NUMBER_PRECISION AS "precision"`, `a.ATTR_TYPE_OWNER AS "type_owner"`}},
		{"collection-elements", metadata.MetadataCollectionElements, []string{"FROM ALL_COLL_TYPES c", `c.COLL_TYPE AS "collection_type"`, `c.UPPER_BOUND AS "upper_bound"`}},
		{"server-info", metadata.MetadataServerInfo, []string{`ob_version() AS "oceanbase_version"`, `'ORACLE' AS "compatibility_mode"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, err := OraclePlan(metadata.MetadataFilter{Kind: tc.kind})
			if err != nil {
				t.Fatal(err)
			}
			if len(query.Columns) == 0 {
				t.Fatal("projection lost its column list")
			}
			for _, want := range tc.want {
				if !strings.Contains(query.SQL, want) {
					t.Fatalf("SQL lost %q: %s", want, query.SQL)
				}
			}
		})
	}
}

func TestMySQLDictionaryShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind metadata.MetadataKind
		want []string
	}{
		{"catalogs", metadata.MetadataCatalogs, []string{`'def' AS "catalog" FROM dual`}},
		{"schemas", metadata.MetadataSchemas, []string{"FROM information_schema.SCHEMATA s", `s.SCHEMA_NAME AS "schema"`}},
		{"tables", metadata.MetadataTables, []string{"CASE t.TABLE_TYPE WHEN 'BASE TABLE' THEN 'TABLE' WHEN 'SYSTEM VIEW' THEN 'VIEW'"}},
		{"columns", metadata.MetadataColumns, []string{`c.CHARACTER_MAXIMUM_LENGTH AS "length"`, "c.DATETIME_PRECISION ELSE c.NUMERIC_SCALE", "LIKE '%auto_increment%'", "%STORED GENERATED%"}},
		{"primary-keys", metadata.MetadataPrimaryKeys, []string{"FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.TABLE_CONSTRAINTS t", "t.CONSTRAINT_TYPE='PRIMARY KEY'"}},
		{"foreign-keys", metadata.MetadataForeignKeys, []string{"JOIN information_schema.REFERENTIAL_CONSTRAINTS r", `k.REFERENCED_COLUMN_NAME AS "referenced_column"`}},
		{"indexes", metadata.MetadataIndexes, []string{"FROM information_schema.STATISTICS i", "i.COLLATION WHEN 'A' THEN 'ASC'"}},
		{"routines", metadata.MetadataRoutines, []string{"FROM information_schema.ROUTINES r", `r.SPECIFIC_NAME AS "specific_name"`}},
		{"routine-parameters", metadata.MetadataRoutineParameters, []string{"JOIN information_schema.ROUTINES r ON r.SPECIFIC_NAME=p.SPECIFIC_NAME", "p.ORDINAL_POSITION=0 THEN 'RETURN'"}},
		{"server-info", metadata.MetadataServerInfo, []string{`'MYSQL' AS "compatibility_mode"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, err := MySQLPlan("5.7.0", metadata.MetadataFilter{Kind: tc.kind}, false)
			if err != nil {
				t.Fatal(err)
			}
			if len(query.Columns) == 0 {
				t.Fatal("projection lost its column list")
			}
			for _, want := range tc.want {
				if !strings.Contains(query.SQL, want) {
					t.Fatalf("SQL lost %q: %s", want, query.SQL)
				}
			}
		})
	}
}
