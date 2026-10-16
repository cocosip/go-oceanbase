package oceanbase

import (
	"encoding/binary"
	"fmt"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Error preserves the server's error code, SQLSTATE and message in either mode.
// Message may contain SQL or sensitive values; the driver does not log it.
type Error struct {
	Code     uint32
	SQLState string
	Message  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("oceanbase: server error %d (%s): %s", e.Code, e.SQLState, e.Message)
}

func decodeServerError(data []byte) error {
	if len(data) < 3 || data[0] != 0xff {
		return wire.ErrMalformed
	}
	result := &Error{Code: uint32(binary.LittleEndian.Uint16(data[1:])), SQLState: "HY000"}
	message := data[3:]
	// Connection-limit errors may use the legacy format before capability negotiation.
	if len(message) > 0 && message[0] == '#' {
		if len(message) < 6 {
			return wire.ErrMalformed
		}
		result.SQLState = string(message[1:6])
		message = message[6:]
	}
	result.Message = string(message)
	return result
}
