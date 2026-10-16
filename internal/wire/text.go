package wire

// Column is the protocol 4.1 column definition, including OceanBase's final bytes.
type Column struct {
	Catalog, Schema, Table, OriginalTable, Name, OriginalName string
	Charset                                                   uint16
	Length                                                    uint32
	Type                                                      byte
	Flags                                                     uint16
	Decimals, Precision, InOut                                byte
	Extra                                                     []byte
	ComplexSchema, ComplexName                                string
	ComplexVersion                                            uint64
	ElementType                                               byte
	ElementSchema, ElementName                                string
	ElementVersion                                            uint64
	ComplexShape                                              *ComplexShape
}

// DecodeColumn parses a complete column definition, preserving optional extension bytes.
func DecodeColumn(data []byte) (Column, error) {
	r := decoder{data: data}
	c := Column{Catalog: string(r.lenBytes()), Schema: string(r.lenBytes()), Table: string(r.lenBytes()), OriginalTable: string(r.lenBytes()), Name: string(r.lenBytes()), OriginalName: string(r.lenBytes())}
	if r.lenEnc() != 12 {
		return Column{}, ErrMalformed
	}
	c.Charset = r.u16()
	c.Length = r.u32()
	c.Type = r.u8()
	c.Flags = r.u16()
	c.Decimals = r.u8()
	c.Precision = r.u8()
	c.InOut = r.u8()
	if c.Type == 160 {
		c.ComplexSchema, c.ComplexName = string(r.lenBytes()), string(r.lenBytes())
		c.ComplexVersion = r.lenEnc()
		if c.ComplexName == "" {
			c.ElementType = r.u8()
			if c.ElementType == 160 {
				c.ElementSchema, c.ElementName = string(r.lenBytes()), string(r.lenBytes())
				c.ElementVersion = r.lenEnc()
			}
		}
	}
	c.Extra = r.take(r.remaining())
	if r.err != nil {
		return Column{}, r.err
	}
	return c, nil
}

// DecodeColumnCount rejects overflow and excessive metadata before allocation.
func DecodeColumnCount(data []byte, limit int) (int, error) {
	r := decoder{data: data}
	n := r.lenEnc()
	if r.err != nil || limit < 1 || n < 1 || n > uint64(limit) || r.remaining() != 0 {
		return 0, ErrMalformed
	}
	return int(n), nil
}

// DecodeTextRow distinguishes SQL NULL from a non-nil empty field. Slices alias data.
func DecodeTextRow(data []byte, columns int) ([][]byte, error) {
	if columns < 0 || columns > len(data) {
		return nil, ErrMalformed
	}
	r := decoder{data: data}
	fields := make([][]byte, columns)
	for i := range fields {
		if r.remaining() > 0 && r.data[r.offset] == 251 {
			r.u8()
			continue
		}
		fields[i] = r.lenBytes()
	}
	if r.err != nil || r.remaining() != 0 {
		return nil, ErrMalformed
	}
	return fields, nil
}

// DecodeEOF parses a legacy protocol 4.1 result terminator.
func DecodeEOF(data []byte) (OK, error) {
	if len(data) != 5 || data[0] != 254 {
		return OK{}, ErrMalformed
	}
	r := decoder{data: data}
	r.u8()
	ok := OK{Warnings: r.u16(), Status: r.u16()}
	return ok, r.err
}
