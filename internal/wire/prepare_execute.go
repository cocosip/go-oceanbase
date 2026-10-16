package wire

import (
	"encoding/binary"
	"math"
)

// PrepareExecuteOK describes OceanBase's combined prepare/execute response.
type PrepareExecuteOK struct {
	PrepareOK
	Flags   uint32
	HasRows bool
}

// DecodePrepareExecuteOK parses the fixed 17-byte combined command header.
func DecodePrepareExecuteOK(data []byte) (PrepareExecuteOK, error) {
	if len(data) != 17 || data[0] != 0 || data[9] != 0 || data[16] > 1 {
		return PrepareExecuteOK{}, ErrMalformed
	}
	return PrepareExecuteOK{
		PrepareOK: PrepareOK{ID: binary.LittleEndian.Uint32(data[1:]), Columns: binary.LittleEndian.Uint16(data[5:]), Parameters: binary.LittleEndian.Uint16(data[7:]), Warnings: binary.LittleEndian.Uint16(data[10:])},
		Flags:     binary.LittleEndian.Uint32(data[12:]), HasRows: data[16] == 1,
	}, nil
}

// EncodePrepareExecute sends fresh types, one iteration, and no cached checksum.
func EncodePrepareExecute(id uint32, query []byte, params []Parameter, limit int64) ([]byte, error) {
	q := Parameter{Data: query, LengthEncoded: true}
	overhead := q.EncodedSize() + 20
	if overhead > limit {
		return nil, ErrMessageTooLarge
	}
	execute, err := EncodeStmtExecute(id, params, limit-overhead)
	if err != nil {
		return nil, err
	}
	if int64(len(execute))+overhead > int64(math.MaxInt) {
		return nil, ErrMessageTooLarge
	}
	b := make([]byte, 10, len(execute)+int(overhead))
	copy(b, execute[:10])
	b[0] = 161
	b = appendLenEnc(b, uint64(len(query)))
	b = append(b, query...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(params)))
	b = append(b, execute[10:]...)
	return append(b, make([]byte, 16)...), nil
}
