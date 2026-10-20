package catalog

import (
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

// MetadataColumns supplies mode-independent names, scan types and nullability.
func MetadataColumns(kind metadata.MetadataKind) []wire.Column {
	columns := Columns(kind)
	projection := make([]wire.Column, len(columns))
	for i, column := range columns {
		projection[i] = wire.Column{Name: column.Name, Type: 253, Charset: 45}
		if column.Numeric {
			projection[i].Type, projection[i].Charset = 8, 63
		}
	}
	return projection
}
