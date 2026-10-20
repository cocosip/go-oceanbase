package valuecodec

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/types"
)

func FuzzScalars(f *testing.F) {
	for _, text := range []string{"0", "+.5e+0", "1.", "1e" + strings.Repeat("0", 400), "1e-130", "-9.99e125", "98765432109876543210.987654321", "1;SELECT", "1e2147483647", ".", "2024-11-03 01:30:00.123456789 -04:00 America/New_York EDT", "-999999998 23:59:59.987654321"} {
		f.Add(text, []byte(text), byte(200))
	}
	for _, text := range []string{ltzSummerNewYork, ltzSpringGap, "0001-01-01 00:00:00 +14:00", "9999-12-31 23:59:59 -12:00"} {
		f.Add(text, []byte(text), byte(201))
	}
	for _, text := range []string{"中", "😀", "€", "A", "\xff"} {
		for selector := range byte(8) {
			f.Add(text, []byte(text), selector)
		}
	}
	f.Fuzz(func(t *testing.T, text string, data []byte, typ byte) {
		if len(text) > 512 || len(data) > 512 {
			return
		}
		limit := int64(typ) * 2
		if plain, err := valueformat.PlainNumber(text, limit); err == nil {
			if strings.ContainsAny(plain, "eE") || int64(len(plain)) > limit {
				t.Fatal("nondecimal literal", plain)
			}
			original, ok := new(big.Rat).SetString(text)
			got, good := new(big.Rat).SetString(plain)
			if !ok || !good || got.Cmp(original) != 0 {
				t.Fatal("numeric value changed", text, plain)
			}
		}
		_, _ = valueformat.ParseTimestamp(text)
		if _, err := valueformat.ParseLTZ(text); err == nil {
			options := config.SessionOptions{Location: time.UTC}
			_, serverErr := EncodeParameter(types.TimestampLTZ(text), config.ModeOracle, options, 1024)
			_, clientErr := ClientLiteral(types.TimestampLTZ(text), config.ModeOracle, options, 1024, 1024)
			if (serverErr == nil) != (clientErr == nil) {
				t.Fatal("LTZ prepare acceptance differs", text, serverErr, clientErr)
			}
		}
		_, _ = valueformat.ParseInterval(text, false)
		_, _ = valueformat.ParseInterval(text, true)
		charsets := [...]string{"", charsetUTF8MB4, charsetUTF8MB3, charsetGBK, charsetLatin1, charsetUTF16, charsetASCII, charsetGB18030}
		options := config.SessionOptions{Charset: charsetGBK, NCharset: charsets[int(typ)%len(charsets)]}
		_, serverErr := EncodeParameter(types.NString(text), config.ModeOracle, options, 8192)
		_, clientErr := ClientLiteral(types.NString(text), config.ModeOracle, options, 8192, 8192)
		if (serverErr == nil) != (clientErr == nil) {
			t.Fatal("types.NString prepare acceptance differs", options.NCharset, text, serverErr, clientErr)
		}
		kinds := [...]byte{200, 201, 202, 204, 205}
		kind := kinds[int(typ)%len(kinds)]
		if decoded, err := DecodeOracleTemporal(kind, data, time.UTC); err == nil {
			var parseErr error
			switch kind {
			case 200:
				_, parseErr = types.TimestampTZ(decoded).Value()
			case 201:
				_, parseErr = types.TimestampLTZ(decoded).Value()
			case 202:
				_, _, parseErr = valueformat.ParseWall(decoded)
			case 204:
				_, parseErr = types.IntervalYM(decoded).Value()
			case 205:
				_, parseErr = types.IntervalDS(decoded).Value()
			}
			if parseErr != nil {
				t.Fatal("invalid decoded temporal", decoded, parseErr)
			}
		}
	})
}
