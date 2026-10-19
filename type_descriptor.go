package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type descriptorKey struct {
	mode         Mode
	schema, name string
	version      uint64
}

// Descriptors are scoped to one command on one physical session. Clearing them
// at command boundaries avoids assuming privileges, synonyms or DDL stay unchanged.
type typeDescriptor struct {
	key    descriptorKey
	array  bool
	fields []descriptorField
	shape  *wire.ComplexShape
}

type descriptorField struct {
	name, typ string
	child     *typeDescriptor
}

func (c *conn) clearDescriptors() { c.descriptors = nil; c.descriptorBytes = 0 }

func exactDescriptorPattern(name string) string {
	return strings.NewReplacer("/", "//", "%", "/%", "_", "/_").Replace(name)
}

func (c *conn) descriptorMetadataLocked(ctx context.Context, kind MetadataKind, schema, name string) ([][]driver.Value, error) {
	r, err := c.metadataLocked(ctx, MetadataFilter{Kind: kind, SchemaPattern: exactDescriptorPattern(schema), NamePattern: exactDescriptorPattern(name)}, false)
	if err != nil {
		return nil, err
	}
	r.holdRelease = true
	sets, err := r.collectLOBResponse(ctx)
	r.holdRelease = false
	if err != nil {
		return nil, r.fail(err, true)
	}
	r.release()
	if r.err != nil {
		return nil, r.err
	}
	if len(sets) != 1 {
		return nil, ErrComplex
	}
	if r.bufferBytes > c.cfg.Limits.MaxMessageBytes-c.descriptorBytes {
		return nil, wire.ErrMessageTooLarge
	}
	c.descriptorBytes += r.bufferBytes
	return sets[0].rows, nil
}

func (c *conn) descriptorLocked(ctx context.Context, key descriptorKey, depth int, visiting map[descriptorKey]bool) (*typeDescriptor, error) {
	if c.responseActive {
		return nil, errResponseActive
	}
	if key.mode != ModeOracle {
		return nil, ErrUnsupported
	}
	if depth > maxComplexDepth || visiting[key] || !validBlobIdentifier(key.name) {
		return nil, ErrComplex
	}
	if key.schema == "" {
		sets, err := c.tableBlobQueryLocked(ctx, "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL", nil, false)
		if err != nil {
			return nil, err
		}
		v, err := tableBlobScalar(sets)
		if err != nil {
			return nil, err
		}
		key.schema = tableBlobText(v)
	}
	if !validBlobIdentifier(key.schema) {
		return nil, ErrComplex
	}
	if d := c.descriptors[key]; d != nil {
		return d, nil
	}
	if len(c.descriptors) >= maxTextColumns {
		return nil, wire.ErrMessageTooLarge
	}
	visiting[key] = true
	defer delete(visiting, key)
	rows, err := c.descriptorMetadataLocked(ctx, MetadataTypes, key.schema, key.name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return c.descriptorSynonymLocked(ctx, key, depth, visiting)
	}
	if len(rows) != 1 || tableBlobText(rows[0][1]) != key.schema || tableBlobText(rows[0][2]) != key.name {
		return nil, ErrComplex
	}
	d := &typeDescriptor{key: key, shape: &wire.ComplexShape{}}
	switch strings.ToUpper(tableBlobText(rows[0][3])) {
	case typeNameObject:
		if err := c.descriptorObjectLocked(ctx, d, rows[0], depth, visiting); err != nil {
			return nil, err
		}
	case "COLLECTION":
		d.array, d.shape.Array = true, true
		rows, e := c.descriptorMetadataLocked(ctx, MetadataCollectionElements, key.schema, key.name)
		if e != nil {
			return nil, e
		}
		if len(rows) != 1 {
			return nil, ErrComplex
		}
		f, e := c.descriptorFieldLocked(ctx, "", tableBlobText(rows[0][5]), tableBlobText(rows[0][6]), depth, visiting)
		if e != nil {
			return nil, e
		}
		d.fields = []descriptorField{f}
	default:
		return nil, ErrUnsupported
	}
	for _, field := range d.fields {
		var shape *wire.ComplexShape
		if field.child != nil {
			shape = field.child.shape
		}
		d.shape.Fields = append(d.shape.Fields, shape)
	}
	c.cacheDescriptor(key, d)
	return d, nil
}

