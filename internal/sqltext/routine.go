package sqltext

import (
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strconv"
	"strings"
)

// RoutineCall maps simple CALL arguments to placeholders, with -1 for literal inputs.
type RoutineCall struct {
	Schema, Name string
	Arguments    []int
}

// NamedRoutine accepts only CALL syntax with unambiguous routine-to-placeholder mapping.
func NamedRoutine(query string, noBackslash bool) (RoutineCall, error) {
	bad := fmt.Errorf("%w: named MySQL arguments require a simple CALL", dberrors.ErrBind)
	tokens, err := LexSQL(query, config.ModeMySQL, noBackslash, false)
	if err != nil {
		return RoutineCall{}, err
	}
	if len(tokens) < 4 || tokens[0].Kind != 'w' || !strings.EqualFold(query[tokens[0].Start:tokens[0].End], "CALL") {
		return RoutineCall{}, bad
	}
	identifier := func(t Token) string {
		text := query[t.Start:t.End]
		if t.Kind == 'q' && text[0] == '`' {
			return strings.ReplaceAll(text[1:len(text)-1], "``", "`")
		}
		if t.Kind == 'w' && (text[0] < '0' || text[0] > '9') {
			return text
		}
		return ""
	}
	call := RoutineCall{Name: identifier(tokens[1])}
	i := 2
	if tokens[i].Kind == '.' {
		if len(tokens) < 6 {
			return RoutineCall{}, bad
		}
		call.Schema, call.Name = call.Name, identifier(tokens[i+1])
		i += 2
		if call.Schema == "" {
			return RoutineCall{}, bad
		}
	}
	if call.Name == "" || tokens[i].Kind != '(' {
		return RoutineCall{}, bad
	}
	i++
	call.Arguments, i, err = parseRoutineArguments(query, tokens, i)
	if err != nil {
		return RoutineCall{}, err
	}
	if i < len(tokens) && tokens[i].Kind == ';' {
		i++
	}
	if i != len(tokens) {
		return RoutineCall{}, bad
	}
	return call, nil
}

func parseRoutineArguments(query string, tokens []Token, i int) ([]int, int, error) {
	bad := fmt.Errorf("%w: named CALL requires literal or placeholder arguments", dberrors.ErrBind)
	var arguments []int
	placeholders := 0
	for i < len(tokens) && tokens[i].Kind != ')' {
		start := i
		for i < len(tokens) && tokens[i].Kind != ',' && tokens[i].Kind != ')' {
			i++
		}
		if i == start || i == len(tokens) {
			return nil, 0, bad
		}
		position := -1
		if i == start+1 && tokens[start].Kind == '?' {
			position = placeholders
			placeholders++
		} else if !routineLiteral(query, tokens[start:i]) {
			return nil, 0, bad
		}
		arguments = append(arguments, position)
		if len(arguments) > 65535 {
			return nil, 0, bad
		}
		if tokens[i].Kind == ',' {
			i++
			if i == len(tokens) || tokens[i].Kind == ')' {
				return nil, 0, bad
			}
		}
	}
	if i == len(tokens) || tokens[i].Kind != ')' {
		return nil, 0, bad
	}
	return arguments, i + 1, nil
}

func routineLiteral(query string, tokens []Token) bool {
	text := query[tokens[0].Start:tokens[len(tokens)-1].End]
	if len(tokens) == 1 {
		if tokens[0].Kind == 'q' && text[0] == '\'' {
			return true
		}
		if strings.EqualFold(text, "NULL") || strings.EqualFold(text, "TRUE") || strings.EqualFold(text, "FALSE") {
			return true
		}
	}
	// Restrict numeric spelling; ParseFloat alone also accepts NaN and infinities.
	if strings.Trim(text, "0123456789+-.eE") != "" {
		return false
	}
	_, err := strconv.ParseFloat(text, 64)
	return err == nil
}
