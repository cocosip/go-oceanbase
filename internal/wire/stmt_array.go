package wire

import "math"

// StmtArraySize bounds the complete column-wise packet using scalar encoded body sizes.
func StmtArraySize(rows, parameters int, dataBytes int64) (int64, error) {
	if rows < 1 || rows > 65535 || parameters < 1 || parameters > 65535 || dataBytes < 0 {
		return 0, ErrMalformed
	}
	header := int64(10 + (parameters+7)/8 + 1)
	header += int64(parameters) * int64(6+len(appendLenEnc(nil, uint64(rows)))+(rows+7)/8)
	if dataBytes > int64(math.MaxInt)-header {
		return 0, ErrMessageTooLarge
	}
	return header + dataBytes, nil
}

// EncodeStmtExecuteArray uses Oracle array binding with saved per-input exceptions.
// Each declaration is fresh; anonymous element metadata cannot carry unsigned flags.
func EncodeStmtExecuteArray(id uint32, rows [][]Parameter, limit int64, options ExecuteOptions) ([]byte, error) {
	if len(rows) < 1 || len(rows) > 65535 || len(rows[0]) < 1 || len(rows[0]) > 65535 {
		return nil, ErrMalformed
	}
	width := len(rows[0])
	types := make([]uint16, width)
	for i := range types {
		types[i] = 6
	}
	var dataBytes int64
	for _, row := range rows {
		if len(row) != width {
			return nil, ErrMalformed
		}
		for i, parameter := range row {
			if parameter.Type > 255 || parameter.Type == 160 || len(parameter.TypeMetadata) != 0 || parameter.LongData {
				return nil, ErrMalformed
			}
			if parameter.Type != 6 {
				if types[i] != 6 && types[i] != parameter.Type {
					return nil, ErrMalformed
				}
				types[i] = parameter.Type
			}
			if parameter.EncodedSize() > limit-dataBytes {
				return nil, ErrMessageTooLarge
			}
			dataBytes += parameter.EncodedSize()
		}
	}
	size, err := StmtArraySize(len(rows), width, dataBytes)
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, ErrMessageTooLarge
	}
	columns := make([]Parameter, width)
	for i, element := range types {
		columns[i] = Parameter{Type: 160, TypeMetadata: []byte{0, 0, 0, byte(element)}}
	}
	options.ReuseTypes = false
	header, err := EncodeStmtExecuteOptions(id, columns, limit, options)
	if err != nil {
		return nil, err
	}
	payload := make([]byte, len(header), int(size))
	copy(payload, header)
	payload[5] = 8 | 16
	for column := range width {
		payload = appendLenEnc(payload, uint64(len(rows)))
		bitmap := len(payload)
		payload = append(payload, make([]byte, (len(rows)+7)/8)...)
		for i, row := range rows {
			parameter := row[column]
			if parameter.Type == 6 {
				payload[bitmap+i/8] |= 1 << uint(i%8)
				continue
			}
			if parameter.LengthEncoded {
				payload = appendLenEnc(payload, uint64(len(parameter.Data)))
			}
			payload = append(payload, parameter.Data...)
		}
	}
	return payload, nil
}
