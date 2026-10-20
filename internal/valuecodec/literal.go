package valuecodec

import (
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// OutputVariable is a validated variable name supplied by call binding.
type OutputVariable string

// LongPlaceholder retains a server-bound streaming parameter slot.
type LongPlaceholder struct{}

// ClientLiteral renders a bounded scalar SQL literal; callers reject streaming inputs first.
func ClientLiteral(v any, mode config.Mode, options config.SessionOptions, maxLOBBytes, limit int64) (string, error) {
	var s string
	switch v := v.(type) {
	case LongPlaceholder:
		return "?", nil
	case OutputVariable:
		if int64(len(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		return string(v), nil
	case types.Number:
		return valueformat.PlainNumber(string(v), limit)
	case types.NString, types.Raw, types.RowID, types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
		if mode != config.ModeOracle {
			return "", dberrors.ErrUnsupported
		}
		return scalarLiteral(v, mode, options, maxLOBBytes, limit)
	case nil:
		s = nullTypeName
	case int64:
		s = strconv.FormatInt(v, 10)
	case uint64:
		s = strconv.FormatUint(v, 10)
	case bool:
		s = "0"
		if v {
			s = "1"
		}
	case float64:
		var err error
		s, err = clientFloat(v, mode)
		if err != nil {
			return "", err
		}
	case string:
		if int64(utf8.RuneCountInString(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		encoded, err := charset.Encode(options.Charset, v)
		if err != nil {
			return "", err
		}
		if mode == config.ModeMySQL {
			name := strings.ToLower(options.Charset)
			if name == charsetUTF8MB3 {
				name = "utf8"
			}
			prefix := "_" + name + " X'"
			if limit < int64(len(prefix)+1) || int64(len(encoded)) > (limit-int64(len(prefix)+1))/2 {
				return "", wire.ErrMessageTooLarge
			}
			return prefix + hex.EncodeToString(encoded) + "'", nil
		}
		return quoteClientString(v, len(encoded), limit)
	case []byte:
		if v == nil {
			s = nullTypeName
			break
		}
		prefix, suffix := "X'", "'"
		if mode == config.ModeOracle {
			prefix, suffix = "HEXTORAW('", "')"
		}
		if int64(len(v)) > maxLOBBytes || int64(len(v)) > (limit-int64(len(prefix)+len(suffix)))/2 {
			return "", wire.ErrMessageTooLarge
		}
		s = prefix + hex.EncodeToString(v) + suffix
	case time.Time:
		var err error
		s, err = clientTimeLiteral(v, mode, options)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("%w: client parameter type", dberrors.ErrBind)
	}
	if int64(len(s)) > limit {
		return "", wire.ErrMessageTooLarge
	}
	return s, nil
}

func clientTimeLiteral(v time.Time, mode config.Mode, options config.SessionOptions) (string, error) {
	var s string
	v = v.In(options.Location)
	if v.Year() < 1 || v.Year() > 9999 || mode != config.ModeOracle && v.Nanosecond()%1000 != 0 {
		return "", fmt.Errorf("%w: datetime precision or range", dberrors.ErrBind)
	}
	s = "'" + v.Format("2006-01-02 15:04:05.000000") + "'"
	if v.Nanosecond()%1000 != 0 {
		s = "'" + v.Format(valueformat.TimestampLayout+".000000000") + "'"
	}
	if mode == config.ModeOracle {
		s = "TIMESTAMP " + s
	}
	return s, nil
}

func scalarLiteral(value any, mode config.Mode, options config.SessionOptions, maxLOBBytes, limit int64) (string, error) {
	// Value validates the representation and returns the exact text to bind, so
	// binding anything else would bypass that validation.
	validated, err := value.(driver.Valuer).Value()
	if err != nil {
		return "", err
	}
	if raw, ok := validated.([]byte); ok {
		return ClientLiteral(raw, mode, options, maxLOBBytes, limit)
	}
	text, ok := validated.(string)
	if !ok {
		return "", fmt.Errorf("%w: scalar text", dberrors.ErrType)
	}
	var literal string
	switch v := value.(type) {
	case types.RowID:
		literal = "CHARTOROWID(" + oracleQuoted(text) + ")"
	case types.NString:
		if int64(len(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		units := utf16.Encode([]rune(text))
		if limit < 10 || int64(len(units)) > (limit-10)/5 {
			return "", wire.ErrMessageTooLarge
		}
		name := options.NCharset
		if name == "" {
			name = options.Charset
		}
		if name == "" {
			name = charsetUTF8MB4
		}
		if _, err = charset.Encode(name, text); err != nil {
			return "", err
		}
		var b strings.Builder
		b.WriteString("UNISTR('")
		for _, unit := range units {
			fmt.Fprintf(&b, "\\%04X", unit)
		}
		b.WriteString("')")
		literal = b.String()
	case types.IntervalYM:
		literal = "INTERVAL " + oracleQuoted(text) + " YEAR(9) TO MONTH"
	case types.IntervalDS:
		literal = "INTERVAL " + oracleQuoted(text) + " DAY(9) TO SECOND(9)"
	case types.TimestampTZ:
		parts, parseErr := valueformat.ParseTimestamp(text)
		if parseErr != nil {
			return "", parseErr
		}
		literal = "TIMESTAMP " + oracleQuoted(formatTimestamp(parts.Instant, parts.Scale)+" "+parts.Instant.Format("-07:00"))
		if parts.Region != "" {
			literal = "(" + literal + " AT TIME ZONE " + oracleQuoted(parts.Region) + ")"
		}
	case types.TimestampLTZ:
		parts, parseErr := valueformat.ParseLTZ(text)
		if parseErr != nil {
			return "", parseErr
		}
		wall, err := ltzSessionWall(parts, options.Location)
		if err != nil {
			return "", err
		}
		stamp := formatTimestamp(parts.Instant, parts.Scale) + " " + parts.Instant.Format("-07:00")
		if parts.Local {
			stamp = formatTimestamp(wall, parts.Scale)
		}
		literal = "CAST(TIMESTAMP " + oracleQuoted(stamp) + " AS TIMESTAMP(9) WITH LOCAL TIME ZONE)"
	}
	if int64(len(literal)) > limit {
		return "", wire.ErrMessageTooLarge
	}
	return literal, nil
}

// oracleQuoted renders an Oracle single-quoted literal, doubling embedded quotes
// so a value cannot terminate the literal early.
func oracleQuoted(text string) string {
	return "'" + strings.ReplaceAll(text, "'", "''") + "'"
}

func formatTimestamp(t time.Time, scale int) string {
	text := t.Format(valueformat.TimestampLayout)
	if scale > 0 {
		fraction := fmt.Sprintf("%09d", t.Nanosecond())
		text += "." + fraction[:scale]
	}
	return text
}

func clientFloat(v float64, mode config.Mode) (string, error) {
	if !math.IsNaN(v) && !math.IsInf(v, 0) {
		s := strconv.FormatFloat(v, 'g', -1, 64)
		if mode == config.ModeOracle {
			return s + "d", nil
		}
		if !strings.ContainsAny(s, "eE") {
			s += "e0"
		}
		return s, nil
	}
	if mode != config.ModeOracle {
		return "", fmt.Errorf("%w: non-finite parameter", dberrors.ErrBind)
	}
	if math.IsNaN(v) {
		return "BINARY_DOUBLE_NAN", nil
	}
	if math.IsInf(v, -1) {
		return "-BINARY_DOUBLE_INFINITY", nil
	}
	return "BINARY_DOUBLE_INFINITY", nil
}

func quoteClientString(text string, encodedBytes int, limit int64) (string, error) {
	if int64(encodedBytes) > limit-2 {
		return "", wire.ErrMessageTooLarge
	}
	n := int64(encodedBytes) + 2
	hasNUL := strings.ContainsRune(text, 0)
	if hasNUL {
		n += 2
	}
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			n++
		case 0:
			n += 11
		}
		if n > limit {
			return "", wire.ErrMessageTooLarge
		}
	}
	if n > limit {
		return "", wire.ErrMessageTooLarge
	}
	var b strings.Builder
	if hasNUL {
		b.WriteByte('(')
	}
	b.WriteByte('\'')
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			b.WriteString("''")
		case 0:
			b.WriteString("'||CHR(0)||'")
		default:
			b.WriteByte(text[i])
		}
	}
	b.WriteByte('\'')
	if hasNUL {
		b.WriteByte(')')
	}
	return b.String(), nil
}
