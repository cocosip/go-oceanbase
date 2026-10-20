package materialize

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func (c *Decoder) complexValueLocked(ctx context.Context, column wire.Column, data []byte) (driver.Value, error) {
	if data == nil {
		return nil, nil
	}
	d, err := c.resolver.Column(ctx, c.op, column)
	if err != nil {
		return nil, err
	}
	v, n, err := wire.DecodeComplex(data, d.Shape, valueformat.MaxComplexNodes)
	if err != nil || n != len(data) {
		// Only buffered bytes are parsed again. No business command is replayed.
		c.resolver.Clear()
		var e error
		d, e = c.resolver.Column(ctx, c.op, column)
		if e != nil {
			return nil, errors.Join(dberrors.ErrComplex, err, e)
		}
		v, n, err = wire.DecodeComplex(data, d.Shape, valueformat.MaxComplexNodes)
		if err != nil || n != len(data) {
			return nil, errors.Join(dberrors.ErrComplex, err)
		}
	}
	budget := min(c.op.Config().Limits.MaxMessageBytes, int64(valueformat.MaxComplexJSON))
	value, err := c.decodeComplexValueLocked(ctx, d, v, 0, &budget)
	if err != nil {
		return nil, err
	}
	encoded, err := valuecodec.ComplexDriverValue(value, false)
	if err == nil && int64(len(encoded.(string))) > c.op.Config().Limits.MaxMessageBytes {
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

func (c *Decoder) decodeComplexValueLocked(ctx context.Context, d *valuecodec.Descriptor, value wire.ComplexValue, depth int, budget *int64) (any, error) {
	if depth > valueformat.MaxComplexDepth {
		return nil, dberrors.ErrComplex
	}
	if value.Null {
		return nil, nil
	}
	if err := consumeComplexBudget(budget, 128+int64(len(d.Key.Schema)+len(d.Key.Name))+int64(len(value.Children))*16); err != nil {
		return nil, err
	}
	if d.Array {
		a := types.Array{Schema: d.Key.Schema, Type: d.Key.Name, Version: d.Key.Version, ElementType: d.Fields[0].Type, Elements: make([]any, 0, len(value.Children))}
		if d.Fields[0].Child != nil {
			a.ElementVersion = d.Fields[0].Child.Key.Version
		}
		for _, v := range value.Children {
			e, err := c.decodeComplexFieldLocked(ctx, d.Fields[0], v, depth+1, budget)
			if err != nil {
				return nil, err
			}
			a.Elements = append(a.Elements, e)
		}
		return a, nil
	}
	if len(value.Children) != len(d.Fields) {
		return nil, dberrors.ErrComplex
	}
	o := types.Object{Schema: d.Key.Schema, Type: d.Key.Name, Version: d.Key.Version, Fields: make([]types.Attribute, 0, len(d.Fields))}
	for i, f := range d.Fields {
		if err := consumeComplexBudget(budget, 64+int64(len(f.Name)+len(f.Type))); err != nil {
			return nil, err
		}
		v, err := c.decodeComplexFieldLocked(ctx, f, value.Children[i], depth+1, budget)
		if err != nil {
			return nil, err
		}
		o.Fields = append(o.Fields, types.Attribute{Name: f.Name, Type: f.Type, Value: v})
	}
	return o, nil
}

func (c *Decoder) decodeComplexFieldLocked(ctx context.Context, f valuecodec.Field, v wire.ComplexValue, depth int, budget *int64) (any, error) {
	if v.Null {
		return nil, nil
	}
	if f.Child != nil {
		return c.decodeComplexValueLocked(ctx, f.Child, v, depth, budget)
	}
	if err := consumeComplexBudget(budget, 16); err != nil {
		return nil, err
	}
	switch strings.ToUpper(f.Type) {
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameNumeric:
		if err := consumeComplexBudget(budget, int64(len(v.Data))); err != nil {
			return nil, err
		}
		return valuecodec.DecodeComplexScalar(f.Type, v.Data, c.op.Config().Session)
	case valueformat.TypeNameRaw:
		if err := consumeComplexBudget(budget, 4*((int64(len(v.Data))+2)/3)+2); err != nil {
			return nil, err
		}
		return valuecodec.DecodeComplexScalar(f.Type, v.Data, c.op.Config().Session)
	case valueformat.TypeNameDate:
		return valuecodec.DecodeComplexScalar(f.Type, v.Data, c.op.Config().Session)
	case valueformat.TypeNameTimestamp:
		return valuecodec.DecodeComplexScalar(f.Type, v.Data, c.op.Config().Session)
	case valueformat.TypeNameBlob, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		column, err := valuecodec.ComplexLOBColumn(f.Type, c.op.Config().Session)
		if err != nil {
			return nil, err
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
			return types.Raw(b), nil
		}
		if err := consumeComplexBudget(budget, rowset.ValuesSize(values)); err != nil {
			return nil, err
		}
		return values[0], nil
	default:
		text, err := charset.Decode(c.complexCharset(f.Type), v.Data)
		if err != nil {
			return nil, err
		}
		return text, consumeComplexBudget(budget, int64(len(text)))
	}
}

func (c *Decoder) complexCharset(typ string) string {
	return valuecodec.ComplexCharset(typ, c.op.Config().Session)
}
