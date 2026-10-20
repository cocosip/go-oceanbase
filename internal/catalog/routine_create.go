package catalog

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	charsetBinary          = "binary"
	metadataCatalogMySQL   = "def"
	metadataGenerated      = "generated"
	metadataLowerInteger   = "integer"
	metadataLowerSmallInt  = "smallint"
	metadataLowerTinyInt   = "tinyint"
	metadataLowerMediumInt = "mediumint"
	metadataLowerVarchar   = "varchar"
	metadataLowerNumeric   = "numeric"
	metadataLowerReal      = "real"
	metadataLowerTimestamp = "timestamp"
	metadataLowerEnum      = "enum"
	metadataLowerSet       = "set"
	metadataLowerDouble    = "double"
	metadataLowerBigInt    = "bigint"
	metadataLowerChar      = "char"
	metadataLowerDecimal   = "decimal"
	metadataLowerFloat     = "float"
	metadataLowerDate      = "date"
)

type routineToken struct {
	text   string
	quoted bool
}
type routineLexer struct {
	text        string
	pos         int
	noBackslash bool
}

func (l *routineLexer) next() (routineToken, error) {
	for l.pos < len(l.text) {
		start := l.pos
		ch := l.text[l.pos]
		if ch <= ' ' {
			l.pos++
			continue
		}
		if ch == '#' || ch == '-' && strings.HasPrefix(l.text[l.pos:], "--") && (l.pos+2 == len(l.text) || l.text[l.pos+2] <= ' ') {
			for l.pos < len(l.text) && l.text[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if strings.HasPrefix(l.text[l.pos:], "/*") {
			end := strings.Index(l.text[l.pos+2:], "*/")
			if end < 0 || strings.HasPrefix(l.text[l.pos:], "/*!") {
				return routineToken{}, dberrors.ErrBind
			}
			l.pos += end + 4
			continue
		}
		if ch == '\'' || ch == '"' || ch == '`' {
			l.pos++
			var out strings.Builder
			for l.pos < len(l.text) {
				c := l.text[l.pos]
				l.pos++
				if c == ch {
					if l.pos < len(l.text) && l.text[l.pos] == ch {
						out.WriteByte(ch)
						l.pos++
						continue
					}
					return routineToken{out.String(), true}, nil
				}
				if c == '\\' && ch != '`' && !l.noBackslash {
					if l.pos == len(l.text) {
						return routineToken{}, dberrors.ErrBind
					}
					c = l.text[l.pos]
					l.pos++
				}
				out.WriteByte(c)
			}
			return routineToken{}, dberrors.ErrBind
		}
		if strings.ContainsRune("(),.@=;", rune(ch)) {
			l.pos++
			return routineToken{text: string(ch)}, nil
		}
		for l.pos < len(l.text) {
			c := l.text[l.pos]
			if c <= ' ' || strings.ContainsRune("(),.@=;'\"`", rune(c)) || strings.HasPrefix(l.text[l.pos:], "/*") {
				break
			}
			l.pos++
		}
		if l.pos == start {
			return routineToken{}, dberrors.ErrBind
		}
		return routineToken{text: l.text[start:l.pos]}, nil
	}
	return routineToken{}, nil
}

var errRoutineDDL = fmt.Errorf("%w: unsupported or malformed SHOW CREATE routine declaration", dberrors.ErrBind)

// ParseRoutineCreateMode extracts parameters from supported SHOW CREATE text under its SQL mode.
func ParseRoutineCreateMode(ddl, schema, name string, noBackslash bool) ([][]driver.Value, error) {
	l := routineLexer{text: ddl, noBackslash: noBackslash}
	kind, err := l.header(schema, name)
	if err != nil {
		return nil, err
	}
	parts, err := l.parameterGroups()
	if err != nil {
		return nil, err
	}
	var rows [][]driver.Value
	seen := make(map[string]bool, len(parts))
	for i, tokens := range parts {
		direction := "IN"
		if !tokens[0].quoted {
			switch strings.ToUpper(tokens[0].text) {
			case "IN", "OUT", "INOUT":
				direction = strings.ToUpper(tokens[0].text)
				tokens = tokens[1:]
			}
		}
		if len(tokens) < 2 || tokens[0].text == "" {
			return nil, errRoutineDDL
		}
		parameter := tokens[0].text
		key := strings.ToUpper(parameter)
		if seen[key] {
			return nil, errRoutineDDL
		}
		seen[key] = true
		if !tokens[0].quoted && !routineIdentifier(parameter) {
			return nil, errRoutineDDL
		}
		row, err := routineParameterRow(tokens[1:], schema, name, parameter, direction, int64(i+1))
		if err != nil {
			return nil, errRoutineDDL
		}
		rows = append(rows, row)
	}
	if kind == "FUNCTION" {
		row, err := l.returnParameter(schema, name)
		if err != nil {
			return nil, err
		}
		rows = append([][]driver.Value{row}, rows...)
	}
	return rows, nil
}

func (l *routineLexer) header(schema, name string) (string, error) {
	tok, err := l.next()
	if err != nil || !strings.EqualFold(tok.text, "CREATE") || tok.quoted {
		return "", errRoutineDDL
	}
	kind := ""
	for kind == "" {
		tok, err = l.next()
		if err != nil || tok.text == "" && !tok.quoted {
			return "", errRoutineDDL
		}
		if !tok.quoted && (strings.EqualFold(tok.text, "PROCEDURE") || strings.EqualFold(tok.text, "FUNCTION")) {
			kind = strings.ToUpper(tok.text)
		}
	}
	tok, err = l.next()
	if err != nil || tok.text == "" {
		return "", errRoutineDDL
	}
	declared := tok.text
	tok, err = l.next()
	if tok.text == "." {
		if declared != schema {
			return "", errRoutineDDL
		}
		tok, err = l.next()
		declared = tok.text
		if err != nil {
			return "", errRoutineDDL
		}
		tok, err = l.next()
	}
	if err != nil || !strings.EqualFold(declared, name) || tok.text != "(" {
		return "", errRoutineDDL
	}
	return kind, nil
}

func (l *routineLexer) parameterGroups() ([][]routineToken, error) {
	var parts [][]routineToken
	var part []routineToken
	depth := 0
	for {
		tok, err := l.next()
		if err != nil || tok.text == "" && !tok.quoted {
			return nil, errRoutineDDL
		}
		if !tok.quoted {
			if tok.text == "(" {
				depth++
			}
			if tok.text == ")" {
				if depth == 0 {
					if len(parts) != 0 && len(part) == 0 {
						return nil, errRoutineDDL
					}
					if len(part) > 0 {
						parts = append(parts, part)
					}
					break
				}
				depth--
			}
			if tok.text == "," && depth == 0 {
				if len(part) == 0 {
					return nil, errRoutineDDL
				}
				parts = append(parts, part)
				if len(parts) >= command.MaxColumns {
					return nil, wire.ErrMessageTooLarge
				}
				part = nil
				continue
			}
		}
		part = append(part, tok)
	}
	if len(parts) > command.MaxColumns {
		return nil, wire.ErrMessageTooLarge
	}
	return parts, nil
}

func (l *routineLexer) returnParameter(schema, name string) ([]driver.Value, error) {
	tok, err := l.next()
	if err != nil || tok.quoted || !strings.EqualFold(tok.text, "RETURNS") {
		return nil, errRoutineDDL
	}
	var tokens []routineToken
	depth := 0
	for {
		tok, err = l.next()
		if err != nil {
			return nil, errRoutineDDL
		}
		if tok.text == "" && !tok.quoted {
			break
		}
		if !tok.quoted {
			if depth == 0 && len(tokens) > 0 {
				switch strings.ToUpper(tok.text) {
				case "DETERMINISTIC", "NOT", "CONTAINS", "NO", "READS", "MODIFIES", "SQL", "COMMENT", "RETURN", "BEGIN", "LANGUAGE":
					goto returnTypeDone
				}
			}
			if tok.text == "(" {
				depth++
			}
			if tok.text == ")" {
				depth--
			}
		}
		tokens = append(tokens, tok)
	}
returnTypeDone:
	row, err := routineParameterRow(tokens, schema, name, "", "RETURN", 0)
	if err != nil {
		return nil, errRoutineDDL
	}
	row[5] = nil
	return row, nil
}

func routineIdentifier(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
			return false
		}
	}
	return s != ""
}

