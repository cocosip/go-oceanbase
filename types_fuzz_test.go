package oceanbase

import (
	"math/big"
	"strings"
	"testing"
	"time"
)

func FuzzScalars(f *testing.F) {
	for _, text := range []string{"0", "1e-130", "-9.99e125", "98765432109876543210.987654321", "1;SELECT", "1e2147483647", ".", "2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT", "-999999998 23:59:59.987654321"} {
		f.Add(text, []byte(text), byte(200))
	}
	f.Fuzz(func(t *testing.T, text string, data []byte, typ byte) {
		if len(text) > 512 || len(data) > 512 {
			return
		}
		if plain, err := plainNumber(text, 512); err == nil {
			if strings.ContainsAny(plain, "eE") || len(plain) > 512 {
				t.Fatal("nondecimal literal", plain)
			}
			original, ok := new(big.Rat).SetString(text)
			got, good := new(big.Rat).SetString(plain)
			if !ok || !good || got.Cmp(original) != 0 {
				t.Fatal("numeric value changed", text, plain)
			}
		}
		_, _ = parseTimestamp(text)
		_, _ = parseLTZ(text)
		_, _ = parseInterval(text, false)
		_, _ = parseInterval(text, true)
		types := [...]byte{200, 201, 202, 204, 205}
		kind := types[int(typ)%len(types)]
		if decoded, err := decodeOracleTemporal(kind, data, time.UTC); err == nil {
			var parseErr error
			switch kind {
			case 200:
				_, parseErr = TimestampTZ(decoded).Value()
			case 201:
				_, parseErr = TimestampLTZ(decoded).Value()
			case 202:
				_, _, parseErr = parseWall(decoded)
			case 204:
				_, parseErr = IntervalYM(decoded).Value()
			case 205:
				_, parseErr = IntervalDS(decoded).Value()
			}
			if parseErr != nil {
				t.Fatal("invalid decoded temporal", decoded, parseErr)
			}
		}
	})
}
