package wire

import "encoding/binary"

// LOBLocator describes OceanBase V1/V2 data; Data aliases the input packet.
type LOBLocator struct {
	Version, Kind byte
	ReadOnly      bool
	InRow         bool
	ByteLength    uint64
	Data          []byte
}

// DecodeLOBLocator validates locator boundaries before exposing an inline payload.
func DecodeLOBLocator(data []byte) (LOBLocator, error) {
	if len(data) < 8 {
		return LOBLocator{}, ErrMalformed
	}
	// OceanBase 4.4 uses the disk LOB common header in place of the legacy magic.
	magic := binary.LittleEndian.Uint32(data)
	if magic != 0x4c4f4221 && magic != 0x00018401 {
		return LOBLocator{}, ErrMalformed
	}
	version := binary.LittleEndian.Uint32(data[4:])
	l := LOBLocator{Version: byte(version)}
	if version == 1 {
		if len(data) < 40 {
			return l, ErrMalformed
		}
		return inlineLOB(l, data, 40+uint64(binary.LittleEndian.Uint32(data[32:])), uint64(binary.LittleEndian.Uint32(data[36:])))
	}
	if l.Version != 2 {
		return l, ErrMalformed
	}
	l.Kind = byte(version >> 8 & 15)
	flags := version >> 12
	if l.Kind > 2 || flags&^uint32(31) != 0 {
		return l, ErrMalformed
	}
	l.ReadOnly = flags&1 != 0
	l.InRow = flags&2 != 0
	extern, simple := flags&16 != 0, flags&8 != 0
	if l.InRow && extern {
		if len(data) < 40 {
			return l, ErrMalformed
		}
		return inlineLOB(l, data, 40+uint64(binary.LittleEndian.Uint32(data[32:])), uint64(binary.LittleEndian.Uint32(data[36:])))
	}
	if l.InRow && simple {
		return inlineLOB(l, data, 8, uint64(len(data)-8))
	}
	if simple {
		return l, ErrMalformed
	}
	return diskLOB(l, data, extern)
}

func diskLOB(l LOBLocator, data []byte, extern bool) (LOBLocator, error) {
	offset := uint64(8)
	if extern {
		if len(data) < 42 {
			return l, ErrMalformed
		}
		offset = 42 + uint64(binary.LittleEndian.Uint16(data[30:])) + uint64(binary.LittleEndian.Uint16(data[40:]))
	}
	if offset > uint64(len(data)) || uint64(len(data))-offset < 4 {
		return l, ErrMalformed
	}
	common := uint32(data[offset+1]) | uint32(data[offset+2])<<8 | uint32(data[offset+3])<<16
	if l.InRow && common&1 == 0 {
		return inlineLOB(l, data, offset+4, uint64(len(data))-offset-4)
	}
	if uint64(len(data))-offset < 28 || common&1 == 0 {
		return l, ErrMalformed
	}
	l.ByteLength = binary.LittleEndian.Uint64(data[offset+20:])
	if !l.InRow {
		return l, nil
	}
	offset += 28
	if common&4 == 0 {
		offset += 56
	}
	return inlineLOB(l, data, offset, l.ByteLength)
}

func inlineLOB(l LOBLocator, data []byte, offset, size uint64) (LOBLocator, error) {
	if offset > uint64(len(data)) || size != uint64(len(data))-offset {
		return l, ErrMalformed
	}
	l.InRow, l.ByteLength, l.Data = true, size, data[offset:]
	return l, nil
}
