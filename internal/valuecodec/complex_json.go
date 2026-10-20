package valuecodec

import (
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/types"
	"strings"
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

// ScanComplex parses the shared format-1 complex representation.
func ScanComplex(src any) (any, error) {
	switch src.(type) {
	case string, []byte:
	default:
		return nil, dberrors.ErrComplex
	}
	var object types.Object
	if err := object.Scan(src); err == nil {
		return object, nil
	}
	var array types.Array
	if err := array.Scan(src); err != nil {
		return nil, err
	}
	return array, nil
}

// ParseComplexScalar parses one scalar from complex JSON without session I/O.
func ParseComplexScalar(typ, text string) (any, error) {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameNumeric:
		n := types.Number(text)
		_, err := n.Value()
		return n, err
	case valueformat.TypeNameRaw, valueformat.TypeNameBlob:
		b, err := base64.StdEncoding.DecodeString(text)
		return types.Raw(b), err
	case valueformat.TypeNameDate, valueformat.TypeNameTimestamp:
		_, _, err := valueformat.ParseWall(text)
		return text, err
	case "", valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return text, nil
	}
	return nil, dberrors.ErrComplex
}
