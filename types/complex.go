package types

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
)

// Attribute is an ordered object field. Type uses the exact dictionary type name.
// Type may be omitted for input; the server descriptor supplies it before binding.
type Attribute struct {
	Name  string
	Type  string
	Value any
}

// Object is an explicitly named Oracle compatibility type, with ordered fields.
// Schema and Type use exact dictionary spelling. Version zero asks for the current type.
type Object struct {
	Schema  string
	Type    string
	Version uint64
	Fields  []Attribute
	Null    bool
}

// Array is a named Oracle compatibility collection. Nil and empty Elements both
// mean an empty collection; Null alone represents SQL NULL.
// An empty Type denotes an anonymous array and requires an explicit ElementType.
type Array struct {
	Schema         string
	Type           string
	Version        uint64
	ElementType    string
	ElementVersion uint64
	Elements       []any
	Null           bool
}

// Value returns lossless format-1 JSON. This driver preserves Object identity in
// CheckNamedValue and binds the native complex protocol, rather than this JSON.
func (o Object) Value() (driver.Value, error) { return complexDriverValue(o, o.Null) }

// Value returns lossless format-1 JSON; native bindings use the server descriptor.
func (a Array) Value() (driver.Value, error) { return complexDriverValue(a, a.Null) }

// Scan consumes this driver's self-describing JSON. A failed Scan leaves o unchanged.
func (o *Object) Scan(src any) error {
	if o == nil {
		return dberrors.ErrComplex
	}
	if src == nil {
		*o = Object{Null: true}
		return nil
	}
	v, err := scanComplex(src)
	value, ok := v.(Object)
	if err != nil || !ok {
		return errors.Join(dberrors.ErrComplex, err)
	}
	*o = value
	return nil
}

// Scan consumes a collection JSON document, preserving NULL elements and order.
func (a *Array) Scan(src any) error {
	if a == nil {
		return dberrors.ErrComplex
	}
	if src == nil {
		*a = Array{Null: true}
		return nil
	}
	v, err := scanComplex(src)
	value, ok := v.(Array)
	if err != nil || !ok {
		return errors.Join(dberrors.ErrComplex, err)
	}
	*a = value
	return nil
}

func complexDriverValue(value any, null bool) (driver.Value, error) {
	if null {
		return nil, nil
	}
	nodes := valueformat.MaxComplexNodes
	data, err := marshalComplex(value, 0, &nodes)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func marshalComplex(value any, depth int, nodes *int) ([]byte, error) {
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return nil, err
	}
	d := valueformat.ComplexJSON{Format: 1}
	payloadBytes := 0
	switch v := value.(type) {
	case Object:
		if !valueformat.Identifier(v.Type) || v.Schema != "" && !valueformat.Identifier(v.Schema) || len(v.Fields) > *nodes {
			return nil, dberrors.ErrComplex
		}
		d.Kind, d.Schema, d.Type, d.Version, d.Null = valueformat.ComplexObject, v.Schema, v.Type, v.Version, v.Null
		seen := make(map[string]bool, len(v.Fields))
		for _, f := range v.Fields {
			if v.Null || !valueformat.Identifier(f.Name) || seen[f.Name] {
				return nil, dberrors.ErrComplex
			}
			seen[f.Name] = true
			data, err := marshalComplexScalar(f.Type, f.Value, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			payloadBytes += len(data) + len(f.Name) + len(f.Type)
			if payloadBytes > valueformat.MaxComplexJSON {
				return nil, dberrors.ErrComplex
			}
			d.Fields = append(d.Fields, valueformat.ComplexJSONField{Name: f.Name, Type: f.Type, Value: data})
		}
	case Array:
		if v.Type != "" && !valueformat.Identifier(v.Type) || v.Schema != "" && !valueformat.Identifier(v.Schema) || len(v.Elements) > *nodes {
			return nil, dberrors.ErrComplex
		}
		d.Kind, d.Schema, d.Type, d.Version, d.Null, d.ElementType = valueformat.ComplexArray, v.Schema, v.Type, v.Version, v.Null, v.ElementType
		d.ElementVersion = v.ElementVersion
		for _, element := range v.Elements {
			if v.Null {
				return nil, dberrors.ErrComplex
			}
			data, err := marshalComplexScalar(v.ElementType, element, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			payloadBytes += len(data)
			if payloadBytes > valueformat.MaxComplexJSON {
				return nil, dberrors.ErrComplex
			}
			d.Elements = append(d.Elements, data)
		}
	default:
		return nil, dberrors.ErrComplex
	}
	data, err := json.Marshal(d)
	if err != nil || len(data) > valueformat.MaxComplexJSON {
		return nil, errors.Join(dberrors.ErrComplex, err)
	}
	return data, nil
}

func marshalComplexScalar(typ string, value any, depth int, nodes *int) ([]byte, error) {
	switch value.(type) {
	case Object, Array:
		return marshalComplex(value, depth, nodes)
	}
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return nil, err
	}
	if value == nil {
		return []byte("null"), nil
	}
	var text string
	switch v := value.(type) {
	case Number:
		if _, err := v.Value(); err != nil {
			return nil, err
		}
		text = string(v)
	case Raw:
		text = base64.StdEncoding.EncodeToString(v)
	case []byte:
		text = base64.StdEncoding.EncodeToString(v)
	case string:
		text = v
	case NString:
		text = string(v)
	case time.Time:
		text = v.Format("2006-01-02 15:04:05.999999999")
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		text = fmt.Sprint(v)
	default:
		return nil, dberrors.ErrComplex
	}
	if !utf8.ValidString(text) || len(text) > valueformat.MaxComplexJSON {
		return nil, dberrors.ErrComplex
	}
	if typ != "" {
		if _, err := parseComplexScalar(typ, text); err != nil {
			return nil, err
		}
	}
	return json.Marshal(text)
}

func scanComplex(src any) (any, error) {
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return nil, dberrors.ErrComplex
	}
	if len(data) > valueformat.MaxComplexJSON || !utf8.Valid(data) {
		return nil, dberrors.ErrComplex
	}
	nodes := valueformat.MaxComplexNodes
	return unmarshalComplex(data, 0, &nodes)
}

