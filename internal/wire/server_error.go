package wire

import "encoding/binary"

// ServerError contains the fields of an ERR packet, before public error mapping.
type ServerError struct {
	Code              uint32
	SQLState, Message string
}

// DecodeServerError accepts both pre-negotiation legacy and protocol-41 ERR packets.
func DecodeServerError(data []byte) (ServerError, error) {
	if len(data) < 3 || data[0] != 0xff {
		return ServerError{}, ErrMalformed
	}
	result := ServerError{Code: uint32(binary.LittleEndian.Uint16(data[1:])), SQLState: "HY000"}
	message := data[3:]
	if len(message) > 0 && message[0] == '#' {
		if len(message) < 6 {
			return ServerError{}, ErrMalformed
		}
		result.SQLState, message = string(message[1:6]), message[6:]
	}
	result.Message = string(message)
	return result, nil
}
