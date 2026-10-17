package wire

import (
	"errors"
	"hash/crc32"
)

// ErrChecksum makes a corrupt frame distinguishable from SQL and transport errors.
var ErrChecksum = errors.New("wire: frame checksum mismatch")

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// SQLChecksum is the raw CRC32C used by OceanBase's Oracle statement protocol.
func SQLChecksum(query []byte) uint32 { return obCRC32(query) }

func obCRC32(data []byte) uint32 {
	var sum uint32
	// OceanBase uses the uncomplemented CRC32C state with a zero initial value.
	for _, b := range data {
		sum = castagnoli[byte(sum)^b] ^ sum>>8
	}
	return sum
}

func obCRC16(data []byte) uint16 {
	var sum uint16
	for _, b := range data {
		sum ^= uint16(b)
		for range 8 {
			if sum&1 != 0 {
				sum = sum>>1 ^ 0xa001
			} else {
				sum >>= 1
			}
		}
	}
	return sum
}
