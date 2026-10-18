package oceanbase

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// RewriteJDBCEscapes explicitly converts JDBC escapes to the selected OceanBase dialect.
// Auto mode and SQL whose quoting depends on unknown session settings are rejected.
func RewriteJDBCEscapes(query string, mode Mode) (string, error) {
	if mode != ModeMySQL && mode != ModeOracle {
		return "", fmt.Errorf("%w: actual compatibility mode required", ErrBind)
	}
	tokens, err := escapeTokens(query, mode)
	if err != nil {
		return "", err
	}
	return rewriteEscapes(query, mode, tokens, 0)
}

func escapeTokens(query string, mode Mode) ([]sqlToken, error) {
	tokens, err := lexSQL(query, mode, false, false)
	if err != nil {
		return nil, err
	}
	if mode == ModeMySQL {
		for _, flags := range [][2]bool{{true, false}, {false, true}} {
			alt, e := lexSQL(query, mode, flags[0], flags[1])
			if e != nil || len(tokens) != len(alt) {
				return nil, fmt.Errorf("%w: ambiguous escape quoting", ErrBind)
			}
			for i, t := range tokens {
				if t != alt[i] {
					return nil, fmt.Errorf("%w: ambiguous escape quoting", ErrBind)
				}
			}
		}
	}
	return tokens, nil
}

func rewriteEscapes(query string, mode Mode, tokens []sqlToken, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("%w: JDBC escape nesting", ErrBind)
	}
	var b strings.Builder
	offset := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.kind == '}' {
			return "", fmt.Errorf("%w: unmatched JDBC escape", ErrBind)
		}
		if t.kind != '{' {
			continue
		}
		j, nesting := i+1, 1
		for ; j < len(tokens); j++ {
			if tokens[j].kind == '{' {
				nesting++
			}
			if tokens[j].kind == '}' {
				nesting--
				if nesting == 0 {
					break
				}
			}
		}
		if j == len(tokens) {
			return "", fmt.Errorf("%w: unterminated JDBC escape", ErrBind)
		}
		inner := query[t.end:tokens[j].start]
		innerTokens, err := lexSQL(inner, mode, false, false)
		if err != nil {
			return "", err
		}
		inner, err = rewriteEscapes(inner, mode, innerTokens, depth+1)
		if err != nil {
			return "", err
		}
		converted, err := convertEscape(strings.TrimSpace(inner), mode)
		if err != nil {
			return "", err
		}
		b.WriteString(query[offset:t.start])
		b.WriteString(converted)
		offset = tokens[j].end
		i = j
	}
	b.WriteString(query[offset:])
	return b.String(), nil
}

func convertEscape(inner string, mode Mode) (string, error) {
	i := 0
	for i < len(inner) && inner[i] >= 'A' && inner[i] <= 'Z' || i < len(inner) && inner[i] >= 'a' && inner[i] <= 'z' {
		i++
	}
	kind := strings.ToLower(inner[:i])
	body := strings.TrimSpace(inner[i:])
	switch kind {
	case "d", "t", "ts":
		return escapeDate(kind, body, mode)
	case "fn":
		return escapeFunction(body, mode)
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
			if mode == ModeOracle {
				return "BEGIN " + body + "; END;", nil
			}
			return "CALL " + body, nil
		}
	case "":
		if mode == ModeOracle && strings.HasPrefix(body, "?") {
			rest := strings.TrimSpace(body[1:])
			if strings.HasPrefix(rest, "=") {
				rest = strings.TrimSpace(rest[1:])
				if len(rest) > 4 && strings.EqualFold(rest[:4], "call") && rest[4] <= 32 {
					return "BEGIN ? := " + strings.TrimSpace(rest[5:]) + "; END;", nil
				}
			}
		}
	}
	return "", fmt.Errorf("%w: JDBC escape syntax or dialect", ErrUnsupported)
}

func escapeDate(kind, body string, mode Mode) (string, error) {
	if len(body) < 2 || body[0] != '\'' || body[len(body)-1] != '\'' {
		return "", fmt.Errorf("%w: JDBC temporal literal", ErrBind)
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
		return "", fmt.Errorf("%w: JDBC temporal literal", ErrBind)
	}
	if mode == ModeOracle {
		if kind == "t" {
			return "", fmt.Errorf("%w: Oracle TIME literal", ErrUnsupported)
		}
		if kind == "d" {
			return "DATE " + body, nil
		}
		return "TIMESTAMP " + body, nil
	}
	return body, nil
}

