package valuecodec

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/types"
)

// ComplexDriverValue encodes a decoded complex value in the shared format-1 representation.
func ComplexDriverValue(value any, null bool) (driver.Value, error) {
	if null {
		return nil, nil
	}
	var document valueformat.ComplexJSON
	switch v := value.(type) {
	case types.Object:
		if !v.Null {
			return v.Value()
		}
		if !valueformat.Identifier(v.Type) || v.Schema != "" && !valueformat.Identifier(v.Schema) || len(v.Fields) != 0 {
			return nil, dberrors.ErrComplex
		}
		document = valueformat.ComplexJSON{Format: 1, Kind: valueformat.ComplexObject, Schema: v.Schema, Type: v.Type, Version: v.Version, Null: true}
	case types.Array:
		if !v.Null {
			return v.Value()
		}
		if v.Type != "" && !valueformat.Identifier(v.Type) || v.Schema != "" && !valueformat.Identifier(v.Schema) || len(v.Elements) != 0 {
			return nil, dberrors.ErrComplex
		}
		document = valueformat.ComplexJSON{Format: 1, Kind: valueformat.ComplexArray, Schema: v.Schema, Type: v.Type, Version: v.Version, Null: true, ElementType: v.ElementType, ElementVersion: v.ElementVersion}
	default:
		return nil, dberrors.ErrComplex
	}
	data, err := json.Marshal(document)
	if err != nil || len(data) > valueformat.MaxComplexJSON {
		return nil, errors.Join(dberrors.ErrComplex, err)
	}
	return string(data), nil
}