func (c *conn) cacheDescriptor(key descriptorKey, d *typeDescriptor) {
	if c.descriptors == nil {
		c.descriptors = make(map[descriptorKey]*typeDescriptor)
	}
	c.descriptors[key] = d
}

func (c *conn) descriptorFieldLocked(ctx context.Context, name, typ, schema string, depth int, visiting map[descriptorKey]bool) (descriptorField, error) {
	f := descriptorField{name: name, typ: typ}
	if schema != "" && schema != "SYS" || !complexBuiltinType(typ) {
		var err error
		f.child, err = c.descriptorLocked(ctx, descriptorKey{c.session.mode, schema, typ, 0}, depth+1, visiting)
		return f, err
	}
	return f, nil
}

func complexBuiltinType(typ string) bool {
	switch strings.ToUpper(typ) {
	case typeNameNumber, typeNameDecimal, typeNameInteger, typeNameNumeric, typeNameVarchar2, typeNameVarchar, typeNameChar, typeNameNVarchar2, typeNameNChar, typeNameRaw, typeNameDate, typeNameTimestamp, typeNameBlob, typeNameClob, typeNameNClob:
		return true
	}
	return false
}

func anonymousDescriptor(column wire.Column) (*typeDescriptor, error) {
	var typ string
	switch column.ElementType {
	case 0, 3, 206, 246:
		typ = typeNameNumber
	case 12:
		typ = typeNameDate
	case 15, 253:
		typ = typeNameVarchar2
	case 254:
		typ = typeNameChar
	case 203:
		typ = typeNameRaw
	default:
		return nil, ErrUnsupported
	}
	return &typeDescriptor{key: descriptorKey{ModeOracle, column.ComplexSchema, "", column.ComplexVersion}, array: true, fields: []descriptorField{{typ: typ}}, shape: &wire.ComplexShape{Array: true, Fields: []*wire.ComplexShape{nil}}}, nil
}

func (c *conn) columnDescriptorLocked(ctx context.Context, column wire.Column) (*typeDescriptor, error) {
	if column.ComplexName == "" {
		if column.ElementType == 160 {
			element, err := c.descriptorLocked(ctx, descriptorKey{ModeOracle, column.ElementSchema, column.ElementName, column.ElementVersion}, 0, make(map[descriptorKey]bool))
			if err != nil {
				return nil, err
			}
			return anonymousComplexDescriptor(column.ComplexVersion, element), nil
		}
		return anonymousDescriptor(column)
	}
	if column.ElementType != 0 {
		return nil, ErrUnsupported
	}
	return c.descriptorLocked(ctx, descriptorKey{c.session.mode, column.ComplexSchema, column.ComplexName, column.ComplexVersion}, 0, make(map[descriptorKey]bool))
}

func anonymousComplexDescriptor(version uint64, element *typeDescriptor) *typeDescriptor {
	return &typeDescriptor{key: descriptorKey{ModeOracle, element.key.schema, "", version}, array: true, fields: []descriptorField{{typ: element.key.name, child: element}}, shape: &wire.ComplexShape{Array: true, Fields: []*wire.ComplexShape{element.shape}}}
}

func (c *conn) describeComplexColumnsLocked(ctx context.Context, columns []wire.Column) error {
	for i, column := range columns {
		if column.Type != 160 {
			continue
		}
		d, err := c.columnDescriptorLocked(ctx, column)
		if err != nil {
			return err
		}
		columns[i].ComplexShape = d.shape
	}
	return nil
}

