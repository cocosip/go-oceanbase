package sqltext

import (
	"errors"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strings"
)

// CursorUpdatableQuery checks the supported updatable-cursor SQL shape.
func CursorUpdatableQuery(query string, mode config.Mode, noBackslash bool) error {
	tokens, err := LexSQL(query, mode, noBackslash, false)
	if err != nil || len(tokens) < 4 {
		return errors.Join(dberrors.ErrCursor, err)
	}
	from := -1
	for i, t := range tokens {
		word := strings.ToUpper(query[t.Start:t.End])
		if t.Kind == 'w' {
			switch word {
			case "DISTINCT", "GROUP", "HAVING", "UNION", "INTERSECT", "MINUS", "EXCEPT", "JOIN", "SELECT":
				if i != 0 {
					return dberrors.ErrCursor
				}
			case "FROM":
				if from != -1 {
					return dberrors.ErrCursor
				}
				from = i
			}
		}
	}
	if from < 2 {
		return dberrors.ErrCursor
	}
	// Projection: identifiers, qualification, wildcard, comma, and explicit AS aliases only.
	for _, t := range tokens[1:from] {
		if t.Kind != 'w' && t.Kind != 'q' && t.Kind != '.' && t.Kind != '*' && t.Kind != ',' {
			return dberrors.ErrCursor
		}
	}
	i := from + 1
	if i >= len(tokens) || !cursorIdentifierToken(query, tokens[i], mode) {
		return dberrors.ErrCursor
	}
	i++
	if i < len(tokens) && tokens[i].Kind == '.' {
		i++
		if i >= len(tokens) || !cursorIdentifierToken(query, tokens[i], mode) {
			return dberrors.ErrCursor
		}
		i++
	}
	if i < len(tokens) && strings.EqualFold(query[tokens[i].Start:tokens[i].End], "AS") {
		i++
	}
	if i < len(tokens) && !cursorClause(query, tokens[i]) && cursorIdentifierToken(query, tokens[i], mode) {
		i++
	}
	if i < len(tokens) && !cursorClause(query, tokens[i]) {
		return dberrors.ErrCursor
	}
	return nil
}
func cursorIdentifierToken(query string, t Token, mode config.Mode) bool {
	if t.Kind == 'w' {
		return true
	}
	quote := byte('`')
	if mode == config.ModeOracle {
		quote = '"'
	}
	return t.Kind == 'q' && query[t.Start] == quote
}
func cursorClause(query string, t Token) bool {
	if t.Kind == ';' {
		return true
	}
	if t.Kind != 'w' {
		return false
	}
	switch strings.ToUpper(query[t.Start:t.End]) {
	case "WHERE", "ORDER", "LIMIT", "OFFSET", "FETCH", "FOR":
		return true
	}
	return false
}

// SensitiveCursorQuery rewrites a supported query to include its ROWID identity.
func SensitiveCursorQuery(query string) (string, error) {
	if err := CursorUpdatableQuery(query, config.ModeOracle, false); err != nil {
		return "", err
	}
	tokens, err := LexSQL(query, config.ModeOracle, false, false)
	if err != nil || len(tokens) < 4 {
		return "", errors.Join(dberrors.ErrCursor, err)
	}
	original := query
	for i, token := range tokens {
		if token.Kind == 'w' && strings.EqualFold(query[token.Start:token.End], "FROM") {
			// Append the hidden column to preserve ordinal ordering and SELECT hints.
			end := tokens[i-1].End
			query = query[:end] + ", ROWID" + query[end:]
			break
		}
	}
	// Oracle requires a qualified wildcard when ROWID is added to the projection.
	if tokens[1].Kind == '*' && strings.EqualFold(original[tokens[2].Start:tokens[2].End], "FROM") {
		i := 3
		if i+2 < len(tokens) && tokens[i+1].Kind == '.' {
			i += 2
		}
		name := original[tokens[i].Start:tokens[i].End]
		i++
		if i < len(tokens) && strings.EqualFold(original[tokens[i].Start:tokens[i].End], "AS") {
			i++
		}
		if i < len(tokens) && !cursorClause(original, tokens[i]) && cursorIdentifierToken(original, tokens[i], config.ModeOracle) {
			name = original[tokens[i].Start:tokens[i].End]
		}
		query = query[:tokens[1].Start] + name + ".*" + query[tokens[1].End:]
	}
	return query, nil
}

// CursorSingleSelect reports whether tokens describe a supported single SELECT.
func CursorSingleSelect(query string, tokens []Token) bool {
	if len(tokens) == 0 || !strings.EqualFold(query[tokens[0].Start:tokens[0].End], "SELECT") {
		return false
	}
	for i, t := range tokens {
		if t.Kind == ';' && i != len(tokens)-1 {
			return false
		}
	}
	return true
}
