package wire

// ComplexShape frames binary complex fields. A nil child denotes a length-encoded scalar.
// Arrays have exactly one child shape; objects have one per declared attribute.
type ComplexShape struct {
	Array  bool
	Fields []*ComplexShape
}

// ComplexValue retains scalar bytes and nested NULLs without interpreting database types.
type ComplexValue struct {
	Null     bool
	Data     []byte
	Children []ComplexValue
}

// DecodeComplex decodes one binary result value, with a shared allocation budget.
func DecodeComplex(data []byte, shape *ComplexShape, nodes int) (ComplexValue, int, error) {
	r := decoder{data: data}
	v, err := r.complex(shape, 0, &nodes, true)
	return v, r.offset, err
}

func (r *decoder) complex(shape *ComplexShape, depth int, nodes *int, collect bool) (ComplexValue, error) {
	if depth > 32 || *nodes < 1 {
		return ComplexValue{}, ErrMessageTooLarge
	}
	*nodes--
	if shape == nil {
		return ComplexValue{Data: r.lenBytes()}, r.err
	}
	n := len(shape.Fields)
	if shape.Array {
		if n != 1 {
			return ComplexValue{}, ErrMalformed
		}
		count := r.lenEnc()
		if count > uint64(*nodes) {
			return ComplexValue{}, ErrMessageTooLarge
		}
		n = int(count)
	}
	if n > *nodes {
		return ComplexValue{}, ErrMessageTooLarge
	}
	bitmap := r.take((n + 9) / 8)
	if r.err != nil {
		return ComplexValue{}, r.err
	}
	v := ComplexValue{}
	if collect {
		v.Children = make([]ComplexValue, n)
	}
	for i := 0; i < n; i++ {
		child := ComplexValue{Null: bitmap[(i+2)/8]&(1<<uint((i+2)%8)) != 0}
		if child.Null {
			if depth >= 32 || *nodes < 1 {
				return ComplexValue{}, ErrMessageTooLarge
			}
			*nodes--
		} else {
			index := i
			if shape.Array {
				index = 0
			}
			var err error
			child, err = r.complex(shape.Fields[index], depth+1, nodes, collect)
			if err != nil {
				return ComplexValue{}, err
			}
		}
		if collect {
			v.Children[i] = child
		}
	}
	return v, nil
}
