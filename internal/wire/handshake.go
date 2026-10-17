package wire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"strings"
)

// ErrMalformed indicates a truncated, invalid or unsupported protocol structure.
var ErrMalformed = errors.New("wire: malformed protocol message")

// Client capability flags used by the handshake and result terminators.
const (
	ClientLongPassword      uint64 = 1
	ClientFoundRows         uint64 = 1 << 1
	ClientConnectWithDB     uint64 = 1 << 3
	ClientCompress          uint64 = 1 << 5
	ClientLocalFiles        uint64 = 1 << 7
	ClientProtocol41        uint64 = 1 << 9
	ClientSSL               uint64 = 1 << 11
	ClientTransactions      uint64 = 1 << 13
	ClientSecureConnection  uint64 = 1 << 15
	ClientMultiStatements   uint64 = 1 << 16
	ClientMultiResults      uint64 = 1 << 17
	ClientPSMultiResults    uint64 = 1 << 18
	ClientPluginAuth        uint64 = 1 << 19
	ClientConnectAttrs      uint64 = 1 << 20
	ClientPluginAuthLenEnc  uint64 = 1 << 21
	ClientSessionTrack      uint64 = 1 << 23
	ClientDeprecateEOF      uint64 = 1 << 24
	ClientSupportOracle     uint64 = 1 << 27
	ClientSupportLOBLocator uint64 = 1 << 29
)

// Handshake contains wire values; mode and effective feature policy belong to the driver.
type Handshake struct {
	ServerVersion string
	ConnectionID  uint32
	Capabilities  uint64
	Charset       byte
	Status        uint16
	Plugin        string
	Challenge     []byte
}

// HandshakeResponse contains only fields explicitly negotiated by the driver.
type HandshakeResponse struct {
	Capabilities           uint64
	MaxPacketBytes         uint32
	Charset                byte
	User, Database, Plugin string
	Auth                   []byte
	Attributes             map[string]string
}

// OK contains a command's counts, status and optional session tracking data.
type OK struct {
	AffectedRows, InsertID uint64
	Status, Warnings       uint16
	Info, State            []byte
}

// DecodeHandshake parses a protocol-v10 server greeting.
func DecodeHandshake(data []byte) (Handshake, error) {
	r := decoder{data: data}
	if r.u8() != 10 {
		return Handshake{}, ErrMalformed
	}
	h := Handshake{ServerVersion: string(r.nul()), Plugin: "mysql_native_password"}
	if len(h.ServerVersion) == 0 || len(h.ServerVersion) > 256 {
		return Handshake{}, ErrMalformed
	}
	h.ConnectionID = r.u32()
	h.Challenge = append([]byte(nil), r.take(8)...)
	if r.u8() != 0 {
		return Handshake{}, ErrMalformed
	}
	h.Capabilities = uint64(r.u16())
	if r.err != nil {
		return Handshake{}, r.err
	}
	if r.remaining() == 0 {
		return h, nil
	}
	h.Charset = r.u8()
	h.Status = r.u16()
	h.Capabilities |= uint64(r.u16()) << 16
	authLength := int(r.u8())
	reserved := r.take(10)
	if r.err != nil {
		return Handshake{}, r.err
	}
	if h.Capabilities&ClientLongPassword == 0 {
		h.Capabilities |= uint64(binary.LittleEndian.Uint32(reserved[6:])) << 32
	}
	if h.Capabilities&ClientSecureConnection != 0 {
		length := 13
		if h.Capabilities&ClientPluginAuth != 0 {
			length = max(length, authLength-8)
		}
		rest := r.take(length)
		if len(rest) > 0 && rest[len(rest)-1] == 0 {
			rest = rest[:len(rest)-1]
		}
		h.Challenge = append(h.Challenge, rest...)
	}
	if h.Capabilities&ClientPluginAuth != 0 {
		h.Plugin = string(r.nul())
		if len(h.Plugin) == 0 || len(h.Plugin) > 128 {
			return Handshake{}, ErrMalformed
		}
	}
	if r.err != nil {
		return Handshake{}, r.err
	}
	return h, nil
}