func routineParameterRow(tokens []routineToken, schema, name, parameter, direction string, ordinal int64) ([]driver.Value, error) {
	if len(tokens) == 0 || tokens[0].quoted || !routineIdentifier(tokens[0].text) {
		return nil, dberrors.ErrBind
	}
	typ := strings.ToLower(tokens[0].text)
	row := []driver.Value{metadataCatalogMySQL, schema, name, name, nil, parameter, ordinal, direction, typ, nil, nil, nil, nil, nil, nil}
	numbers, enumLength, modifiers, err := routineTypeArguments(typ, tokens[1:])
	if err != nil {
		return nil, err
	}
	unsigned, err := routineTypeModifiers(typ, modifiers)
	if err != nil {
		return nil, err
	}
	if err := routineTypeFields(row, typ, numbers, enumLength, unsigned); err != nil {
		return nil, err
	}
	return row, nil
}

func routineTypeArguments(typ string, tokens []routineToken) ([]int64, int64, []routineToken, error) {
	var numbers []int64
	var enumLength int64
	i := 0
	if i < len(tokens) && tokens[i].text == "(" {
		i++
		if typ == metadataLowerEnum || typ == metadataLowerSet {
			wantLiteral := true
			for i < len(tokens) && (tokens[i].quoted || tokens[i].text != ")") {
				if wantLiteral != tokens[i].quoted || !wantLiteral && tokens[i].text != "," {
					return nil, 0, nil, dberrors.ErrBind
				}
				if wantLiteral {
					n := int64(utf8.RuneCountInString(tokens[i].text))
					if typ == metadataLowerEnum {
						enumLength = max(enumLength, n)
					} else {
						if i > 1 {
							enumLength++
						}
						enumLength += n
					}
				}
				wantLiteral = !wantLiteral
				i++
			}
			if wantLiteral {
				return nil, 0, nil, dberrors.ErrBind
			}
		} else {
			for {
				if i >= len(tokens) || tokens[i].quoted {
					return nil, 0, nil, dberrors.ErrBind
				}
				n, err := strconv.ParseInt(tokens[i].text, 10, 64)
				if err != nil || n < 0 {
					return nil, 0, nil, dberrors.ErrBind
				}
				numbers = append(numbers, n)
				i++
				if i >= len(tokens) || tokens[i].text != "," {
					break
				}
				i++
			}
		}
		if i >= len(tokens) || tokens[i].text != ")" {
			return nil, 0, nil, dberrors.ErrBind
		}
		i++
	}
	return numbers, enumLength, tokens[i:], nil
}

