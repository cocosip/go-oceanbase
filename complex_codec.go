package oceanbase

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func (c *conn) encodeParameterLocked(ctx context.Context, value any, limit int64) (wire.Parameter, error) {
	switch v := value.(type) {
	case Object:
		return c.encodeComplexParameterLocked(ctx, v, v.Null, descriptorKey{c.session.mode, v.Schema, v.Type, v.Version}, limit)
	case Array:
		return c.encodeComplexParameterLocked(ctx, v, v.Null, descriptorKey{c.session.mode, v.Schema, v.Type, v.Version}, limit)
	}
	return encodeParameter(value, c.session.mode, c.cfg.Session, limit)
}

func (c *conn) encodeComplexParameterLocked(ctx context.Context, value any, null bool, key descriptorKey, limit int64) (wire.Parameter, error) {
	if c.session.mode != ModeOracle {
		return wire.Parameter{}, ErrUnsupported
	}
	if null {
		return wire.Parameter{Type: 6}, nil
	}
	var d *typeDescriptor
	var err error
	if key.name == "" {
		a, ok := value.(Array)
		if !ok {
			return wire.Parameter{}, ErrComplex
		}
		column := wire.Column{ComplexSchema: a.Schema, ComplexVersion: a.Version, ElementType: complexElementWireType(a.ElementType)}
		if column.ElementType == 255 {
			if complexBuiltinType(a.ElementType) {
				return wire.Parameter{}, ErrUnsupported
			}
			element, e := c.descriptorLocked(ctx, descriptorKey{ModeOracle, a.Schema, a.ElementType, a.ElementVersion}, 0, make(map[descriptorKey]bool))
			err = e
			if err == nil {
				d = anonymousComplexDescriptor(a.Version, element)
			}
		} else {
			d, err = anonymousDescriptor(column)
		}
	} else {
		d, err = c.descriptorLocked(ctx, key, 0, make(map[descriptorKey]bool))
	}
	if err != nil {
		return wire.Parameter{}, complexDescriptorError(key.name, err)
	}
	schemaName, typeName := d.key.schema, d.key.name
	if d.array {
		schemaName, typeName = "", ""
	}
	schema, err := c.encodeSQL(schemaName)
	if err != nil {
		return wire.Parameter{}, err
	}
	name, err := c.encodeSQL(typeName)
	if err != nil {
		return wire.Parameter{}, err
	}
	metadata := complexLenBytes(nil, schema)
	metadata = complexLenBytes(metadata, name)
	metadata = complexLenEnc(metadata, d.key.version)
	if d.array {
		field := d.fields[0]
		if field.child == nil {
			elementType := complexElementWireType(field.typ)
			if elementType == 255 {
				return wire.Parameter{}, ErrUnsupported
			}
			metadata = append(metadata, elementType)
		} else {
			metadata = append(metadata, 160)
			schema, err = c.encodeSQL(field.child.key.schema)
			if err != nil {
				return wire.Parameter{}, err
			}
			name, err = c.encodeSQL(field.child.key.name)
			if err != nil {
				return wire.Parameter{}, err
			}
			metadata = complexLenBytes(metadata, schema)
			metadata = complexLenBytes(metadata, name)
			metadata = complexLenEnc(metadata, field.child.key.version)
		}
	}
	if int64(len(metadata)) > limit {
		return wire.Parameter{}, wire.ErrMessageTooLarge
	}
	e := complexEncoder{conn: c, ctx: ctx, limit: limit - int64(len(metadata)), nodes: maxComplexNodes}
	if err = e.value(d, value, 0, false); err != nil {
		return wire.Parameter{}, err
	}
	return wire.Parameter{Type: 160, TypeMetadata: metadata, Data: e.data}, nil
}

func complexElementWireType(typ string) byte {
	switch strings.ToUpper(typ) {
	case typeNameNumber, typeNameInteger, typeNameDecimal, typeNameNumeric:
		return 0
	case typeNameDate:
		return 12
	case typeNameVarchar2, typeNameVarchar:
		return 15
	case typeNameChar:
		return 254
	case typeNameRaw:
		return 203
	}
	return 255
}

func complexLenEnc(data []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(data, byte(n))
	case n <= 65535:
		return append(data, 252, byte(n), byte(n>>8))
	case n <= 16777215:
		return append(data, 253, byte(n), byte(n>>8), byte(n>>16))
	default:
		return binary.LittleEndian.AppendUint64(append(data, 254), n)
	}
}
func complexLenBytes(data, value []byte) []byte {
	return append(complexLenEnc(data, uint64(len(value))), value...)
}

type complexEncoder struct {
	conn  *conn
	ctx   context.Context
	data  []byte
	limit int64
	nodes int
}

func (e *complexEncoder) append(data []byte, lengthEncoded bool) error {
	size := int64(len(data))
	if lengthEncoded {
		size = (wire.Parameter{Data: data, LengthEncoded: true}).EncodedSize()
	}
	if size > e.limit-int64(len(e.data)) {
		return wire.ErrMessageTooLarge
	}
	if lengthEncoded {
		e.data = complexLenBytes(e.data, data)
	} else {
		e.data = append(e.data, data...)
	}
	return nil
}

