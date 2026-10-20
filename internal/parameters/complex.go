package parameters

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func encodeComplex(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, value any, null bool, key valuecodec.Key, limit int64) (wire.Parameter, error) {
	if op.Protocol().Mode != config.ModeOracle {
		return wire.Parameter{}, dberrors.ErrUnsupported
	}
	if null {
		return wire.Parameter{Type: 6}, nil
	}
	var d *valuecodec.Descriptor
	var err error
	if key.Name == "" {
		a, ok := value.(types.Array)
		if !ok {
			return wire.Parameter{}, dberrors.ErrComplex
		}
		column := wire.Column{ComplexSchema: a.Schema, ComplexVersion: a.Version, ElementType: valuecodec.ComplexElementWireType(a.ElementType)}
		if column.ElementType == 255 {
			if valuecodec.ComplexBuiltinType(a.ElementType) {
				return wire.Parameter{}, dberrors.ErrUnsupported
			}
			var element *valuecodec.Descriptor
			element, err = resolver.Resolve(ctx, op, valuecodec.Key{Mode: config.ModeOracle, Schema: a.Schema, Name: a.ElementType, Version: a.ElementVersion})
			if err == nil {
				d = catalog.AnonymousComplexDescriptor(a.Version, element)
			}
		} else {
			d, err = catalog.AnonymousDescriptor(column)
		}
	} else {
		d, err = resolver.Resolve(ctx, op, key)
	}
	if err != nil {
		return wire.Parameter{}, fmt.Errorf("%w: type %s: %w", dberrors.ErrComplex, key.Name, err)
	}
	cfg := op.Config()
	metadata, err := valuecodec.EncodeComplexMetadata(d, cfg.Session, cfg.Limits.MaxMessageBytes-1, limit)
	if err != nil {
		return wire.Parameter{}, err
	}
	e := complexEncoder{ctx: ctx, op: op, resolver: resolver, options: cfg.Session, maxLOBBytes: cfg.Limits.MaxLOBBytes, limit: limit - int64(len(metadata)), nodes: valueformat.MaxComplexNodes}
	if err = e.value(d, value, 0, false); err != nil {
		return wire.Parameter{}, err
	}
	return wire.Parameter{Type: 160, TypeMetadata: metadata, Data: e.data}, nil
}

type complexEncoder struct {
	ctx         context.Context
	op          *session.Operation
	resolver    *catalog.Resolver
	options     config.SessionOptions
	maxLOBBytes int64
	data        []byte
	limit       int64
	nodes       int
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
		e.data = wire.AppendComplexLenBytes(e.data, data)
	} else {
		e.data = append(e.data, data...)
	}
	return nil
}

func (e *complexEncoder) value(d *valuecodec.Descriptor, value any, depth int, checkName bool) error {
	if err := valueformat.ComplexNode(depth, &e.nodes); err != nil {
		return err
	}
	schema, name, values, err := complexInputValues(d, value)
	if err != nil {
		return err
	}
	if checkName && (schema != "" && schema != d.Key.Schema || name != d.Key.Name) {
		resolved, err := e.resolver.ResolveAt(e.ctx, e.op, valuecodec.Key{Mode: config.ModeOracle, Schema: schema, Name: name}, depth)
		if err != nil {
			return err
		}
		if resolved.Key.Schema != d.Key.Schema || resolved.Key.Name != d.Key.Name {
			return dberrors.ErrComplex
		}
	}
	if len(values) > e.nodes {
		return dberrors.ErrComplex
	}
	if d.Array {
		if err := e.append(wire.AppendComplexLenEnc(nil, uint64(len(values))), false); err != nil {
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
		case types.Object:
			null = v.Null
		case types.Array:
			null = v.Null
		}
		if null {
			if err := valueformat.ComplexNode(depth+1, &e.nodes); err != nil {
				return err
			}
			e.data[bitmap+i/8] |= 1 << uint(i%8)
			continue
		}
		index := i
		if d.Array {
			index = 0
		}
		field := d.Fields[index]
		var err error
		if field.Child != nil {
			err = e.value(field.Child, value, depth+1, true)
		} else {
			err = e.scalar(field.Type, value, depth+1)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *complexEncoder) scalar(typ string, value any, depth int) error {
	if err := valueformat.ComplexNode(depth, &e.nodes); err != nil {
		return err
	}
	p, err := valuecodec.EncodeComplexScalar(typ, value, e.options, e.maxLOBBytes, e.limit-int64(len(e.data)))
	if err != nil {
		return err
	}
	return e.append(p.Data, p.LengthEncoded)
}

func complexInputValues(d *valuecodec.Descriptor, value any) (string, string, []any, error) {
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
