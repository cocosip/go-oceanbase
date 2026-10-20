// Package metadata defines the driver metadata request contracts.
package metadata

// MetadataKind selects one fixed metadata result schema, documented in the architecture.
type MetadataKind uint8 //nolint:revive // Preserve the established public metadata contract name.

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
	MetadataTypeInfo
)

// MetadataFilter uses case-preserving LIKE patterns with '/' as the escape.
// Empty patterns select all accessible objects. Catalog is an exact match.
// Query is used only for MetadataStatementParameters and is prepared, never executed.
type MetadataFilter struct { //nolint:revive // Preserve the established public metadata contract name.
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
