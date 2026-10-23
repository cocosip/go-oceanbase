package sqltext

import (
	"strings"

	"github.com/cocosip/go-oceanbase/config"
)

// ReturningBindCount counts the trailing INTO slots of a single Oracle DML statement.
// The query has already had named binds replaced with question marks.
func ReturningBindCount(query string) int {
	tokens, err := LexSQL(query, config.ModeOracle, false, false)
	if err != nil || len(tokens) == 0 {
		return 0
	}
	first := strings.ToUpper(query[tokens[0].Start:tokens[0].End])
	if first != "INSERT" && first != "UPDATE" && first != "DELETE" {
		return 0
	}
	depth, returning := 0, false
	for i, token := range tokens {
		switch token.Kind {
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			return 0
		}
		if depth != 0 || token.Kind != 'w' {
			continue
		}
		word := query[token.Start:token.End]
		if strings.EqualFold(word, "RETURNING") {
			returning = true
		} else if returning && strings.EqualFold(word, "INTO") {
			tail := tokens[i+1:]
			if len(tail) > 0 && tail[len(tail)-1].Kind == ';' {
				tail = tail[:len(tail)-1]
			}
			if len(tail)%2 != 1 {
				return 0
			}
			for j, bind := range tail {
				want := byte('?')
				if j%2 != 0 {
					want = ','
				}
				if bind.Kind != want {
					return 0
				}
			}
			return (len(tail) + 1) / 2
		}
	}
	return 0
}
