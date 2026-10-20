package sqltext

import (
	"fmt"
	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"strings"
	"time"
	"unicode"
)

// RewriteJDBCEscapes explicitly converts JDBC escapes to the selected OceanBase dialect.
// Auto mode and SQL whose quoting depends on unknown session settings are rejected.
func RewriteJDBCEscapes(query string, mode config.Mode) (string, error) {
	if mode != config.ModeMySQL && mode != config.ModeOracle {
		return "", fmt.Errorf("%w: actual compatibility mode required", dberrors.ErrBind)
	}
	tokens, err := EscapeTokens(query, mode)
	if err != nil {
		return "", err
	}
	return RewriteEscapes(query, mode, tokens, 0)
}

// EscapeTokens lexes SQL used by JDBC escape rewriting.
func EscapeTokens(query string, mode config.Mode) ([]Token, error) {
	tokens, err := LexSQL(query, mode, false, false)
	if err != nil {
		return nil, err
	}
	if mode == config.ModeMySQL {
		for _, flags := range [][2]bool{{true, false}, {false, true}} {
			alt, e := LexSQL(query, mode, flags[0], flags[1])
			if e != nil || len(tokens) != len(alt) {
				return nil, fmt.Errorf("%w: ambiguous escape quoting", dberrors.ErrBind)
			}
			for i, t := range tokens {
				if t != alt[i] {
					return nil, fmt.Errorf("%w: ambiguous escape quoting", dberrors.ErrBind)
				}
			}
		}
	}
	return tokens, nil
}

// RewriteEscapes rewrites a bounded level of JDBC escapes.
func RewriteEscapes(query string, mode config.Mode, tokens []Token, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("%w: JDBC escape nesting", dberrors.ErrBind)
	}
	var b strings.Builder
	offset := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.Kind == '}' {
			return "", fmt.Errorf("%w: unmatched JDBC escape", dberrors.ErrBind)
		}
		if t.Kind != '{' {
			continue
		}
		j, nesting := i+1, 1
		for ; j < len(tokens); j++ {
			if tokens[j].Kind == '{' {
				nesting++
			}
			if tokens[j].Kind == '}' {
				nesting--
				if nesting == 0 {
					break
				}
			}
		}
		if j == len(tokens) {
			return "", fmt.Errorf("%w: unterminated JDBC escape", dberrors.ErrBind)
		}
		inner := query[t.End:tokens[j].Start]
		innerTokens, err := LexSQL(inner, mode, false, false)
		if err != nil {
			return "", err
		}
		inner, err = RewriteEscapes(inner, mode, innerTokens, depth+1)
		if err != nil {
			return "", err
		}
		converted, err := ConvertEscape(strings.TrimSpace(inner), mode)
		if err != nil {
			return "", err
		}
		b.WriteString(query[offset:t.Start])
		b.WriteString(converted)
		offset = tokens[j].End
		i = j
	}
	b.WriteString(query[offset:])
	return b.String(), nil
}

