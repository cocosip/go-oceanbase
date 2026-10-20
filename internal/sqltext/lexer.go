package sqltext

import (
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Token retains an executable SQL token span.
type Token struct {
	Start, End int
	Kind       byte
}

// Slot maps one physical placeholder to its logical input.
type Slot struct {
	Start, End, Index int
	Name              string
}

// Plan retains SQL text and its logical-to-physical bind mapping.
type Plan struct {
	Query, SQL string
	Mode       config.Mode
	Slots      []Slot
	NumInput   int
	Named      bool
}

// BuildBindPlan parses placeholders using the actual compatibility mode.
func BuildBindPlan(query string, mode config.Mode) (Plan, error) {
	return BuildBindPlanEscapes(query, mode, false)
}

// BuildBindPlanEscapes parses placeholders under the active backslash-escape mode.
func BuildBindPlanEscapes(query string, mode config.Mode, noBackslash bool) (Plan, error) {
	p, err := BuildBindPlanSQLMode(query, mode, noBackslash, false)
	if mode != config.ModeMySQL || noBackslash || !strings.Contains(query, `"`) || !strings.Contains(query, `\`) {
		return p, err
	}
	alt, altErr := BuildBindPlanSQLMode(query, mode, noBackslash, true)
	if err != nil || altErr != nil || len(p.Slots) != len(alt.Slots) {
		return Plan{}, fmt.Errorf("%w: ambiguous SQL quoting", dberrors.ErrBind)
	}
	for i, slot := range p.Slots {
		if slot != alt.Slots[i] {
			return Plan{}, fmt.Errorf("%w: ambiguous SQL quoting", dberrors.ErrBind)
		}
	}
	return p, nil
}

// BuildBindPlanSQLMode parses placeholders with explicit SQL quote modes.
func BuildBindPlanSQLMode(query string, mode config.Mode, noBackslash, ansiQuotes bool) (Plan, error) {
	if mode != config.ModeMySQL && mode != config.ModeOracle {
		return Plan{}, fmt.Errorf("%w: actual compatibility mode required", dberrors.ErrBind)
	}
	tokens, err := LexSQL(query, mode, noBackslash, ansiQuotes)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Query: query, Mode: mode}
	style := ""
	names := make(map[string]int)
	for _, t := range tokens {
		if t.Kind != '?' && (t.Kind != ':' || mode != config.ModeOracle) {
			continue
		}
		slot := Slot{Start: t.Start, End: t.End}
		nextStyle := "position"
		if t.Kind == '?' {
			slot.Index = len(p.Slots)
		} else {
			name := query[t.Start+1 : t.End]
			if name[0] >= '0' && name[0] <= '9' {
				nextStyle = "number"
				n, err := strconv.Atoi(name)
				if err != nil || n < 1 || n > 65535 {
					return Plan{}, fmt.Errorf("%w: numeric placeholder", dberrors.ErrBind)
				}
				slot.Index = n - 1
			} else {
				nextStyle = "name"
				p.Named = true
				slot.Name = strings.ToUpper(name)
				if _, ok := names[slot.Name]; !ok {
					names[slot.Name] = len(names)
				}
				slot.Index = names[slot.Name]
			}
		}
		if style != "" && nextStyle != style {
			return Plan{}, fmt.Errorf("%w: mixed placeholder styles", dberrors.ErrBind)
		}
		style = nextStyle
		if len(p.Slots) >= 65535 {
			return Plan{}, fmt.Errorf("%w: too many placeholders", dberrors.ErrBind)
		}
		p.Slots = append(p.Slots, slot)
		p.NumInput = max(p.NumInput, slot.Index+1)
	}
	if style == "number" {
		if err := ValidateNumberSlots(p); err != nil {
			return Plan{}, err
		}
	}
	var b strings.Builder
	offset := 0
	for _, s := range p.Slots {
		b.WriteString(query[offset:s.Start])
		b.WriteByte('?')
		offset = s.End
	}
	b.WriteString(query[offset:])
	p.SQL = b.String()
	return p, nil
}

// LexSQL retains executable token spans and excludes quoted contents and comments from binding.
func LexSQL(query string, mode config.Mode, noBackslash, ansiQuotes bool) ([]Token, error) {
	if strings.ContainsRune(query, 0) || !utf8.ValidString(query) {
		return nil, fmt.Errorf("%w: invalid SQL text", dberrors.ErrBind)
	}
	var tokens []Token
	for i := 0; i < len(query); {
		start := i
		ch := query[i]
		if ch <= 32 {
			i++
			continue
		}
		next, err := SQLCommentEnd(query, i, mode)
		if err != nil {
			return nil, err
		}
		if next != i {
			i = next
			continue
		}
		if mode == config.ModeOracle && OracleQuotePrefix(query, i) > 0 {
			end, err := OracleQuoteEnd(query, i)
			if err != nil {
				return nil, err
			}
			i = end
			tokens = append(tokens, Token{start, i, 'q'})
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			end, err := QuotedEnd(query, i, ch, mode == config.ModeMySQL && !noBackslash && ch != '`' && (ch != '"' || !ansiQuotes))
			if err != nil {
				return nil, err
			}
			i = end
			tokens = append(tokens, Token{start, i, 'q'})
			continue
		}
		kind := ch
		i++
		if mode == config.ModeOracle && ch == ':' {
			if i < len(query) && (query[i] == '=' || query[i] == ':') {
				i++
				kind = 0
			} else {
				i = SQLNameEnd(query, i, true)
				if i == start+1 {
					kind = 0
				}
			}
		} else if BindNameByte(ch) {
			kind = 'w'
			i = SQLNameEnd(query, i, mode != config.ModeMySQL)
		}
		tokens = append(tokens, Token{start, i, kind})
	}
	return tokens, nil
}

