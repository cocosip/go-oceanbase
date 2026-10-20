package valuecodec

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// EncodeComplexScalar encodes one native scalar field including its complete framing budget.
func EncodeComplexScalar(typ string, value any, options config.SessionOptions, maxLOBBytes, limit int64) (wire.Parameter, error) {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric:
		var text string
		switch v := value.(type) {
		case types.Number:
			text = string(v)
		case string:
			text = v
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			text = fmt.Sprint(v)
		default:
			return wire.Parameter{}, dberrors.ErrComplex
		}
		text, err := valueformat.PlainNumber(text, limit)
		if err != nil {
			return wire.Parameter{}, err
		}
		return complexParameter([]byte(text), true, limit)
	case valueformat.TypeNameRaw:
		var data []byte
		switch v := value.(type) {
		case types.Raw:
			data = v
		case []byte:
			data = v
		default:
			return wire.Parameter{}, dberrors.ErrComplex
		}
		if int64(len(data)) > maxLOBBytes {
			return wire.Parameter{}, wire.ErrMessageTooLarge
		}
		return complexParameter(data, true, limit)
	case valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameNChar:
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case types.NString:
			text = string(v)
		default:
			return wire.Parameter{}, dberrors.ErrComplex
		}
		if int64(utf8.RuneCountInString(text)) > limit {
			return wire.Parameter{}, wire.ErrMessageTooLarge
		}
		name := options.Charset
		if typ == valueformat.TypeNameNVarchar2 || typ == valueformat.TypeNameNChar {
			name = options.NCharset
			if name == "" {
				name = options.Charset
			}
		}
		if name == "" {
			name = charsetUTF8MB4
		}
		data, err := charset.Encode(name, text)
		if err != nil {
			return wire.Parameter{}, err
		}
		return complexParameter(data, true, limit)
	case valueformat.TypeNameDate:
		var wall time.Time
		switch v := value.(type) {
		case time.Time:
			wall = v
		case string:
			var err error
			wall, _, err = valueformat.ParseWall(v)
			if err != nil {
				return wire.Parameter{}, err
			}
		default:
			return wire.Parameter{}, dberrors.ErrComplex
		}
		if wall.Nanosecond() != 0 {
			return wire.Parameter{}, dberrors.ErrComplex
		}
		options.Location = wall.Location()
		p, err := EncodeParameter(wall, config.ModeMySQL, options, limit)
		if err != nil {
			return wire.Parameter{}, err
		}
		return complexParameter(p.Data, false, limit)
	default:
		return wire.Parameter{}, fmt.Errorf("%w: native complex input field %s", dberrors.ErrUnsupported, typ)
	}
}

func complexParameter(data []byte, lengthEncoded bool, limit int64) (wire.Parameter, error) {
	p := wire.Parameter{Data: data, LengthEncoded: lengthEncoded}
	if p.EncodedSize() > limit {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	return p, nil
}

// ComplexElementWireType maps supported native collection elements to wire types.
func ComplexElementWireType(typ string) byte {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric:
		return 0
	case valueformat.TypeNameDate:
		return 12
	case valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar:
		return 15
	case valueformat.TypeNameChar:
		return 254
	case valueformat.TypeNameRaw:
		return 203
	}
	return 255
}

// ComplexBuiltinType reports whether a dictionary name denotes a builtin scalar.
func ComplexBuiltinType(typ string) bool {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameNumeric, valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameNChar, valueformat.TypeNameRaw, valueformat.TypeNameDate, valueformat.TypeNameTimestamp, valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return true
	}
	return false
}

// ComplexCharset chooses the session charset for a complex scalar field.
func ComplexCharset(typ string, options config.SessionOptions) string {
	name := options.Charset
	if typ == valueformat.TypeNameNChar || typ == valueformat.TypeNameNVarchar2 || typ == valueformat.TypeNameNClob {
		name = options.NCharset
		if name == "" {
			name = options.Charset
		}
	}
	if name == "" {
		name = charsetUTF8MB4
	}
	return name
}

// EncodeComplexMetadata serializes a resolved descriptor's parameter type metadata.
func EncodeComplexMetadata(d *Descriptor, options config.SessionOptions, sqlLimit, limit int64) ([]byte, error) {
	schemaName, typeName := d.Key.Schema, d.Key.Name
	if d.Array {
		schemaName, typeName = "", ""
	}
	schema, err := encodeMetadataName(schemaName, options, sqlLimit)
	if err != nil {
		return nil, err
	}
	name, err := encodeMetadataName(typeName, options, sqlLimit)
	if err != nil {
		return nil, err
	}
	metadata := wire.AppendComplexLenBytes(nil, schema)
	metadata = wire.AppendComplexLenBytes(metadata, name)
	metadata = wire.AppendComplexLenEnc(metadata, d.Key.Version)
	if d.Array {
		field := d.Fields[0]
		if field.Child == nil {
			elementType := ComplexElementWireType(field.Type)
			if elementType == 255 {
				return nil, dberrors.ErrUnsupported
			}
			metadata = append(metadata, elementType)
		} else {
			metadata = append(metadata, 160)
			schema, err = encodeMetadataName(field.Child.Key.Schema, options, sqlLimit)
			if err != nil {
				return nil, err
			}
			name, err = encodeMetadataName(field.Child.Key.Name, options, sqlLimit)
			if err != nil {
				return nil, err
			}
			metadata = wire.AppendComplexLenBytes(metadata, schema)
			metadata = wire.AppendComplexLenBytes(metadata, name)
			metadata = wire.AppendComplexLenEnc(metadata, field.Child.Key.Version)
		}
	}
	if int64(len(metadata)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	return metadata, nil
}

func encodeMetadataName(name string, options config.SessionOptions, limit int64) ([]byte, error) {
	if int64(utf8.RuneCountInString(name)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	if !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return nil, fmt.Errorf("%w: invalid SQL text", dberrors.ErrBind)
	}
	encoding := options.Charset
	if encoding == "" {
		encoding = charsetUTF8MB4
	}
	data, err := charset.Encode(encoding, name)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	return data, nil
}

// DecodeComplexScalar interprets a buffered native scalar; LOBs are materialized upstream.
func DecodeComplexScalar(typ string, data []byte, options config.SessionOptions) (any, error) {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameNumeric:
		n := types.Number(string(data))
		_, err := n.Value()
		return n, err
	case valueformat.TypeNameRaw:
		return types.Raw(append([]byte{}, data...)), nil
	case valueformat.TypeNameDate:
		return DecodeBinaryValue(wire.Column{Type: 12}, config.ModeOracle, data)
	case valueformat.TypeNameTimestamp:
		return DecodeOracleTemporal(202, data, options.Location)
	case valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return nil, dberrors.ErrUnsupported
	default:
		return charset.Decode(ComplexCharset(typ, options), data)
	}
}
