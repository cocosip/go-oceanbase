package catalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

// Resolver caches immutable descriptors for one outer command on one session.
type Resolver struct {
	cache map[valuecodec.Key]*valuecodec.Descriptor
	bytes int64
}

// Clear drops the current outer command descriptor cache and byte budget.
func (r *Resolver) Clear() { r.cache = nil; r.bytes = 0 }

// Resolve resolves one named type using the current outer command cache.
func (r *Resolver) Resolve(ctx context.Context, op *session.Operation, key valuecodec.Key) (*valuecodec.Descriptor, error) {
	return r.descriptorLocked(ctx, op, key, 0, make(map[valuecodec.Key]bool))
}

// ResolveAt preserves the current complex-value depth during nested name resolution.
func (r *Resolver) ResolveAt(ctx context.Context, op *session.Operation, key valuecodec.Key, depth int) (*valuecodec.Descriptor, error) {
	return r.descriptorLocked(ctx, op, key, depth, make(map[valuecodec.Key]bool))
}

func (r *Resolver) metadata(ctx context.Context, op *session.Operation, kind metadata.MetadataKind, schema, name string) ([][]driver.Value, error) {
	plan, err := Plan(op.Protocol().Mode, op.Protocol().OBVersion, metadata.MetadataFilter{Kind: kind, SchemaPattern: ExactDescriptorPattern(schema), NamePattern: ExactDescriptorPattern(name)})
	if err != nil {
		return nil, err
	}
	result, err := QueryRows(ctx, op, plan)
	if err != nil {
		return nil, err
	}
	if result.Bytes > op.Config().Limits.MaxMessageBytes-r.bytes {
		return nil, wire.ErrMessageTooLarge
	}
	r.bytes += result.Bytes
	return result.Rows, nil
}

// Text extracts dictionary text without changing NULL semantics.
func Text(value driver.Value) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	}
	return ""
}

// Scalar requires one column and at most one dictionary row.
func Scalar(result Result) (driver.Value, error) {
	if len(result.Columns) != 1 || len(result.Rows) > 1 {
		return nil, dberrors.ErrLOB
	}
	if len(result.Rows) == 0 {
		return nil, sql.ErrNoRows
	}
	return result.Rows[0][0], nil
}

// Length validates a nonnegative integral dictionary value.
func Length(value driver.Value) (int64, error) {
	if b, ok := value.([]byte); ok {
		value = string(b)
	}
	var n int64
	if v, ok := value.(int64); ok {
		n = v
	} else {
		v, ok := value.(string)
		if !ok {
			return 0, dberrors.ErrLOB
		}
		plain, err := valueformat.PlainNumber(v, 64)
		if err != nil {
			return 0, dberrors.ErrLOB
		}
		whole, fraction, _ := strings.Cut(plain, ".")
		if strings.Trim(fraction, "0") != "" {
			return 0, dberrors.ErrLOB
		}
		n, err = strconv.ParseInt(whole, 10, 64)
		if err != nil {
			return 0, dberrors.ErrLOB
		}
	}
	if n < 0 {
		return 0, dberrors.ErrLOB
	}
	return n, nil
}

// ExactDescriptorPattern escapes a descriptor identifier for an exact dictionary filter.
func ExactDescriptorPattern(name string) string {
	return strings.NewReplacer("/", "//", "%", "/%", "_", "/_").Replace(name)
}