func (c *conn) complexValueLocked(ctx context.Context, column wire.Column, data []byte) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	d, err := c.columnDescriptorLocked(ctx, column)
	if err != nil {
		return nil, err
	}
	v, n, err := wire.DecodeComplex(data, d.shape, maxComplexNodes)
	if err != nil || n != len(data) {
		// Only buffered bytes are parsed again. No business command is replayed.
		c.clearDescriptors()
		var e error
		d, e = c.columnDescriptorLocked(ctx, column)
		if e != nil {
			return nil, errors.Join(ErrComplex, err, e)
		}
		v, n, err = wire.DecodeComplex(data, d.shape, maxComplexNodes)
		if err != nil || n != len(data) {
			return nil, errors.Join(ErrComplex, err)
		}
	}
	budget := min(c.cfg.Limits.MaxMessageBytes, int64(maxComplexJSON))
	value, err := c.decodeComplexValueLocked(ctx, d, v, 0, &budget)
	if err != nil {
		return nil, err
	}
	encoded, err := complexDriverValue(value, false)
	if err == nil && int64(len(encoded.(string))) > c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	return encoded, err
}

func consumeComplexBudget(budget *int64, size int64) error {
	if size > *budget {
		return wire.ErrMessageTooLarge
	}
	*budget -= size
	return nil
}

func (c *conn) decodeComplexValueLocked(ctx context.Context, d *typeDescriptor, value wire.ComplexValue, depth int, budget *int64) (any, error) {
	if depth > maxComplexDepth {
		return nil, ErrComplex
	}
	if value.Null {
		return nil, nil
	}
	if err := consumeComplexBudget(budget, 128+int64(len(d.key.schema)+len(d.key.name))+int64(len(value.Children))*16); err != nil {
		return nil, err
	}
	if d.array {
		a := Array{Schema: d.key.schema, Type: d.key.name, Version: d.key.version, ElementType: d.fields[0].typ, Elements: make([]any, 0, len(value.Children))}
		if d.fields[0].child != nil {
			a.ElementVersion = d.fields[0].child.key.version
		}
		for _, v := range value.Children {
			e, err := c.decodeComplexFieldLocked(ctx, d.fields[0], v, depth+1, budget)
			if err != nil {
				return nil, err
			}
			a.Elements = append(a.Elements, e)
		}
		return a, nil
	}
	if len(value.Children) != len(d.fields) {
		return nil, ErrComplex
	}
	o := Object{Schema: d.key.schema, Type: d.key.name, Version: d.key.version, Fields: make([]Attribute, 0, len(d.fields))}
	for i, f := range d.fields {
		if err := consumeComplexBudget(budget, 64+int64(len(f.name)+len(f.typ))); err != nil {
			return nil, err
		}
		v, err := c.decodeComplexFieldLocked(ctx, f, value.Children[i], depth+1, budget)
		if err != nil {
			return nil, err
		}
		o.Fields = append(o.Fields, Attribute{f.name, f.typ, v})
	}
	return o, nil
}