func unmarshalComplex(data []byte, depth int, nodes *int) (any, error) {
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return nil, err
	}
	var d valueformat.ComplexJSON
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil || dec.Decode(new(any)) != io.EOF || d.Format != 1 || d.Schema != "" && !valueformat.Identifier(d.Schema) {
		return nil, dberrors.ErrComplex
	}
	switch d.Kind {
	case valueformat.ComplexObject:
		if !valueformat.Identifier(d.Type) || len(d.Fields) > *nodes || len(d.Elements) != 0 || d.ElementType != "" {
			return nil, dberrors.ErrComplex
		}
		o := Object{Schema: d.Schema, Type: d.Type, Version: d.Version, Null: d.Null, Fields: make([]Attribute, 0, len(d.Fields))}
		seen := make(map[string]bool, len(d.Fields))
		for _, f := range d.Fields {
			if d.Null || !valueformat.Identifier(f.Name) || seen[f.Name] {
				return nil, dberrors.ErrComplex
			}
			seen[f.Name] = true
			v, err := unmarshalComplexScalar(f.Type, f.Value, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			o.Fields = append(o.Fields, Attribute{Name: f.Name, Type: f.Type, Value: v})
		}
		return o, nil
	case valueformat.ComplexArray:
		if d.Type != "" && !valueformat.Identifier(d.Type) || len(d.Fields) != 0 || len(d.Elements) > *nodes {
			return nil, dberrors.ErrComplex
		}
		a := Array{Schema: d.Schema, Type: d.Type, Version: d.Version, Null: d.Null, ElementType: d.ElementType, Elements: make([]any, 0, len(d.Elements))}
		a.ElementVersion = d.ElementVersion
		for _, raw := range d.Elements {
			if d.Null {
				return nil, dberrors.ErrComplex
			}
			v, err := unmarshalComplexScalar(d.ElementType, raw, depth+1, nodes)
			if err != nil {
				return nil, err
			}
			a.Elements = append(a.Elements, v)
		}
		return a, nil
	}
	return nil, dberrors.ErrComplex
}

func unmarshalComplexScalar(typ string, data []byte, depth int, nodes *int) (any, error) {
	if len(data) > 0 && data[0] == '{' {
		return unmarshalComplex(data, depth, nodes)
	}
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return nil, err
	}
	if string(data) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return nil, dberrors.ErrComplex
	}
	return parseComplexScalar(typ, text)
}

func parseComplexScalar(typ, text string) (any, error) {
	switch strings.ToUpper(typ) {
	case valueformat.TypeNameNumber, valueformat.TypeNameDecimal, valueformat.TypeNameInteger, valueformat.TypeNameNumeric:
		n := Number(text)
		_, err := n.Value()
		return n, err
	case valueformat.TypeNameRaw, valueformat.TypeNameBlob:
		b, err := base64.StdEncoding.DecodeString(text)
		return Raw(b), err
	case valueformat.TypeNameDate, valueformat.TypeNameTimestamp:
		_, _, err := valueformat.ParseWall(text)
		return text, err
	case "", valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2, valueformat.TypeNameClob, valueformat.TypeNameNClob:
		return text, nil
	}
	return nil, dberrors.ErrComplex
}

var (
	_ sql.Scanner   = (*Object)(nil)
	_ sql.Scanner   = (*Array)(nil)
	_ driver.Valuer = Object{}
	_ driver.Valuer = Array{}
)