func (r *Resolver) descriptorLocked(ctx context.Context, op *session.Operation, key valuecodec.Key, depth int, visiting map[valuecodec.Key]bool) (*valuecodec.Descriptor, error) {
	if op.ResponseActive() {
		return nil, ErrResponseActive
	}
	if key.Mode != config.ModeOracle {
		return nil, dberrors.ErrUnsupported
	}
	if depth > valueformat.MaxComplexDepth || visiting[key] || !valueformat.Identifier(key.Name) {
		return nil, dberrors.ErrComplex
	}
	if key.Schema == "" {
		sets, err := QueryRows(ctx, op, Query{SQL: "SELECT SYS_CONTEXT('USERENV','CURRENT_SCHEMA') FROM DUAL"})
		if err != nil {
			return nil, err
		}
		v, err := Scalar(sets)
		if err != nil {
			return nil, err
		}
		key.Schema = Text(v)
	}
	if !valueformat.Identifier(key.Schema) {
		return nil, dberrors.ErrComplex
	}
	if d := r.cache[key]; d != nil {
		return d, nil
	}
	if len(r.cache) >= command.MaxColumns {
		return nil, wire.ErrMessageTooLarge
	}
	visiting[key] = true
	defer delete(visiting, key)
	rows, err := r.metadata(ctx, op, metadata.MetadataTypes, key.Schema, key.Name)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return r.descriptorSynonymLocked(ctx, op, key, depth, visiting)
	}
	if len(rows) != 1 || Text(rows[0][1]) != key.Schema || Text(rows[0][2]) != key.Name {
		return nil, dberrors.ErrComplex
	}
	d := &valuecodec.Descriptor{Key: key, Shape: &wire.ComplexShape{}}
	switch strings.ToUpper(Text(rows[0][3])) {
	case valueformat.TypeNameObject:
		if err := r.descriptorObjectLocked(ctx, op, d, rows[0], depth, visiting); err != nil {
			return nil, err
		}
	case "COLLECTION":
		d.Array, d.Shape.Array = true, true
		rows, e := r.metadata(ctx, op, metadata.MetadataCollectionElements, key.Schema, key.Name)
		if e != nil {
			return nil, e
		}
		if len(rows) != 1 {
			return nil, dberrors.ErrComplex
		}
		f, e := r.descriptorFieldLocked(ctx, op, "", Text(rows[0][5]), Text(rows[0][6]), depth, visiting)
		if e != nil {
			return nil, e
		}
		d.Fields = []valuecodec.Field{f}
	default:
		return nil, dberrors.ErrUnsupported
	}
	for _, field := range d.Fields {
		var shape *wire.ComplexShape
		if field.Child != nil {
			shape = field.Child.Shape
		}
		d.Shape.Fields = append(d.Shape.Fields, shape)
	}
	r.cacheDescriptor(key, d)
	return d, nil
}

func (r *Resolver) cacheDescriptor(key valuecodec.Key, d *valuecodec.Descriptor) {
	if r.cache == nil {
		r.cache = make(map[valuecodec.Key]*valuecodec.Descriptor)
	}
	r.cache[key] = d
}

func (r *Resolver) descriptorFieldLocked(ctx context.Context, op *session.Operation, name, typ, schema string, depth int, visiting map[valuecodec.Key]bool) (valuecodec.Field, error) {
	f := valuecodec.Field{Name: name, Type: typ}
	if schema != "" && schema != "SYS" || !valuecodec.ComplexBuiltinType(typ) {
		var err error
		f.Child, err = r.descriptorLocked(ctx, op, valuecodec.Key{Mode: op.Protocol().Mode, Schema: schema, Name: typ, Version: 0}, depth+1, visiting)
		return f, err
	}
	return f, nil
}

// AnonymousDescriptor constructs a supported builtin collection descriptor.
func AnonymousDescriptor(column wire.Column) (*valuecodec.Descriptor, error) {
	var typ string
	switch column.ElementType {
	case 0, 3, 206, 246:
		typ = valueformat.TypeNameNumber
	case 12:
		typ = valueformat.TypeNameDate
	case 15, 253:
		typ = valueformat.TypeNameVarchar2
	case 254:
		typ = valueformat.TypeNameChar
	case 203:
		typ = valueformat.TypeNameRaw
	default:
		return nil, dberrors.ErrUnsupported
	}
	return &valuecodec.Descriptor{Key: valuecodec.Key{Mode: config.ModeOracle, Schema: column.ComplexSchema, Name: "", Version: column.ComplexVersion}, Array: true, Fields: []valuecodec.Field{{Type: typ}}, Shape: &wire.ComplexShape{Array: true, Fields: []*wire.ComplexShape{nil}}}, nil
}

