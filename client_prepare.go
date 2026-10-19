package oceanbase

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const sqlNullLiteral = "NULL"

type clientStmt struct {
	conn   *conn
	query  string
	closed atomic.Bool
	epoch  *resourceEpoch
}

func (c *conn) prepareClient(ctx context.Context, query string) (driver.Stmt, error) {
	if int64(utf8.RuneCountInString(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	defer c.unlock()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if _, err := buildBindPlanEscapes(query, c.session.mode, c.session.ok.Status&512 != 0); err != nil {
		return nil, err
	}
	return &clientStmt{conn: c, query: query, epoch: c.statementEpoch}, nil
}

func (s *clientStmt) Close() error { s.closed.Store(true); return nil }

// Session SQL modes may change between Prepare and Execute; validate at execution.
func (*clientStmt) NumInput() int { return -1 }
func (s *clientStmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *clientStmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}
func (s *clientStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	r, err := s.queryRows(ctx, args)
	if err != nil {
		return nil, err
	}
	if err = r.drainAll(ctx); err != nil {
		return nil, err
	}
	return r.last, nil
}
func (s *clientStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.queryRows(ctx, args)
}
func (s *clientStmt) queryRows(ctx context.Context, args []driver.NamedValue) (*textRows, error) {
	c := s.conn
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		c.unlock()
		return nil, driver.ErrBadConn
	}
	if s.closed.Load() {
		c.unlock()
		return nil, fmt.Errorf("oceanbase: statement closed")
	}
	if s.epoch != c.statementEpoch {
		c.unlock()
		return nil, ErrResource
	}
	if hasOutputArguments(args) {
		return c.queryOutputsLocked(ctx, s.query, args, nil)
	}
	plan, err := buildBindPlanEscapes(s.query, c.session.mode, c.session.ok.Status&512 != 0)
	var values []boundValue
	if err == nil {
		values, err = c.bindCommandArgumentsLocked(ctx, plan, args)
	}
	var query string
	if err == nil {
		query, err = c.interpolatePlan(plan, values)
	}
	if err != nil {
		c.unlock()
		return nil, err
	}
	return c.queryCommandLocked(ctx, append([]byte{3}, query...), false, nil)
}

// interpolate is called while owning the session gate, so status cannot change mid-encoding.
func (c *conn) interpolate(query string, args []driver.NamedValue) (string, error) {
	limit := c.cfg.Limits.MaxMessageBytes - 1
	if int64(utf8.RuneCountInString(query)) > limit {
		return "", wire.ErrMessageTooLarge
	}
	plan, err := buildBindPlanEscapes(query, c.session.mode, c.session.ok.Status&512 != 0)
	if err != nil {
		return "", err
	}
	values, err := bindArguments(plan, args)
	if err != nil {
		return "", err
	}
	return c.interpolatePlan(plan, values)
}

func (c *conn) interpolatePlan(plan bindPlan, values []boundValue) (string, error) {
	query := plan.query
	limit := c.cfg.Limits.MaxMessageBytes - 1
	var b strings.Builder
	offset := 0
	for i, slot := range plan.slots {
		span, err := c.encodeSQLLimit(query[offset:slot.start], limit-int64(b.Len()))
		if err != nil {
			return "", err
		}
		b.Write(span)
		literal, err := c.clientLiteral(values[i].value, limit-int64(b.Len())-2)
		if err != nil {
			return "", err
		}
		encoded, err := c.encodeSQLLimit(literal, limit-int64(b.Len())-2)
		if err != nil {
			return "", err
		}
		// Preserve token boundaries, including unary minus and numeric aliases.
		b.WriteByte(' ')
		b.Write(encoded)
		b.WriteByte(' ')
		offset = slot.end
	}
	tail, err := c.encodeSQLLimit(query[offset:], limit-int64(b.Len()))
	if err != nil {
		return "", err
	}
	b.Write(tail)
	return b.String(), nil
}

func (c *conn) clientLiteral(v any, limit int64) (string, error) {
	var s string
	switch v := v.(type) {
	case longPlaceholder:
		return "?", nil
	case *LongData:
		return "", fmt.Errorf("%w: long data requires server preparation", ErrUnsupported)
	case outputVariable:
		if int64(len(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		return string(v), nil
	case Number:
		return plainNumber(string(v), limit)
	case NString, Raw, RowID, TimestampTZ, TimestampLTZ, IntervalYM, IntervalDS:
		if c.session.mode != ModeOracle {
			return "", ErrUnsupported
		}
		return c.scalarLiteral(v, limit)
	case nil:
		s = sqlNullLiteral
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
		s, err = clientFloat(v, c.session.mode)
		if err != nil {
			return "", err
		}
	case string:
		if int64(utf8.RuneCountInString(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		encoded, err := charset.Encode(c.cfg.Session.Charset, v)
		if err != nil {
			return "", err
		}
		if c.session.mode == ModeMySQL {
			name := strings.ToLower(c.cfg.Session.Charset)
			if name == charsetUTF8MB3Alias {
				name = charsetUTF8
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
			s = sqlNullLiteral
			break
		}
		prefix, suffix := "X'", "'"
		if c.session.mode == ModeOracle {
			prefix, suffix = "HEXTORAW('", "')"
		}
		if int64(len(v)) > c.cfg.Limits.MaxLOBBytes || int64(len(v)) > (limit-int64(len(prefix)+len(suffix)))/2 {
			return "", wire.ErrMessageTooLarge
		}
		s = prefix + hex.EncodeToString(v) + suffix
	case time.Time:
		var err error
		s, err = c.clientTimeLiteral(v)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("%w: client parameter type", ErrBind)
	}
	if int64(len(s)) > limit {
		return "", wire.ErrMessageTooLarge
	}
	return s, nil
}

func (c *conn) clientTimeLiteral(v time.Time) (string, error) {
	var s string
	v = v.In(c.cfg.Session.Location)
	if v.Year() < 1 || v.Year() > 9999 || c.session.mode != ModeOracle && v.Nanosecond()%1000 != 0 {
		return "", fmt.Errorf("%w: datetime precision or range", ErrBind)
	}
	s = "'" + v.Format("2006-01-02 15:04:05.000000") + "'"
	if v.Nanosecond()%1000 != 0 {
		s = "'" + v.Format(timestampLayout+".000000000") + "'"
	}
	if c.session.mode == ModeOracle {
		s = "TIMESTAMP " + s
	}
	return s, nil
}

func (c *conn) scalarLiteral(value any, limit int64) (string, error) {
	// Value validates the representation and returns the exact text to bind, so
	// binding anything else would bypass that validation.
	validated, err := value.(driver.Valuer).Value()
	if err != nil {
		return "", err
	}
	if raw, ok := validated.([]byte); ok {
		return c.clientLiteral(raw, limit)
	}
	text, ok := validated.(string)
	if !ok {
		return "", fmt.Errorf("%w: scalar text", ErrType)
	}
	var literal string
	switch v := value.(type) {
	case RowID:
		literal = "CHARTOROWID(" + oracleQuoted(text) + ")"
	case NString:
		if int64(len(v)) > limit {
			return "", wire.ErrMessageTooLarge
		}
		units := utf16.Encode([]rune(text))
		if limit < 10 || int64(len(units)) > (limit-10)/5 {
			return "", wire.ErrMessageTooLarge
		}
		name := c.cfg.Session.NCharset
		if name == "" {
			name = c.cfg.Session.Charset
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
	case IntervalYM:
		literal = "INTERVAL " + oracleQuoted(text) + " YEAR(9) TO MONTH"
	case IntervalDS:
		literal = "INTERVAL " + oracleQuoted(text) + " DAY(9) TO SECOND(9)"
	case TimestampTZ:
		parts, parseErr := parseTimestamp(text)
		if parseErr != nil {
			return "", parseErr
		}
		literal = "TIMESTAMP " + oracleQuoted(formatTimestamp(parts.instant, parts.scale)+" "+parts.instant.Format("-07:00"))
		if parts.region != "" {
			literal = "(" + literal + " AT TIME ZONE " + oracleQuoted(parts.region) + ")"
		}
	case TimestampLTZ:
		parts, parseErr := parseLTZ(text)
		if parseErr != nil {
			return "", parseErr
		}
		wall, err := ltzSessionWall(parts, c.cfg.Session.Location)
		if err != nil {
			return "", err
		}
		stamp := formatTimestamp(parts.instant, parts.scale) + " " + parts.instant.Format("-07:00")
		if parts.local {
			stamp = formatTimestamp(wall, parts.scale)
		}
		literal = "CAST(TIMESTAMP " + oracleQuoted(stamp) + " AS TIMESTAMP WITH LOCAL TIME ZONE)"
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
	text := t.Format(timestampLayout)
	if scale > 0 {
		fraction := fmt.Sprintf("%09d", t.Nanosecond())
		text += "." + fraction[:scale]
	}
	return text
}

func clientFloat(v float64, mode Mode) (string, error) {
	if !math.IsNaN(v) && !math.IsInf(v, 0) {
		s := strconv.FormatFloat(v, 'g', -1, 64)
		if mode == ModeOracle {
			return s + "d", nil
		}
		if !strings.ContainsAny(s, "eE") {
			s += "e0"
		}
		return s, nil
	}
	if mode != ModeOracle {
		return "", fmt.Errorf("%w: non-finite parameter", ErrBind)
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

var (
	_ driver.Stmt             = (*clientStmt)(nil)
	_ driver.StmtExecContext  = (*clientStmt)(nil)
	_ driver.StmtQueryContext = (*clientStmt)(nil)
)