func (c *conn) decodeComplexFieldLocked(ctx context.Context, f descriptorField, v wire.ComplexValue, depth int, budget *int64) (any, error) {
	if v.Null {
		return nil, nil
	}
	if f.child != nil {
		return c.decodeComplexValueLocked(ctx, f.child, v, depth, budget)
	}
	if err := consumeComplexBudget(budget, 16); err != nil {
		return nil, err
	}
	switch strings.ToUpper(f.typ) {
	case typeNameNumber, typeNameDecimal, typeNameInteger, typeNameNumeric:
		if err := consumeComplexBudget(budget, int64(len(v.Data))); err != nil {
			return nil, err
		}
		return parseComplexScalar(f.typ, string(v.Data))
	case typeNameRaw:
		if err := consumeComplexBudget(budget, 4*((int64(len(v.Data))+2)/3)+2); err != nil {
			return nil, err
		}
		return Raw(append([]byte{}, v.Data...)), nil
	case typeNameDate:
		return decodeBinaryValue(wire.Column{Type: 12}, ModeOracle, v.Data)
	case typeNameTimestamp:
		return decodeOracleTemporal(202, v.Data, c.cfg.Session.Location)
	case typeNameBlob, typeNameClob, typeNameNClob:
		column := wire.Column{Type: 210, Charset: 63}
		if f.typ != typeNameBlob {
			column.Type = 211
		}
		if f.typ == typeNameNClob {
			name := c.complexCharset(f.typ)
			if name == charsetUTF16 {
				column.Charset = 54
			} else {
				id, err := handshakeCharset(name)
				if err != nil {
					return nil, err
				}
				column.Charset = uint16(id)
			}
		}
		values := []driver.Value{v.Data}
		limit := *budget
		if column.Type == 210 {
			limit = max(0, limit-2) / 4 * 3
		}
		if err := c.materializeLOBValuesLocked(ctx, []wire.Column{column}, values, limit); err != nil {
			return nil, err
		}
		if b, ok := values[0].([]byte); ok {
			if err := consumeComplexBudget(budget, 4*((int64(len(b))+2)/3)+2); err != nil {
				return nil, err
			}
			return Raw(b), nil
		}
		if err := consumeComplexBudget(budget, lobValuesSize(values)); err != nil {
			return nil, err
		}
		return values[0], nil
	default:
		text, err := charset.Decode(c.complexCharset(f.typ), v.Data)
		if err != nil {
			return nil, err
		}
		return text, consumeComplexBudget(budget, int64(len(text)))
	}
}

func (c *conn) complexCharset(typ string) string {
	name := c.cfg.Session.Charset
	if typ == typeNameNChar || typ == typeNameNVarchar2 || typ == typeNameNClob {
		name = c.cfg.Session.NCharset
		if name == "" {
			name = c.cfg.Session.Charset
		}
	}
	if name == "" {
		name = charsetUTF8MB4
	}
	return name
}

func complexDescriptorError(name string, err error) error {
	return fmt.Errorf("%w: type %s: %w", ErrComplex, name, err)
}

func (c *conn) descriptorSynonymLocked(ctx context.Context, key descriptorKey, depth int, visiting map[descriptorKey]bool) (*typeDescriptor, error) {
	// Local types precede private and PUBLIC synonyms. Remote links are never followed.
	for _, schema := range []string{key.schema, "PUBLIC"} {
		synonyms, e := c.descriptorMetadataLocked(ctx, MetadataSynonyms, schema, key.name)
		if e != nil {
			return nil, e
		}
		if len(synonyms) == 0 {
			continue
		}
		if len(synonyms) != 1 || tableBlobText(synonyms[0][5]) != "" {
			return nil, ErrComplex
		}
		target := descriptorKey{key.mode, tableBlobText(synonyms[0][3]), tableBlobText(synonyms[0][4]), key.version}
		d, e := c.descriptorLocked(ctx, target, depth+1, visiting)
		if e == nil {
			c.cacheDescriptor(key, d)
		}
		return d, e
	}
	return nil, ErrComplex
}

func (c *conn) descriptorObjectLocked(ctx context.Context, d *typeDescriptor, row []driver.Value, depth int, visiting map[descriptorKey]bool) error {
	key := d.key
	count, e := tableBlobLength(row[4])
	if e != nil || count > maxTextColumns {
		return ErrComplex
	}
	attrs, e := c.descriptorMetadataLocked(ctx, MetadataTypeAttributes, key.schema, key.name)
	if e != nil {
		return e
	}
	if int64(len(attrs)) != count {
		return ErrComplex
	}
	seen := make(map[string]bool, len(attrs))
	for i, row := range attrs {
		ordinal, e := tableBlobLength(row[4])
		name := tableBlobText(row[3])
		if e != nil || ordinal != int64(i+1) || !validBlobIdentifier(name) || seen[name] {
			return ErrComplex
		}
		seen[name] = true
		f, e := c.descriptorFieldLocked(ctx, name, tableBlobText(row[5]), tableBlobText(row[6]), depth, visiting)
		if e != nil {
			return e
		}
		d.fields = append(d.fields, f)
	}
	return nil
}