func routineTypeModifiers(typ string, tokens []routineToken) (bool, error) {
	i := 0
	unsigned := false
	for i < len(tokens) {
		t := strings.ToUpper(tokens[i].text)
		if tokens[i].quoted {
			return false, dberrors.ErrBind
		}
		switch t {
		case "UNSIGNED", "ZEROFILL", "BINARY":
			unsigned = unsigned || t == "UNSIGNED" || t == "ZEROFILL"
			i++
		case "CHARSET", "COLLATE":
			i += 2
		case "CHARACTER":
			if i+1 >= len(tokens) || !strings.EqualFold(tokens[i+1].text, "SET") {
				return false, dberrors.ErrBind
			}
			i += 3
		case "PRECISION":
			if typ != metadataLowerDouble {
				return false, dberrors.ErrBind
			}
			i++
		default:
			return false, dberrors.ErrBind
		}
		if i > len(tokens) {
			return false, dberrors.ErrBind
		}
	}
	return unsigned, nil
}

func routineTypeFields(row []driver.Value, typ string, numbers []int64, enumLength int64, unsigned bool) error {
	integerPrecision := map[string]int64{metadataLowerTinyInt: 3, metadataLowerSmallInt: 5, metadataLowerMediumInt: 7, "int": 10, metadataLowerInteger: 10, metadataLowerBigInt: 19}
	if p, ok := integerPrecision[typ]; ok {
		if len(numbers) > 1 {
			return dberrors.ErrBind
		}
		if unsigned && (typ == metadataLowerBigInt || typ == metadataLowerMediumInt) {
			p++
		}
		row[10], row[11] = p, int64(0)
		return nil
	}
	switch typ {
	case metadataLowerChar, metadataLowerVarchar, charsetBinary, "varbinary":
		if len(numbers) == 1 {
			row[9] = numbers[0]
		} else if len(numbers) > 1 {
			return dberrors.ErrBind
		}
		if row[9] == nil && (typ == metadataLowerChar || typ == charsetBinary) {
			row[9] = int64(1)
		}
	case metadataLowerDecimal, metadataLowerNumeric, "dec", "fixed":
		return routineDecimalFields(row, numbers)
	case metadataLowerFloat, metadataLowerDouble, metadataLowerReal:
		if len(numbers) == 2 {
			row[10], row[11] = numbers[0], numbers[1]
		} else if len(numbers) > 2 {
			return dberrors.ErrBind
		}
	case "time", "datetime", metadataLowerTimestamp:
		if len(numbers) == 1 {
			if numbers[0] > 6 {
				return dberrors.ErrBind
			}
			row[11] = numbers[0]
		} else if len(numbers) > 1 {
			return dberrors.ErrBind
		}
	case "bit":
		row[10] = int64(1)
		if len(numbers) == 1 {
			row[10] = numbers[0]
		} else if len(numbers) > 1 {
			return dberrors.ErrBind
		}
	case metadataLowerEnum, metadataLowerSet:
		row[9] = enumLength
	case "year":
		if len(numbers) > 1 || len(numbers) == 1 && numbers[0] != 4 {
			return dberrors.ErrBind
		}
	case metadataLowerDate, "tinytext", "text", "mediumtext", "longtext", "tinyblob", "blob", "mediumblob", "longblob", "json":
		if len(numbers) > 0 {
			return dberrors.ErrBind
		}
	default:
		return dberrors.ErrUnsupported
	}
	return nil
}

func routineDecimalFields(row []driver.Value, numbers []int64) error {
	if len(numbers) > 2 {
		return dberrors.ErrBind
	}
	row[10], row[11] = int64(10), int64(0)
	if len(numbers) > 0 {
		row[10] = numbers[0]
	}
	if len(numbers) > 1 {
		row[11] = numbers[1]
	}
	if row[10].(int64) < 1 || row[10].(int64) > 65 || row[11].(int64) > 30 || row[11].(int64) > row[10].(int64) {
		return dberrors.ErrBind
	}
	return nil
}