func (e *complexEncoder) value(d *typeDescriptor, value any, depth int, checkName bool) error {
	if err := complexNode(depth, &e.nodes); err != nil {
		return err
	}
	schema, name, values, err := complexInputValues(d, value)
	if err != nil {
		return err
	}
	if checkName && (schema != "" && schema != d.key.schema || name != d.key.name) {
		resolved, err := e.conn.descriptorLocked(e.ctx, descriptorKey{ModeOracle, schema, name, 0}, depth, make(map[descriptorKey]bool))
		if err != nil {
			return err
		}
		if resolved.key.schema != d.key.schema || resolved.key.name != d.key.name {
			return ErrComplex
		}
	}
	if len(values) > e.nodes {
		return ErrComplex
	}
	if d.array {
		if err := e.append(complexLenEnc(nil, uint64(len(values))), false); err != nil {
			return err
		}
	}
	bitmap := len(e.data)
	if err := e.append(make([]byte, (len(values)+7)/8), false); err != nil {
		return err
	}
	for i, value := range values {
		null := value == nil
		switch v := value.(type) {
		case Object:
			null = v.Null
		case Array:
			null = v.Null
		}
		if null {
			e.data[bitmap+i/8] |= 1 << uint(i%8)
			e.nodes--
			continue
		}
		index := i
		if d.array {
			index = 0
		}
		field := d.fields[index]
		var err error
		if field.child != nil {
			err = e.value(field.child, value, depth+1, true)
		} else {
			err = e.scalar(field.typ, value, depth+1)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *complexEncoder) scalar(typ string, value any, depth int) error {
	if err := complexNode(depth, &e.nodes); err != nil {
		return err
	}
	limit := e.limit - int64(len(e.data))
	switch strings.ToUpper(typ) {
	case typeNameNumber, typeNameInteger, typeNameDecimal, typeNameNumeric:
		var text string
		switch v := value.(type) {
		case Number:
			text = string(v)
		case string:
			text = v
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			text = fmt.Sprint(v)
		default:
			return ErrComplex
		}
		text, err := plainNumber(text, limit)
		if err != nil {
			return err
		}
		return e.append([]byte(text), true)
	case typeNameRaw:
		var data []byte
		switch v := value.(type) {
		case Raw:
			data = v
		case []byte:
			data = v
		default:
			return ErrComplex
		}
		if int64(len(data)) > e.conn.cfg.Limits.MaxLOBBytes {
			return wire.ErrMessageTooLarge
		}
		return e.append(data, true)
	case typeNameVarchar2, typeNameVarchar, typeNameChar, typeNameNVarchar2, typeNameNChar:
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case NString:
			text = string(v)
		default:
			return ErrComplex
		}
		if int64(len(text)) > limit {
			return wire.ErrMessageTooLarge
		}
		name := e.conn.cfg.Session.Charset
		if typ == typeNameNVarchar2 || typ == typeNameNChar {
			name = e.conn.cfg.Session.NCharset
			if name == "" {
				name = e.conn.cfg.Session.Charset
			}
		}
		if name == "" {
			name = charsetUTF8MB4
		}
		data, err := charset.Encode(name, text)
		if err != nil {
			return err
		}
		return e.append(data, true)
	case typeNameDate:
		var wall time.Time
		switch v := value.(type) {
		case time.Time:
			wall = v
		case string:
			var err error
			wall, _, err = parseWall(v)
			if err != nil {
				return err
			}
		default:
			return ErrComplex
		}
		if wall.Nanosecond() != 0 {
			return ErrComplex
		}
		options := e.conn.cfg.Session
		options.Location = wall.Location()
		p, err := encodeParameter(wall, ModeMySQL, options, limit)
		if err != nil {
			return err
		}
		return e.append(p.Data, false)
	default:
		return fmt.Errorf("%w: native complex input field %s", ErrUnsupported, typ)
	}
}

func complexInputValues(d *typeDescriptor, value any) (string, string, []any, error) {
	var values []any
	var schema, name string
	switch v := value.(type) {
	case Object:
		if v.Null || d.array || len(v.Fields) != len(d.fields) {
			return "", "", nil, ErrComplex
		}
		schema, name = v.Schema, v.Type
		values = make([]any, len(v.Fields))
		for i, f := range v.Fields {
			if f.Name != d.fields[i].name || f.Type != "" && f.Type != d.fields[i].typ {
				return "", "", nil, ErrComplex
			}
			values[i] = f.Value
		}
	case Array:
		if v.Null || !d.array || v.ElementType != "" && v.ElementType != d.fields[0].typ {
			return "", "", nil, ErrComplex
		}
		schema, name, values = v.Schema, v.Type, v.Elements
	default:
		return "", "", nil, ErrComplex
	}
	return schema, name, values, nil
}
