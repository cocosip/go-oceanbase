package valueformat

import (
	"math"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// PlainNumber expands a decimal exponent within the output byte limit.
func PlainNumber(text string, limit int64) (string, error) {
	mantissa, exponent, err := DecimalParts(text)
	if err != nil {
		return "", err
	}
	sign := ""
	if mantissa[0] == '-' {
		sign = "-"
	}
	mantissa = strings.TrimPrefix(strings.TrimPrefix(mantissa, "-"), "+")
	point := int64(strings.IndexByte(mantissa, '.'))
	digitCount := int64(len(mantissa))
	if point < 0 {
		point = digitCount
	} else {
		digitCount--
	}
	point += exponent
	length := digitCount
	switch {
	case point <= 0:
		length += 2 - point
	case point < digitCount:
		length++
	default:
		length = point
	}
	if length+int64(len(sign)) > limit || length+int64(len(sign)) > int64(math.MaxInt) {
		return "", wire.ErrMessageTooLarge
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	if point <= 0 {
		return sign + "0." + strings.Repeat("0", int(-point)) + digits, nil
	}
	if point < int64(len(digits)) {
		return sign + digits[:int(point)] + "." + digits[int(point):], nil
	}
	return sign + digits + strings.Repeat("0", int(point)-len(digits)), nil
}

// DecimalParts validates decimal text without converting its significand.
func DecimalParts(text string) (string, int64, error) {
	mantissa := text
	var exponent int64
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		mantissa = text[:i]
		var err error
		exponent, err = strconv.ParseInt(text[i+1:], 10, 32)
		if err != nil {
			return "", 0, dberrors.ErrType
		}
	}
	unsigned := strings.TrimPrefix(strings.TrimPrefix(mantissa, "-"), "+")
	digits, dots := 0, 0
	for _, c := range unsigned {
		if c >= '0' && c <= '9' {
			digits++
			continue
		}
		if c == '.' {
			dots++
			continue
		}
		return "", 0, dberrors.ErrType
	}
	if digits == 0 || dots > 1 || strings.HasPrefix(mantissa, "-+") {
		return "", 0, dberrors.ErrType
	}
	return mantissa, exponent, nil
}