// ConvertEscape rewrites one JDBC escape body.
func ConvertEscape(inner string, mode config.Mode) (string, error) {
	i := 0
	for i < len(inner) && inner[i] >= 'A' && inner[i] <= 'Z' || i < len(inner) && inner[i] >= 'a' && inner[i] <= 'z' {
		i++
	}
	kind := strings.ToLower(inner[:i])
	body := strings.TrimSpace(inner[i:])
	switch kind {
	case "d", "t", "ts":
		return EscapeDate(kind, body, mode)
	case "fn":
		return EscapeFunction(body, mode)
	case "oj":
		if body != "" {
			return body, nil
		}
	case "escape":
		if len(body) >= 3 && body[0] == '\'' && body[len(body)-1] == '\'' {
			return "ESCAPE " + body, nil
		}
	case "call":
		if body != "" {
			if mode == config.ModeOracle {
				return "BEGIN " + body + "; END;", nil
			}
			return "CALL " + body, nil
		}
	case "":
		if mode == config.ModeOracle && strings.HasPrefix(body, "?") {
			rest := strings.TrimSpace(body[1:])
			if strings.HasPrefix(rest, "=") {
				rest = strings.TrimSpace(rest[1:])
				if len(rest) > 4 && strings.EqualFold(rest[:4], "call") && rest[4] <= 32 {
					return "BEGIN ? := " + strings.TrimSpace(rest[5:]) + "; END;", nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w: JDBC escape syntax or dialect", dberrors.ErrUnsupported)
}

// EscapeDate validates and rewrites a JDBC date or timestamp literal.
func EscapeDate(kind, body string, mode config.Mode) (string, error) {
	if len(body) < 2 || body[0] != '\'' || body[len(body)-1] != '\'' {
		return "", fmt.Errorf("%w: JDBC temporal literal", dberrors.ErrBind)
	}
	text := body[1 : len(body)-1]
	layout := "2006-01-02"
	if kind == "t" {
		layout = "15:04:05"
	}
	if kind == "ts" {
		layout = "2006-01-02 15:04:05"
	}
	if _, err := time.Parse(layout, text); err != nil {
		return "", fmt.Errorf("%w: JDBC temporal literal", dberrors.ErrBind)
	}
	if mode == config.ModeOracle {
		if kind == "t" {
			return "", fmt.Errorf("%w: Oracle TIME literal", dberrors.ErrUnsupported)
		}
		if kind == "d" {
			return "DATE " + body, nil
		}
		return "TIMESTAMP " + body, nil
	}
	return body, nil
}

// EscapeFunction rewrites one JDBC scalar-function escape.
func EscapeFunction(body string, mode config.Mode) (string, error) {
	open := strings.IndexByte(body, '(')
	if open < 1 || !strings.HasSuffix(body, ")") {
		return "", fmt.Errorf("%w: JDBC function", dberrors.ErrBind)
	}
	name := strings.ToLower(strings.TrimSpace(body[:open]))
	args := body[open+1 : len(body)-1]
	if name == "convert" {
		return EscapeConvert(args, mode)
	}
	if name == "timestampadd" || name == "timestampdiff" {
		if mode != config.ModeMySQL {
			return "", fmt.Errorf("%w: Oracle JDBC timestamp arithmetic", dberrors.ErrUnsupported)
		}
		args = strings.TrimSpace(args)
		if len(args) > 8 && strings.EqualFold(args[:8], "SQL_TSI_") {
			args = args[8:]
		}
		return name + "(" + args + ")", nil
	}
	aliases := map[string]string{"ucase": "UPPER", "lcase": "LOWER"}
	if mode == config.ModeOracle {
		aliases["ifnull"] = "NVL"
		switch name {
		case "now":
			if strings.TrimSpace(args) == "" {
				return "CURRENT_TIMESTAMP", nil
			}
		case "curdate":
			if strings.TrimSpace(args) == "" {
				return "TRUNC(CURRENT_DATE)", nil
			}
		case "curtime":
			return "", fmt.Errorf("%w: Oracle JDBC current time", dberrors.ErrUnsupported)
		}
	}
	if alias, ok := aliases[name]; ok {
		return alias + "(" + args + ")", nil
	}
	return body, nil
}

// EscapeConvert rewrites a JDBC conversion with a complete operand boundary.
func EscapeConvert(args string, mode config.Mode) (string, error) {
	tokens, err := LexSQL(args, mode, false, false)
	if err != nil {
		return "", err
	}
	comma, depth := -1, 0
	for _, t := range tokens {
		switch t.Kind {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				comma = t.Start
			}
		}
	}
	if comma < 0 {
		return "", fmt.Errorf("%w: JDBC CONVERT arguments", dberrors.ErrBind)
	}
	operand, err := LexSQL(strings.TrimSpace(args[:comma]), mode, false, false)
	if err != nil {
		return "", err
	}
	if len(operand) == 0 {
		return "", fmt.Errorf("%w: JDBC CONVERT operand", dberrors.ErrBind)
	}
	// Retain trailing newlines so a line comment cannot swallow the generated suffix.
	value := strings.TrimLeftFunc(args[:comma], unicode.IsSpace)
	typ := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(args[comma+1:])), "sql_")
	target := ""
	if mode == config.ModeOracle {
		target = OracleConvertType(typ)
	} else {
		target = MysqlConvertType(typ)
	}

	if typ == "boolean" && mode == config.ModeMySQL {
		return "(1=(" + value + "))", nil
	}
	if (typ == "double" || typ == "float" || typ == "real") && mode == config.ModeMySQL {
		return "(0e0+(" + value + "))", nil
	}
	if target == "" {
		return "", fmt.Errorf("%w: JDBC CONVERT type", dberrors.ErrUnsupported)
	}
	return "CAST(" + value + " AS " + target + ")", nil
}

// MysqlConvertType maps a JDBC conversion type to MySQL syntax.
func MysqlConvertType(typ string) string {
	switch typ {
	case "integer", "bigint", "smallint", "tinyint":
		return "SIGNED INTEGER"
	case "bit":
		return "UNSIGNED INTEGER"
	case "binary", "varbinary", "longvarbinary", "blob", "rowid":
		return "BINARY"
	case "char", "varchar", "longvarchar", "nchar", "nvarchar", "longnvarchar", "clob", "nclob", "sqlxml":
		return "CHAR"
	case "numeric", "decimal":
		return "DECIMAL"
	case "date":
		return "DATE"
	case "time":
		return "TIME"
	case "timestamp":
		return "DATETIME"
	}
	return ""
}

// OracleConvertType maps a JDBC conversion type to Oracle syntax.
func OracleConvertType(typ string) string {
	switch typ {
	case "integer":
		return "NUMBER(10)"
	case "bigint":
		return "NUMBER(19)"
	case "smallint":
		return "NUMBER(5)"
	case "tinyint":
		return "NUMBER(3)"
	case "numeric", "decimal":
		return "NUMBER"
	case "float", "double":
		return "BINARY_DOUBLE"
	case "real":
		return "BINARY_FLOAT"
	case "char":
		return "CHAR"
	case "varchar", "longvarchar":
		return "VARCHAR2(4000)"
	case "nchar":
		return "NCHAR"
	case "nvarchar", "longnvarchar":
		return "NVARCHAR2(2000)"
	case "date":
		return "DATE"
	case "timestamp":
		return "TIMESTAMP"
	}
	return ""
}