// EncodeHandshakeResponse encodes the protocol-4.1 client authentication response.
func EncodeHandshakeResponse(h HandshakeResponse) ([]byte, error) {
	if strings.ContainsRune(h.User, 0) || strings.ContainsRune(h.Database, 0) || strings.ContainsRune(h.Plugin, 0) {
		return nil, ErrMalformed
	}
	if h.Capabilities&ClientPluginAuthLenEnc == 0 && len(h.Auth) > 255 {
		return nil, ErrMalformed
	}
	data := make([]byte, 32)
	binary.LittleEndian.PutUint32(data, uint32(h.Capabilities))
	binary.LittleEndian.PutUint32(data[4:], h.MaxPacketBytes)
	data[8] = h.Charset
	binary.LittleEndian.PutUint32(data[28:], uint32(h.Capabilities>>32))
	data = append(data, h.User...)
	data = append(data, 0)
	if h.Capabilities&ClientPluginAuthLenEnc != 0 {
		data = appendLenEnc(data, uint64(len(h.Auth)))
		data = append(data, h.Auth...)
	} else if h.Capabilities&ClientSecureConnection != 0 {
		data = append(data, byte(len(h.Auth)))
		data = append(data, h.Auth...)
	} else {
		if bytes.IndexByte(h.Auth, 0) >= 0 {
			return nil, ErrMalformed
		}
		data = append(data, h.Auth...)
		data = append(data, 0)
	}
	if h.Capabilities&ClientConnectWithDB != 0 {
		data = append(data, h.Database...)
		data = append(data, 0)
	}
	if h.Capabilities&ClientPluginAuth != 0 {
		data = append(data, h.Plugin...)
		data = append(data, 0)
	}
	if h.Capabilities&ClientConnectAttrs != 0 {
		var attributes []byte
		keys := make([]string, 0, len(h.Attributes))
		for key := range h.Attributes {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			attributes = appendLenEnc(attributes, uint64(len(key)))
			attributes = append(attributes, key...)
			value := h.Attributes[key]
			attributes = appendLenEnc(attributes, uint64(len(value)))
			attributes = append(attributes, value...)
		}
		data = appendLenEnc(data, uint64(len(attributes)))
		data = append(data, attributes...)
	}
	return data, nil
}

// DecodeOK parses an OK packet according to the negotiated client capabilities.
func DecodeOK(data []byte, capabilities uint64) (OK, error) {
	r := decoder{data: data}
	header := r.u8()
	if header != 0 && header != 0xfe {
		return OK{}, ErrMalformed
	}
	ok := OK{AffectedRows: r.lenEnc(), InsertID: r.lenEnc()}
	if capabilities&ClientProtocol41 != 0 {
		ok.Status = r.u16()
		ok.Warnings = r.u16()
	} else if capabilities&ClientTransactions != 0 {
		ok.Status = r.u16()
	}
	if capabilities&ClientSessionTrack != 0 {
		if r.remaining() > 0 {
			ok.Info = r.lenBytes()
		}
		if ok.Status&0x4000 != 0 {
			if r.remaining() == 0 {
				return OK{}, ErrMalformed
			}
			ok.State = r.lenBytes()
		}
	} else {
		ok.Info = r.take(r.remaining())
	}
	if r.err != nil || r.remaining() != 0 {
		return OK{}, ErrMalformed
	}
	return ok, nil
}

type decoder struct {
	data   []byte
	offset int
	err    error
}

func (r *decoder) remaining() int { return len(r.data) - r.offset }
func (r *decoder) take(n int) []byte {
	if r.err != nil || n < 0 || n > r.remaining() {
		r.err = ErrMalformed
		return nil
	}
	b := r.data[r.offset : r.offset+n]
	r.offset += n
	return b
}
func (r *decoder) u8() byte {
	b := r.take(1)
	if len(b) < 1 {
		return 0
	}
	return b[0]
}
func (r *decoder) u16() uint16 {
	b := r.take(2)
	if len(b) < 2 {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}
func (r *decoder) u32() uint32 {
	b := r.take(4)
	if len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}
func (r *decoder) nul() []byte {
	if r.err != nil {
		return nil
	}
	n := bytes.IndexByte(r.data[r.offset:], 0)
	if n < 0 {
		r.err = ErrMalformed
		return nil
	}
	b := r.take(n)
	r.take(1)
	return b
}
func (r *decoder) lenEnc() uint64 {
	first := r.u8()
	switch first {
	case 0xfb, 0xff:
		r.err = ErrMalformed
		return 0
	case 0xfc:
		return uint64(r.u16())
	case 0xfd:
		b := r.take(3)
		if len(b) < 3 {
			return 0
		}
		return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16
	case 0xfe:
		b := r.take(8)
		if len(b) < 8 {
			return 0
		}
		return binary.LittleEndian.Uint64(b)
	default:
		return uint64(first)
	}
}
func (r *decoder) lenBytes() []byte {
	n := r.lenEnc()
	if r.err != nil || n > uint64(r.remaining()) {
		r.err = ErrMalformed
		return nil
	}
	return r.take(int(n))
}
func appendLenEnc(data []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(data, byte(n))
	case n <= 0xffff:
		return append(data, 0xfc, byte(n), byte(n>>8))
	case n <= 0xffffff:
		return append(data, 0xfd, byte(n), byte(n>>8), byte(n>>16))
	default:
		data = append(data, 0xfe)
		return binary.LittleEndian.AppendUint64(data, n)
	}
}
