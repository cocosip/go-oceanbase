package valuecodec

import (
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// DecodeComplexText decodes the supported named OBJECT text subset using its dictionary identity.
func DecodeComplexText(d *Descriptor, data []byte, limit int64) (types.Object, error) {
	limit = min(limit, int64(valueformat.MaxComplexJSON))
	if int64(len(data)) > limit {
		return types.Object{}, wire.ErrMessageTooLarge
	}
	if !utf8.Valid(data) {
		return types.Object{}, charset.ErrEncoding
	}
	if err := ValidateComplexText(d); err != nil {
		return types.Object{}, err
	}
	p := complexTextParser{text: string(data), budget: limit, nodes: valueformat.MaxComplexNodes}
	v, err := p.object(d, 0)
	p.space()
	if err == nil && p.pos != len(p.text) {
		err = dberrors.ErrComplex
	}
	return v, err
}

// ValidateComplexText checks the entire text descriptor, including NULL subtrees.
func ValidateComplexText(d *Descriptor) error {
	nodes := valueformat.MaxComplexNodes
	return validateTextDescriptor(d, 0, &nodes)
}

func validateTextDescriptor(d *Descriptor, depth int, nodes *int) error {
	if err := valueformat.ComplexNode(depth, nodes); err != nil {
		return err
	}
	if d == nil || d.Array || d.Key.Mode != config.ModeOracle || d.Key.Name == "" {
		return dberrors.ErrUnsupported
	}
	for _, f := range d.Fields {
		if f.Child != nil {
			if err := validateTextDescriptor(f.Child, depth+1, nodes); err != nil {
				return err
			}
		} else {
			if err := valueformat.ComplexNode(depth+1, nodes); err != nil {
				return err
			}
			switch strings.ToUpper(f.Type) {
			case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric,
				valueformat.TypeNameVarchar2, valueformat.TypeNameVarchar, valueformat.TypeNameChar, valueformat.TypeNameNChar, valueformat.TypeNameNVarchar2:
			default:
				return dberrors.ErrUnsupported
			}
		}
	}
	return nil
}

type complexTextParser struct {
	text       string
	pos, nodes int
	budget     int64
}

func (p *complexTextParser) consume(n int64) error {
	if n > p.budget {
		return wire.ErrMessageTooLarge
	}
	p.budget -= n
	return nil
}

func (p *complexTextParser) space() {
	for p.pos < len(p.text) && strings.ContainsRune(" \t\n\r", rune(p.text[p.pos])) {
		p.pos++
	}
}

func (p *complexTextParser) expect(c byte) error {
	p.space()
	if p.pos == len(p.text) || p.text[p.pos] != c {
		return dberrors.ErrComplex
	}
	p.pos++
	return nil
}

func (p *complexTextParser) object(d *Descriptor, depth int) (types.Object, error) {
	if err := valueformat.ComplexNode(depth, &p.nodes); err != nil {
		return types.Object{}, err
	}
	p.space()
	start := p.pos
	for p.pos < len(p.text) && !strings.ContainsRune("( \t\n\r", rune(p.text[p.pos])) {
		p.pos++
	}
	if p.text[start:p.pos] != d.Key.Name {
		return types.Object{}, dberrors.ErrComplex
	}
	if err := p.expect('('); err != nil {
		return types.Object{}, err
	}
	if err := p.consume(128 + int64(len(d.Key.Schema)+len(d.Key.Name)) + int64(len(d.Fields))*80); err != nil {
		return types.Object{}, err
	}
	o := types.Object{Schema: d.Key.Schema, Type: d.Key.Name, Version: d.Key.Version, Fields: make([]types.Attribute, 0, len(d.Fields))}
	for i, f := range d.Fields {
		if i != 0 {
			if err := p.expect(','); err != nil {
				return types.Object{}, err
			}
		}
		if err := p.consume(int64(len(f.Name) + len(f.Type))); err != nil {
			return types.Object{}, err
		}
		v, err := p.member(f, depth+1)
		if err != nil {
			return types.Object{}, err
		}
		o.Fields = append(o.Fields, types.Attribute{Name: f.Name, Type: f.Type, Value: v})
	}
	return o, p.expect(')')
}

func (p *complexTextParser) member(f Field, depth int) (any, error) {
	p.space()
	if p.pos+4 <= len(p.text) && strings.EqualFold(p.text[p.pos:p.pos+4], "NULL") && (p.pos+4 == len(p.text) || strings.ContainsRune(",) \t\n\r", rune(p.text[p.pos+4]))) {
		p.pos += 4
		return nil, valueformat.ComplexNode(depth, &p.nodes)
	}
	if f.Child != nil {
		return p.object(f.Child, depth)
	}
	if err := valueformat.ComplexNode(depth, &p.nodes); err != nil {
		return nil, err
	}
	switch strings.ToUpper(f.Type) {
	case valueformat.TypeNameNumber, valueformat.TypeNameInteger, valueformat.TypeNameDecimal, valueformat.TypeNameNumeric:
		start := p.pos
		for p.pos < len(p.text) && p.text[p.pos] != ',' && p.text[p.pos] != ')' {
			p.pos++
		}
		text := strings.TrimSpace(p.text[start:p.pos])
		if err := p.consume(int64(len(text))); err != nil {
			return nil, err
		}
		n := types.Number(text)
		if _, err := n.Value(); err != nil {
			return nil, err
		}
		return n, nil
	default:
		return p.quoted()
	}
}

func (p *complexTextParser) closingQuote() bool {
	next := p.pos
	for next < len(p.text) && strings.ContainsRune(" \t\n\r", rune(p.text[next])) {
		next++
	}
	return next == len(p.text) || p.text[next] == ',' || p.text[next] == ')'
}

func (p *complexTextParser) quoted() (string, error) {
	if err := p.expect(39); err != nil {
		return "", err
	}
	var b strings.Builder
	for p.pos < len(p.text) {
		c := p.text[p.pos]
		p.pos++
		if c == 39 {
			if p.closingQuote() {
				return b.String(), nil
			}
			// The server emits both raw interior quotes and doubled quotes.
			if p.pos < len(p.text) && p.text[p.pos] == 39 {
				p.pos++
				if p.closingQuote() {
					p.pos--
				}
			}
		}
		if err := p.consume(1); err != nil {
			return "", err
		}
		b.WriteByte(c)
	}
	return "", dberrors.ErrComplex
}
