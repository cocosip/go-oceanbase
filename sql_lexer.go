package oceanbase

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type sqlToken struct {
	start, end int
	kind       byte
}
type bindSlot struct {
	start, end, index int
	name              string
}
type bindPlan struct {
	query, sql string
	mode       Mode
	slots      []bindSlot
	numInput   int
	named      bool
}
type boundValue struct{ value any }

func buildBindPlan(query string, mode Mode) (bindPlan, error) {
	return buildBindPlanEscapes(query, mode, false)
}

func buildBindPlanEscapes(query string, mode Mode, noBackslash bool) (bindPlan, error) {
	p, err := buildBindPlanSQLMode(query, mode, noBackslash, false)
	if mode != ModeMySQL || noBackslash || !strings.Contains(query, `"`) || !strings.Contains(query, `\`) {
		return p, err
	}
	alt, altErr := buildBindPlanSQLMode(query, mode, noBackslash, true)
	if err != nil || altErr != nil || len(p.slots) != len(alt.slots) {
		return bindPlan{}, fmt.Errorf("%w: ambiguous SQL quoting", ErrBind)
	}
	for i, slot := range p.slots {
		if slot != alt.slots[i] {
			return bindPlan{}, fmt.Errorf("%w: ambiguous SQL quoting", ErrBind)
		}
	}
	return p, nil
}

func buildBindPlanSQLMode(query string, mode Mode, noBackslash, ansiQuotes bool) (bindPlan, error) {
	if mode != ModeMySQL && mode != ModeOracle {
		return bindPlan{}, fmt.Errorf("%w: actual compatibility mode required", ErrBind)
	}
	tokens, err := lexSQL(query, mode, noBackslash, ansiQuotes)
	if err != nil {
		return bindPlan{}, err
	}
	p := bindPlan{query: query, mode: mode}
	style := ""
	names := make(map[string]int)
	for _, t := range tokens {
		if t.kind != '?' && (t.kind != ':' || mode != ModeOracle) {
			continue
		}
		slot := bindSlot{start: t.start, end: t.end}
		nextStyle := "position"
		if t.kind == '?' {
			slot.index = len(p.slots)
		} else {
			name := query[t.start+1 : t.end]
			if name[0] >= '0' && name[0] <= '9' {
				nextStyle = "number"
				n, err := strconv.Atoi(name)
				if err != nil || n < 1 || n > 65535 {
					return bindPlan{}, fmt.Errorf("%w: numeric placeholder", ErrBind)
				}
				slot.index = n - 1
			} else {
				nextStyle = "name"
				p.named = true
				slot.name = strings.ToUpper(name)
				if _, ok := names[slot.name]; !ok {
					names[slot.name] = len(names)
				}
				slot.index = names[slot.name]
			}
		}
		if style != "" && nextStyle != style {
			return bindPlan{}, fmt.Errorf("%w: mixed placeholder styles", ErrBind)
		}
		style = nextStyle
		if len(p.slots) >= 65535 {
			return bindPlan{}, fmt.Errorf("%w: too many placeholders", ErrBind)
		}
		p.slots = append(p.slots, slot)
		p.numInput = max(p.numInput, slot.index+1)
	}
	if style == "number" {
		if err := validateNumberSlots(p); err != nil {
			return bindPlan{}, err
		}
	}
	var b strings.Builder
	offset := 0
	for _, s := range p.slots {
		b.WriteString(query[offset:s.start])
		b.WriteByte('?')
		offset = s.end
	}
	b.WriteString(query[offset:])
	p.sql = b.String()
	return p, nil
}

// lexSQL retains executable token spans and excludes quoted contents and comments from binding.
func lexSQL(query string, mode Mode, noBackslash, ansiQuotes bool) ([]sqlToken, error) {
	if strings.ContainsRune(query, 0) || !utf8.ValidString(query) {
		return nil, fmt.Errorf("%w: invalid SQL text", ErrBind)
	}
	var tokens []sqlToken
	for i := 0; i < len(query); {
		start := i
		ch := query[i]
		if ch <= 32 {
			i++
			continue
		}
		next, err := sqlCommentEnd(query, i, mode)
		if err != nil {
			return nil, err
		}
		if next != i {
			i = next
			continue
		}
		if mode == ModeOracle && oracleQuotePrefix(query, i) > 0 {
			end, err := oracleQuoteEnd(query, i)
			if err != nil {
				return nil, err
			}
			i = end
			tokens = append(tokens, sqlToken{start, i, 'q'})
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			end, err := quotedEnd(query, i, ch, mode == ModeMySQL && !noBackslash && ch != '`' && (ch != '"' || !ansiQuotes))
			if err != nil {
				return nil, err
			}
			i = end
			tokens = append(tokens, sqlToken{start, i, 'q'})
			continue
		}
		kind := ch
		i++
		if mode == ModeOracle && ch == ':' {
			if i < len(query) && (query[i] == '=' || query[i] == ':') {
				i++
				kind = 0
			} else {
				i = sqlNameEnd(query, i, true)
				if i == start+1 {
					kind = 0
				}
			}
		} else if bindNameByte(ch) {
			kind = 'w'
			i = sqlNameEnd(query, i, mode != ModeMySQL)
		}
		tokens = append(tokens, sqlToken{start, i, kind})
	}
	return tokens, nil
}

