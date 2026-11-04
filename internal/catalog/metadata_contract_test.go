package catalog

import (
	"strings"
	"testing"

	"github.com/cocosip/go-oceanbase/metadata"
)

// Column lists are the public Metadata schema contract shared by both
// compatibility modes; a rename or a numeric-flag change is a breaking change
// and must fail here before it reaches callers such as database/sql scans.
func TestMetadataColumnContract(t *testing.T) {
	want := map[metadata.MetadataKind]string{
		metadata.MetadataCatalogs:            "catalog",
		metadata.MetadataSchemas:             "catalog schema",
		metadata.MetadataTables:              "catalog schema name type remarks",
		metadata.MetadataSynonyms:            "catalog schema name target_schema target_name database_link",
		metadata.MetadataColumns:             "catalog schema table_name name #ordinal_position data_type #length #precision #scale #nullable default_value remarks type_owner #auto_increment #generated",
		metadata.MetadataPrimaryKeys:         "catalog schema table_name name column_name #ordinal_position",
		metadata.MetadataForeignKeys:         "catalog schema table_name name column_name #ordinal_position referenced_catalog referenced_schema referenced_table referenced_column update_rule delete_rule deferrable initially_deferred",
		metadata.MetadataIndexes:             "catalog schema table_name name #non_unique column_name #ordinal_position ordering #cardinality type",
		metadata.MetadataTablePrivileges:     "catalog schema table_name grantor grantee privilege #grantable",
		metadata.MetadataColumnPrivileges:    "catalog schema table_name column_name grantor grantee privilege #grantable",
		metadata.MetadataRoutines:            "catalog schema name specific_name package_name type return_type",
		metadata.MetadataRoutineParameters:   "catalog schema routine_name specific_name package_name name #ordinal_position direction data_type #length #precision #scale type_owner type_name #data_level",
		metadata.MetadataTypes:               "catalog schema name type #attribute_count #final #instantiable",
		metadata.MetadataTypeAttributes:      "catalog schema type_name name #ordinal_position data_type type_owner #length #precision #scale",
		metadata.MetadataCollectionElements:  "catalog schema name collection_type #upper_bound element_type element_owner #length #precision #scale",
		metadata.MetadataServerInfo:          "oceanbase_version compatibility_mode #parameter_dictionary #type_dictionary",
		metadata.MetadataStatementParameters: "#physical_ordinal #logical_ordinal name direction data_type #nullable #length #precision #scale #protocol_type #protocol_direction",
		metadata.MetadataTypeInfo:            "type_name category #precision #minimum_scale #maximum_scale #radix literal_prefix literal_suffix create_parameters #auto_increment",
	}
	for kind, spec := range want {
		columns := Columns(kind)
		if got := formatColumns(columns); got != spec {
			t.Fatalf("kind %d columns changed: got %q want %q", kind, got, spec)
		}
		seen := map[string]bool{}
		for _, column := range columns {
			if column.Name == "" || seen[column.Name] {
				t.Fatalf("kind %d has an empty or duplicate column %q", kind, column.Name)
			}
			seen[column.Name] = true
		}
	}
}

func formatColumns(columns []Column) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		name := column.Name
		if column.Numeric {
			name = "#" + name
		}
		parts[i] = name
	}
	return strings.Join(parts, " ")
}