// LineEnd returns the end of a SQL line comment.
func LineEnd(s string, i int) int {
	for i < len(s) && s[i] != '\n' && s[i] != '\r' {
		i++
	}
	return i
}

// BindNameByte reports whether a byte can appear in an unquoted bind name.
func BindNameByte(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '$' || ch == '#'
}

// QuotedEnd locates the end of a quoted SQL token.
func QuotedEnd(s string, i int, quote byte, backslash bool) (int, error) {
	for i = i + 1; i < len(s); i++ {
		if backslash && s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			if i+1 < len(s) && s[i+1] == quote {
				i++
				continue
			}
			return i + 1, nil
		}
	}
	return 0, fmt.Errorf("%w: unterminated quoted text", dberrors.ErrBind)
}

// OracleQuotePrefix identifies an Oracle alternative-quote prefix.
func OracleQuotePrefix(s string, i int) int {
	if i+1 < len(s) && (s[i] == 'q' || s[i] == 'Q') && s[i+1] == '\'' {
		return 2
	}
	if i+2 < len(s) && (s[i] == 'n' || s[i] == 'N') && (s[i+1] == 'q' || s[i+1] == 'Q') && s[i+2] == '\'' {
		return 3
	}
	return 0
}

// OracleQuoteEnd locates the end of an Oracle alternative-quoted token.
func OracleQuoteEnd(s string, i int) (int, error) {
	prefix := OracleQuotePrefix(s, i)
	start := i + prefix
	if start >= len(s) {
		return 0, fmt.Errorf("%w: alternative quote", dberrors.ErrBind)
	}
	delim, n := utf8.DecodeRuneInString(s[start:])
	if unicode.IsSpace(delim) {
		return 0, fmt.Errorf("%w: alternative quote delimiter", dberrors.ErrBind)
	}
	switch delim {
	case '[':
		delim = ']'
	case '(':
		delim = ')'
	case '{':
		delim = '}'
	case '<':
		delim = '>'
	}
	tail := string(delim) + "'"
	offset := strings.Index(s[start+n:], tail)
	if offset < 0 {
		return 0, fmt.Errorf("%w: unterminated alternative quote", dberrors.ErrBind)
	}
	return start + n + offset + len(tail), nil
}

// SQLCommentEnd skips an ordinary SQL comment and rejects executable comments.
func SQLCommentEnd(query string, i int, mode config.Mode) (int, error) {
	ch := query[i]
	if ch == '#' && mode == config.ModeMySQL {
		return LineEnd(query, i+1), nil
	}
	if ch == '-' && i+1 < len(query) && query[i+1] == '-' && (mode == config.ModeOracle || i+2 == len(query) || query[i+2] <= 32) {
		return LineEnd(query, i+2), nil
	}
	if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
		if mode == config.ModeMySQL && i+2 < len(query) && query[i+2] == '!' {
			return 0, fmt.Errorf("%w: executable comments", dberrors.ErrUnsupported)
		}
		n := strings.Index(query[i+2:], "*/")
		if n < 0 {
			return 0, fmt.Errorf("%w: unterminated comment", dberrors.ErrBind)
		}
		return i + n + 4, nil
	}
	return i, nil
}

// SQLNameEnd locates the end of an unquoted SQL name.
func SQLNameEnd(query string, i int, hash bool) int {
	for i < len(query) && BindNameByte(query[i]) && (hash || query[i] != '#') {
		i++
	}
	return i
}

// ValidateNumberSlots rejects sparse numeric placeholder sequences.
func ValidateNumberSlots(p Plan) error {
	if p.NumInput > len(p.Slots) {
		return fmt.Errorf("%w: sparse numeric placeholders", dberrors.ErrBind)
	}
	used := make([]bool, p.NumInput)
	for _, s := range p.Slots {
		used[s.Index] = true
	}
	for _, ok := range used {
		if !ok {
			return fmt.Errorf("%w: sparse numeric placeholders", dberrors.ErrBind)
		}
	}
	return nil
}
