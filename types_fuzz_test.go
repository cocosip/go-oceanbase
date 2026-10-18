package oceanbase

import (
	"math/big"
	"strings"
	"testing"
	"time"
)

func FuzzScalars(f *testing.F) {
	for _, text := range []string{"0", "+.5e+0", "1.", "1e" + strings.Repeat("0", 400), "1e-130", "-9.99e125", "98765432109876543210.987654321", "1;SELECT", "1e2147483647", ".", "2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT", "-999999998 23:59:59.987654321"} {
		f.Add(text, []byte(text), byte(200))
	}
	for _, text := range []string{ltzSummerNewYork, ltzSpringGap, "0001-01-01 00:00:00 +14:00", "9999-12-31 23:59:59 -12:00"} {
		f.Add(text, []byte(text), byte(201))
	}
	f.Fuzz(func(t *testing.T, text string, data []byte, typ byte) {
		if len(text) > 512 || len(data) > 512 {
			return
		}
		limit := int64(typ) * 2
		if plain, err := plainNumber(text, limit); err == nil {
			if strings.ContainsAny(plain, "eE") || int64(len(plain)) > limit {
				t.Fatal("nondecimal literal", plain)
			}
			original, ok := new(big.Rat).SetString(text)
			got, good := new(big.Rat).SetString(plain)
			if !ok || !good || got.Cmp(original) != 0 {
				t.Fatal("numeric value changed", text, plain)
			}
		}
		_, _ = parseTimestamp(text)
		if _, err := parseLTZ(text); err == nil {
			options := SessionOptions{Location: time.UTC}
			_, serverErr := encodeParameter(TimestampLTZ(text), ModeOracle, options, 1024)
			c := &conn{cfg: Config{Session: options}, session: &handshakeSession{mode: ModeOracle}}
			_, clientErr := c.clientLiteral(TimestampLTZ(text), 1024)
			if (serverErr == nil) != (clientErr == nil) {
				t.Fatal("LTZ prepare acceptance differs", text, serverErr, clientErr)
			}
		}
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