// Column resolves complex metadata for one wire column.
func (r *Resolver) Column(ctx context.Context, op *session.Operation, column wire.Column) (*valuecodec.Descriptor, error) {
	if column.ComplexName == "" {
		if column.ElementType == 160 {
			element, err := r.descriptorLocked(ctx, op, valuecodec.Key{Mode: config.ModeOracle, Schema: column.ElementSchema, Name: column.ElementName, Version: column.ElementVersion}, 0, make(map[valuecodec.Key]bool))
			if err != nil {
				return nil, err
			}
			return AnonymousComplexDescriptor(column.ComplexVersion, element), nil
		}
		return AnonymousDescriptor(column)
	}
	if column.ElementType != 0 {
		return nil, dberrors.ErrUnsupported
	}
	return r.descriptorLocked(ctx, op, valuecodec.Key{Mode: op.Protocol().Mode, Schema: column.ComplexSchema, Name: column.ComplexName, Version: column.ComplexVersion}, 0, make(map[valuecodec.Key]bool))
}

// AnonymousComplexDescriptor constructs a collection of resolved complex elements.
func AnonymousComplexDescriptor(version uint64, element *valuecodec.Descriptor) *valuecodec.Descriptor {
	return &valuecodec.Descriptor{Key: valuecodec.Key{Mode: config.ModeOracle, Schema: element.Key.Schema, Name: "", Version: version}, Array: true, Fields: []valuecodec.Field{{Type: element.Key.Name, Child: element}}, Shape: &wire.ComplexShape{Array: true, Fields: []*wire.ComplexShape{element.Shape}}}
}

// DescribeColumns attaches immutable complex shapes to result metadata.
func (r *Resolver) DescribeColumns(ctx context.Context, op *session.Operation, columns []wire.Column) error {
	for i, column := range columns {
		if column.Type != 160 {
			continue
		}
		d, err := r.Column(ctx, op, column)
		if err != nil {
			return err
		}
		columns[i].ComplexShape = d.Shape
	}
	return nil
}

func (r *Resolver) descriptorSynonymLocked(ctx context.Context, op *session.Operation, key valuecodec.Key, depth int, visiting map[valuecodec.Key]bool) (*valuecodec.Descriptor, error) {
	// Local types precede private and PUBLIC synonyms. Remote links are never followed.
	for _, schema := range []string{key.Schema, "PUBLIC"} {
		synonyms, e := r.metadata(ctx, op, metadata.MetadataSynonyms, schema, key.Name)
		if e != nil {
			return nil, e
		}
		if len(synonyms) == 0 {
			continue
		}
		if len(synonyms) != 1 || Text(synonyms[0][5]) != "" {
			return nil, dberrors.ErrComplex
		}
		target := valuecodec.Key{Mode: key.Mode, Schema: Text(synonyms[0][3]), Name: Text(synonyms[0][4]), Version: key.Version}
		d, e := r.descriptorLocked(ctx, op, target, depth+1, visiting)
		if e == nil {
			r.cacheDescriptor(key, d)
		}
		return d, e
	}
	return nil, dberrors.ErrComplex
}

func (r *Resolver) descriptorObjectLocked(ctx context.Context, op *session.Operation, d *valuecodec.Descriptor, row []driver.Value, depth int, visiting map[valuecodec.Key]bool) error {
	key := d.Key
	count, e := Length(row[4])
	if e != nil || count > command.MaxColumns {
		return dberrors.ErrComplex
	}
	attrs, e := r.metadata(ctx, op, metadata.MetadataTypeAttributes, key.Schema, key.Name)
	if e != nil {
		return e
	}
	if int64(len(attrs)) != count {
		return dberrors.ErrComplex
	}
	seen := make(map[string]bool, len(attrs))
	for i, row := range attrs {
		ordinal, e := Length(row[4])
		name := Text(row[3])
		if e != nil || ordinal != int64(i+1) || !valueformat.Identifier(name) || seen[name] {
			return dberrors.ErrComplex
		}
		seen[name] = true
		f, e := r.descriptorFieldLocked(ctx, op, name, Text(row[5]), Text(row[6]), depth, visiting)
		if e != nil {
			return e
		}
		d.Fields = append(d.Fields, f)
	}
	return nil
}
