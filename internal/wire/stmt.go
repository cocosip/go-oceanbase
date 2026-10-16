package wire

import (
	"encoding/binary"
	"math"
)

// PrepareOK identifies a statement and its following metadata counts.
type PrepareOK struct {
	ID                            uint32
	Columns, Parameters, Warnings uint16
}

// DecodePrepareOK parses the fixed protocol 4.1 prepare response.
func DecodePrepareOK(data []byte) (PrepareOK, error) {
	if len(data) != 12 || data[0] != 0 || data[9] != 0 {
		return PrepareOK{}, ErrMalformed
	}
	return PrepareOK{ID: binary.LittleEndian.Uint32(data[1:]), Columns: binary.LittleEndian.Uint16(data[5:]), Parameters: binary.LittleEndian.Uint16(data[7:]), Warnings: binary.LittleEndian.Uint16(data[10:])}, nil
}

// Parameter contains the wire type and unprefixed data for length-encoded values.
type Parameter struct {
	Type          uint16
	Data          []byte
	LengthEncoded bool
}

// EncodedSize returns the parameter body size, excluding bitmap and type header.
func (p Parameter) EncodedSize() int64 {
	if byte(p.Type) == 6 {
		return 0
	}
	n := int64(len(p.Data))
	if !p.LengthEncoded {
		return n
	}
	switch {
	case n < 251:
		return n + 1
	case n <= 65535:
		return n + 3
	case n <= 16777215:
		return n + 4
	default:
		return n + 9
	}
}

// EncodeStmtExecute includes fresh type headers on every execution.
func EncodeStmtExecute(id uint32, params []Parameter, limit int64) ([]byte, error) {
	if len(params) > 65535 {
		return nil, ErrMalformed
	}
	if limit < 10 {
		return nil, ErrMessageTooLarge
	}
	n := int64(10)
	if len(params) > 0 {
		n += int64((len(params)+7)/8 + 1 + 2*len(params))
	}
	for _, p := range params {
		size := p.EncodedSize()
		if size > limit-n {
			return nil, ErrMessageTooLarge
		}
		n += size
	}
	if n > limit || n > int64(math.MaxInt) {
		return nil, ErrMessageTooLarge
	}
	b := make([]byte, 10, int(n))
	b[0] = 23
	binary.LittleEndian.PutUint32(b[1:], id)
	b[6] = 1
	if len(params) == 0 {
		return b, nil
	}
	bitmap := len(b)
	b = append(b, make([]byte, (len(params)+7)/8)...)
	b = append(b, 1)
	for i, p := range params {
		if byte(p.Type) == 6 {
			b[bitmap+i/8] |= 1 << uint(i%8)
		}
		b = binary.LittleEndian.AppendUint16(b, p.Type)
	}
	for _, p := range params {
		if byte(p.Type) == 6 {
			continue
		}
		if p.LengthEncoded {
			b = appendLenEnc(b, uint64(len(p.Data)))
		}
		b = append(b, p.Data...)
	}
	return b, nil
}

// DecodeBinaryRow returns field slices that alias data; nil denotes SQL NULL.
func DecodeBinaryRow(data []byte, columns []Column) ([][]byte, error) {
	bitmapLen := (len(columns) + 9) / 8
	if len(data) < 1+bitmapLen || data[0] != 0 {
		return nil, ErrMalformed
	}
	bitmap := data[1 : 1+bitmapLen]
	r := decoder{data: data, offset: 1 + bitmapLen}
	fields := make([][]byte, len(columns))
	for i, c := range columns {
		if bitmap[(i+2)/8]&(1<<uint((i+2)%8)) != 0 || c.Type == 6 {
			continue
		}
		switch c.Type {
		case 1:
			fields[i] = r.take(1)
		case 2, 13:
			fields[i] = r.take(2)
		case 3, 4, 9:
			fields[i] = r.take(4)
		case 5, 8:
			fields[i] = r.take(8)
		case 7, 10, 11, 12, 14:
			n := int(r.u8())
			fields[i] = r.take(n)
		default:
			fields[i] = r.lenBytes()
		}
	}
	if r.err != nil || r.remaining() != 0 {
		return nil, ErrMalformed
	}
	return fields, nil
}
