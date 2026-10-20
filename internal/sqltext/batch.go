// Package sqltext parses SQL syntax without accessing session state or the wire.
package sqltext

import (
	"github.com/cocosip/go-oceanbase/config"
	"strings"
)

// BatchValuesPlan accepts a single VALUES tuple suitable for batch expansion.
func BatchValuesPlan(query string, noBackslash bool) (string, Plan, bool) {
	tokens, err := LexSQL(query, config.ModeMySQL, noBackslash, false)
	if err != nil || len(tokens) < 7 {
		return "", Plan{}, false
	}
	word := func(i int, w string) bool {
		return i < len(tokens) && tokens[i].Kind == 'w' && strings.EqualFold(query[tokens[i].Start:tokens[i].End], w)
	}
	ident := func(t Token) bool {
		return t.Kind == 'w' && (query[t.Start] < '0' || query[t.Start] > '9') || t.Kind == 'q' && query[t.Start] == '`'
	}
	if !word(0, "INSERT") || !word(1, "INTO") || !ident(tokens[2]) {
		return "", Plan{}, false
	}
	i := 3
	if tokens[i].Kind == '.' {
		i++
		if !ident(tokens[i]) {
			return "", Plan{}, false
		}
		i++
	}
	if tokens[i].Kind == '(' {
		i = batchValuesListEnd(tokens, i, ident)
	}
	if i < 0 || !word(i, "VALUES") {
		return "", Plan{}, false
	}
	i++
	start := i
	i = batchValuesListEnd(tokens, i, func(t Token) bool { return t.Kind == '?' })
	if i != len(tokens) {
		return "", Plan{}, false
	}
	plan, err := BuildBindPlanEscapes(query[tokens[start].Start:tokens[i-1].End], config.ModeMySQL, noBackslash)
	return query[:tokens[start].Start], plan, err == nil
}

func batchValuesListEnd(tokens []Token, i int, valid func(Token) bool) int {
	if i >= len(tokens) || tokens[i].Kind != '(' {
		return -1
	}
	i++
	for {
		if i >= len(tokens) || !valid(tokens[i]) {
			return -1
		}
		i++
		if i >= len(tokens) {
			return -1
		}
		if tokens[i].Kind == ')' {
			return i + 1
		}
		if tokens[i].Kind != ',' {
			return -1
		}
		i++
	}
}

// BatchDML reports whether SQL is eligible for the batch DML strategies.
func BatchDML(query string, mode config.Mode, noBackslash bool) bool {
	for _, ansi := range []bool{false, true} {
		tokens, err := LexSQL(query, mode, noBackslash, ansi)
		if err != nil || len(tokens) == 0 || tokens[0].Kind != 'w' {
			return false
		}
		switch strings.ToUpper(query[tokens[0].Start:tokens[0].End]) {
		case "INSERT", "UPDATE", "DELETE", "REPLACE":
		default:
			return false
		}
		for _, token := range tokens {
			if token.Kind == ';' || token.Kind == 'w' && strings.EqualFold(query[token.Start:token.End], "RETURNING") {
				return false
			}
		}
	}
	return true
}