func escapeFunction(body string, mode Mode) (string, error) {
	open := strings.IndexByte(body, '(')
	if open < 1 || !strings.HasSuffix(body, ")") {
		return "", fmt.Errorf("%w: JDBC function", ErrBind)
	}
	name := strings.ToLower(strings.TrimSpace(body[:open]))
	args := body[open+1 : len(body)-1]
	if name == "convert" {
		return escapeConvert(args, mode)
	}
	if name == "timestampadd" || name == "timestampdiff" {
		if mode != ModeMySQL {
			return "", fmt.Errorf("%w: Oracle JDBC timestamp arithmetic", ErrUnsupported)
		}
		args = strings.TrimSpace(args)
		if len(args) > 8 && strings.EqualFold(args[:8], "SQL_TSI_") {
			args = args[8:]
		}
		return name + "(" + args + ")", nil
	}
	aliases := map[string]string{"ucase": "UPPER", "lcase": "LOWER"}
	if mode == ModeOracle {
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
			return "", fmt.Errorf("%w: Oracle JDBC current time", ErrUnsupported)
		}
	}
	if alias, ok := aliases[name]; ok {
		return alias + "(" + args + ")", nil
	}
	return body, nil
}

func escapeConvert(args string, mode Mode) (string, error) {
	tokens, err := lexSQL(args, mode, false, false)
	if err != nil {
		return "", err
	}
	comma, depth := -1, 0
	for _, t := range tokens {
		switch t.kind {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				comma = t.start
			}
		}
	}
	if comma < 0 {
		return "", fmt.Errorf("%w: JDBC CONVERT arguments", ErrBind)
	}
	operand, err := lexSQL(strings.TrimSpace(args[:comma]), mode, false, false)
	if err != nil {
		return "", err
	}
	if len(operand) == 0 {
		return "", fmt.Errorf("%w: JDBC CONVERT operand", ErrBind)
	}
	// Retain trailing newlines so a line comment cannot swallow the generated suffix.
	value := strings.TrimLeftFunc(args[:comma], unicode.IsSpace)
	typ := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(args[comma+1:])), "sql_")
	target := ""
	if mode == ModeOracle {
		target = oracleConvertType(typ)
	} else {
		target = mysqlConvertType(typ)
	}

	if typ == "boolean" && mode == ModeMySQL {
		return "(1=(" + value + "))", nil
	}
	if (typ == "double" || typ == "float" || typ == "real") && mode == ModeMySQL {
		return "(0e0+(" + value + "))", nil
	}
	if target == "" {
		return "", fmt.Errorf("%w: JDBC CONVERT type", ErrUnsupported)
	}
	return "CAST(" + value + " AS " + target + ")", nil
}

func mysqlConvertType(typ string) string {
	switch typ {
	case "integer", "bigint", "smallint", "tinyint":
		return "SIGNED INTEGER"
	case "bit":
		return "UNSIGNED INTEGER"
	case charsetBinary, "varbinary", "longvarbinary", "blob", "rowid":
		return "BINARY"
	case "char", "varchar", "longvarchar", "nchar", "nvarchar", "longnvarchar", "clob", "nclob", "sqlxml":
		return typeNameChar
	case "numeric", "decimal":
		return typeNameDecimal
	case "date":
		return typeNameDate
	case "time":
		return typeNameTime
	case "timestamp":
		return typeNameDateTime
	}
	return ""
}
func oracleConvertType(typ string) string {
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
		return typeNameNumber
	case "float", "double":
		return typeNameBinaryDouble
	case "real":
		return typeNameBinaryFloat
	case "char":
		return typeNameChar
	case "varchar", "longvarchar":
		return "VARCHAR2(4000)"
	case "nchar":
		return typeNameNChar
	case "nvarchar", "longnvarchar":
		return "NVARCHAR2(2000)"
	case "date":
		return typeNameDate
	case "timestamp":
		return typeNameTimestamp
	}
	return ""
}
