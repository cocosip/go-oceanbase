package oceanbase

import (
	context "context"
	sql "database/sql"
	fmt "fmt"
	metadata "github.com/cocosip/go-oceanbase/metadata"
)

// MetadataKind selects one fixed metadata result schema.
type MetadataKind = metadata.MetadataKind

// Metadata kinds. Views are selected through MetadataTables and TableTypes.
const (
	MetadataCatalogs            = metadata.MetadataCatalogs
	MetadataSchemas             = metadata.MetadataSchemas
	MetadataTables              = metadata.MetadataTables
	MetadataSynonyms            = metadata.MetadataSynonyms
	MetadataColumns             = metadata.MetadataColumns
	MetadataPrimaryKeys         = metadata.MetadataPrimaryKeys
	MetadataForeignKeys         = metadata.MetadataForeignKeys
	MetadataIndexes             = metadata.MetadataIndexes
	MetadataTablePrivileges     = metadata.MetadataTablePrivileges
	MetadataColumnPrivileges    = metadata.MetadataColumnPrivileges
	MetadataRoutines            = metadata.MetadataRoutines
	MetadataRoutineParameters   = metadata.MetadataRoutineParameters
	MetadataTypes               = metadata.MetadataTypes
	MetadataTypeAttributes      = metadata.MetadataTypeAttributes
	MetadataCollectionElements  = metadata.MetadataCollectionElements
	MetadataServerInfo          = metadata.MetadataServerInfo
	MetadataStatementParameters = metadata.MetadataStatementParameters
	MetadataTypeInfo            = metadata.MetadataTypeInfo
)

// MetadataFilter selects a fixed metadata result schema and its filters.
type MetadataFilter = metadata.MetadataFilter

type metadataRequest struct{ filter MetadataFilter }

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
