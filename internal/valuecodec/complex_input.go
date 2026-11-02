package valuecodec

import (
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/types"
)

// ComplexInputValues validates descriptor shape and returns the ordered input members.
func ComplexInputValues(d *Descriptor, value any) (string, string, []any, error) {
	var values []any
	var schema, name string
	switch v := value.(type) {
	case types.Object:
		if v.Null || d.Array || len(v.Fields) != len(d.Fields) {
			return "", "", nil, dberrors.ErrComplex
		}
		schema, name = v.Schema, v.Type
		values = make([]any, len(v.Fields))
		for i, f := range v.Fields {
			if f.Name != d.Fields[i].Name || f.Type != "" && f.Type != d.Fields[i].Type {
				return "", "", nil, dberrors.ErrComplex
			}
			values[i] = f.Value
		}
	case types.Array:
		if v.Null || !d.Array || v.ElementType != "" && v.ElementType != d.Fields[0].Type {
			return "", "", nil, dberrors.ErrComplex
		}
		schema, name, values = v.Schema, v.Type, v.Elements
	default:
		return "", "", nil, dberrors.ErrComplex
	}
	return schema, name, values, nil
}