func lineEnd(s string, i int) int {
	for i < len(s) && s[i] != '\n' && s[i] != '\r' {
		i++
	}
	return i
}
func bindNameByte(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '$' || ch == '#'
}
func quotedEnd(s string, i int, quote byte, backslash bool) (int, error) {
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
	return 0, fmt.Errorf("%w: unterminated quoted text", ErrBind)
}
func oracleQuotePrefix(s string, i int) int {
	if i+1 < len(s) && (s[i] == 'q' || s[i] == 'Q') && s[i+1] == '\'' {
		return 2
	}
	if i+2 < len(s) && (s[i] == 'n' || s[i] == 'N') && (s[i+1] == 'q' || s[i+1] == 'Q') && s[i+2] == '\'' {
		return 3
	}
	return 0
}
func oracleQuoteEnd(s string, i int) (int, error) {
	prefix := oracleQuotePrefix(s, i)
	start := i + prefix
	if start >= len(s) {
		return 0, fmt.Errorf("%w: alternative quote", ErrBind)
	}
	delim, n := utf8.DecodeRuneInString(s[start:])
	if unicode.IsSpace(delim) {
		return 0, fmt.Errorf("%w: alternative quote delimiter", ErrBind)
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
		return 0, fmt.Errorf("%w: unterminated alternative quote", ErrBind)
	}
	return start + n + offset + len(tail), nil
}

func sqlCommentEnd(query string, i int, mode Mode) (int, error) {
	ch := query[i]
	if ch == '#' && mode == ModeMySQL {
		return lineEnd(query, i+1), nil
	}
	if ch == '-' && i+1 < len(query) && query[i+1] == '-' && (mode == ModeOracle || i+2 == len(query) || query[i+2] <= 32) {
		return lineEnd(query, i+2), nil
	}
	if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
		if mode == ModeMySQL && i+2 < len(query) && query[i+2] == '!' {
			return 0, fmt.Errorf("%w: executable comments", ErrUnsupported)
		}
		n := strings.Index(query[i+2:], "*/")
		if n < 0 {
			return 0, fmt.Errorf("%w: unterminated comment", ErrBind)
		}
		return i + n + 4, nil
	}
	return i, nil
}

func sqlNameEnd(query string, i int, hash bool) int {
	for i < len(query) && bindNameByte(query[i]) && (hash || query[i] != '#') {
		i++
	}
	return i
}
func validateNumberSlots(p bindPlan) error {
	if p.numInput > len(p.slots) {
		return fmt.Errorf("%w: sparse numeric placeholders", ErrBind)
	}
	used := make([]bool, p.numInput)
	for _, s := range p.slots {
		used[s.index] = true
	}
	for _, ok := range used {
		if !ok {
			return fmt.Errorf("%w: sparse numeric placeholders", ErrBind)
		}
	}
	return nil
}
