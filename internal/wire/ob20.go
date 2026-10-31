package wire

import (
	"encoding/binary"
)

// OB20Options describes framing already negotiated on this physical connection.
type OB20Options struct {
	ConnectionID    uint32
	Checksum        bool
	RequireChecksum bool
	NewExtra        bool
	FullLinkTrace   bool
}

// EnableOB20 selects uncompressed OB20 envelopes after authentication.
func (p *PacketIO) EnableOB20(options OB20Options) error { return p.enableFrames(&options) }

func (f *frameIO) readOB20() error {
	var h [31]byte
	if err := f.readFull(h[:]); err != nil {
		return err
	}
	n := int3(h[:3])
	payload := binary.LittleEndian.Uint32(h[19:])
	if n < 28 || payload != uint32(n-28) {
		return ErrMalformed
	}
	if int3(h[4:]) != 0 || binary.LittleEndian.Uint16(h[7:]) != 0x20ab || binary.LittleEndian.Uint16(h[9:]) != 20 || binary.LittleEndian.Uint32(h[11:]) != f.ob.ConnectionID || uint32(int3(h[15:])) != f.requestID {
		return ErrMalformed
	}
	if h[3] != f.sequence || h[18] != f.obSequence {
		return ErrSequence
	}
	checksum := binary.LittleEndian.Uint16(h[29:])
	if (checksum != 0 || f.ob.RequireChecksum) && checksum != obCRC16(h[:29]) {
		return ErrChecksum
	}
	// Unknown flag bits are tolerated so future server flags cannot break frame parsing.
	flags := binary.LittleEndian.Uint32(h[23:])
	f.sequence++
	f.obSequence++
	data := make([]byte, int(payload))
	if err := f.readFull(data); err != nil {
		return err
	}
	var tail [4]byte
	if err := f.readFull(tail[:]); err != nil {
		return err
	}
	sum := binary.LittleEndian.Uint32(tail[:])
	if (sum != 0 || f.ob.RequireChecksum) && sum != obCRC32(data) {
		return ErrChecksum
	}
	if flags&1 != 0 {
		if len(data) < 4 {
			return ErrMalformed
		}
		width := binary.LittleEndian.Uint32(data)
		if uint64(width) > uint64(len(data)-4) {
			return ErrMalformed
		}
		extra := data[4 : 4+int(width)]
		if err := f.decodeTraceExtra(extra, flags&8 != 0); err != nil {
			return err
		}
		data = data[4+int(width):]
	}
	f.buffer = data
	return nil
}

func (f *frameIO) encodeOB20(data []byte, last bool) ([]byte, error) {
	var extraFlag uint32
	if len(f.outExtra) > 0 {
		payload := binary.LittleEndian.AppendUint32(nil, uint32(len(f.outExtra)))
		payload = append(payload, f.outExtra...)
		data = append(payload, data...)
		f.outExtra = nil // never resend after partial transport failure
		extraFlag = 1
	}
	if len(data) > maxFrame-28 {
		return nil, ErrMessageTooLarge
	}
	size := uint32(len(data) + 28)
	h := make([]byte, 31, 31+len(data)+4)
	put3(h, size)
	h[3] = f.sequence
	binary.LittleEndian.PutUint16(h[7:], 0x20ab)
	binary.LittleEndian.PutUint16(h[9:], 20)
	binary.LittleEndian.PutUint32(h[11:], f.ob.ConnectionID)
	put3(h[15:], f.requestID)
	h[18] = f.obSequence
	binary.LittleEndian.PutUint32(h[19:], uint32(len(data)))
	flags := extraFlag
	if last {
		flags |= 2
	}
	if f.ob.NewExtra {
		flags |= 8
	}
	binary.LittleEndian.PutUint32(h[23:], flags)
	var tail uint32
	if f.ob.Checksum || f.ob.RequireChecksum {
		binary.LittleEndian.PutUint16(h[29:], obCRC16(h[:29]))
		tail = obCRC32(data)
	}
	h = append(h, data...)
	f.sequence++
	f.obSequence++
	return binary.LittleEndian.AppendUint32(h, tail), nil
}
