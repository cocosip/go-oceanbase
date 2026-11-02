package valuecodec

import (
	"strings"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// ResolvedComplex pairs a normalized value with its authoritative descriptor for client SQL.
type ResolvedComplex struct {
	Descriptor *Descriptor
	Value      any
}

func complexConstructor(v ResolvedComplex, mode config.Mode, options config.SessionOptions, maxLOBBytes, limit int64) (string, error) {
	if mode != config.ModeOracle {
		return "", dberrors.ErrUnsupported
	}
	nodes := valueformat.MaxComplexNodes
	if err := validateConstructor(v.Descriptor, 0, &nodes); err != nil {
		return "", err
	}
	e := constructorEncoder{options: options, maxLOBBytes: maxLOBBytes, limit: limit, nodes: valueformat.MaxComplexNodes}
	if err := e.value(v.Descriptor, v.Value, 0, false, false); err != nil {
		return "", err
	}
	return e.sql.String(), nil
}

func validateConstructor(d *Descriptor, depth int, nodes *int) error {
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return err
	}
	if d == nil || d.Key.Mode != config.ModeOracle || !valueformat.Identifier(d.Key.Schema) || !valueformat.Identifier(d.Key.Name) || d.Array && len(d.Fields) != 1 {
		return dberrors.ErrUnsupported
	}
	for _, f := range d.Fields {
		if f.Child != nil {
			if err := validateConstructor(f.Child, depth+1, nodes); err != nil {
				return err
			}
			continue
		}
		if err := valueformat.ComplexNode(depth+1, nodes); err != nil {
			return err
		}
		switch strings.ToUpper(f.Type) {
		case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric,
			valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameNChar,
			valueformat.TypeNameRaw, valueformat.TypeNameDate, valueformat.TypeNameTimestamp:
		default:
			return dberrors.ErrUnsupported
		}
	}
	return nil
}

type constructorEncoder struct {
	sql                strings.Builder
	options            config.SessionOptions
	maxLOBBytes, limit int64
	nodes              int
}

func (e *constructorEncoder) append(text string) error {
	name := e.options.Charset
	if name == "" {
		name = charsetUTF8MB4
	}
	data, err := charset.Encode(name, text)
	if err != nil {
		return err
	}
	if int64(len(data)) > e.limit {
		return wire.ErrMessageTooLarge
	}
	e.limit -= int64(len(data))
	e.sql.WriteString(text)
	return nil
}

func (e *constructorEncoder) value(d *Descriptor, value any, depth int, checkName, inCollection bool) error {
	if err := valueformat.ComplexNode(depth, &e.nodes); err != nil {
		return err
	}
	null := value == nil
	switch v := value.(type) {
	case types.Object:
		if d.Array {
			return dberrors.ErrComplex
		}
		null = v.Null
	case types.Array:
		if !d.Array {
			return dberrors.ErrComplex
		}
		null = v.Null
	}
	if null {
		// shortcut: collection constructors cannot preserve NULL OBJECT subtrees; require server identity evidence to extend.
		if inCollection && !d.Array {
			return dberrors.ErrUnsupported
		}
		return e.append("NULL")
	}
	schema, name, values, err := ComplexInputValues(d, value)
	if err != nil {
		return err
	}
	if checkName && (name != d.Key.Name || schema != "" && schema != d.Key.Schema) {
		return dberrors.ErrComplex
	}
	if len(values) > e.nodes {
		return dberrors.ErrComplex
	}
	schema = strings.ReplaceAll(d.Key.Schema, `"`, `""`)
	name = strings.ReplaceAll(d.Key.Name, `"`, `""`)
	if err = e.append(`"` + schema + `"."` + name + `"(`); err != nil {
		return err
	}
	for i, value := range values {
		if i > 0 {
			if err = e.append(","); err != nil {
				return err
			}
		}
		index := i
		if d.Array {
			index = 0
		}
		f := d.Fields[index]
		if f.Child != nil {
			err = e.value(f.Child, value, depth+1, true, inCollection || d.Array)
		} else {
			err = e.scalar(f.Type, value, depth+1)
		}
		if err != nil {
			return err
		}
	}
	return e.append(")")
}

func (e *constructorEncoder) scalar(typ string, value any, depth int) error {
	if err := valueformat.ComplexNode(depth, &e.nodes); err != nil {
		return err
	}
	if value == nil {
		return e.append("NULL")
	}
	var literal string
	var err error
	typ = strings.ToUpper(typ)
	switch typ {
	case valueformat.TypeNameDate, valueformat.TypeNameTimestamp:
		literal, err = constructorTime(typ, value)
	default:
		var p wire.Parameter
		p, err = EncodeComplexScalar(typ, value, e.options, e.maxLOBBytes, e.limit)
		if err != nil {
			return err
		}
		switch typ {
		case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric:
			literal = string(p.Data)
		case valueformat.TypeNameRaw:
			literal, err = ClientLiteral(p.Data, config.ModeOracle, e.options, e.maxLOBBytes, e.limit)
		default:
			var text string
			switch v := value.(type) {
			case string:
				text = v
			case types.NString:
				text = string(v)
			default:
				return dberrors.ErrComplex
			}
			var v any = text
			if typ == valueformat.TypeNameNChar || typ == valueformat.TypeNameNVarchar2 {
				v = types.NString(text)
			}
			literal, err = ClientLiteral(v, config.ModeOracle, e.options, e.maxLOBBytes, e.limit)
		}
	}
	if err != nil {
		return err
	}
	return e.append(literal)
}

func constructorTime(typ string, value any) (string, error) {
	var wall time.Time
	switch v := value.(type) {
	case time.Time:
		wall = v
	case string:
		var err error
		wall, _, err = valueformat.ParseWall(v)
		if err != nil {
			return "", err
		}
	default:
		return "", dberrors.ErrComplex
	}
	if wall.Year() < 1 || wall.Year() > 9999 {
		return "", dberrors.ErrType
	}
	if typ == valueformat.TypeNameDate {
		if wall.Nanosecond() != 0 {
			return "", dberrors.ErrComplex
		}
		return "TO_DATE('" + wall.Format(valueformat.TimestampLayout) + "','YYYY-MM-DD HH24:MI:SS')", nil
	}
	return "TIMESTAMP '" + wall.Format(valueformat.TimestampLayout+".000000000") + "'", nil
}
