package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type routineCall struct {
	schema, name string
	// Each routine argument maps to its placeholder, or -1 for a literal.
	arguments []int
}

const (
	routineDirectionOut   = "OUT"
	routineDirectionInOut = "INOUT"
)

func hasNamedArguments(args []driver.NamedValue) bool {
	for _, arg := range args {
		if arg.Name != "" {
			return true
		}
	}
	return false
}

// Only simple CALL syntax can establish an unambiguous routine-to-placeholder mapping.
func parseNamedRoutine(query string, noBackslash bool) (routineCall, error) {
	bad := fmt.Errorf("%w: named MySQL arguments require a simple CALL", ErrBind)
	tokens, err := lexSQL(query, ModeMySQL, noBackslash, false)
	if err != nil {
		return routineCall{}, err
	}
	if len(tokens) < 4 || tokens[0].kind != 'w' || !strings.EqualFold(query[tokens[0].start:tokens[0].end], "CALL") {
		return routineCall{}, bad
	}
	identifier := func(t sqlToken) string {
		text := query[t.start:t.end]
		if t.kind == 'q' && text[0] == '`' {
			return strings.ReplaceAll(text[1:len(text)-1], "``", "`")
		}
		if t.kind == 'w' && (text[0] < '0' || text[0] > '9') {
			return text
		}
		return ""
	}
	call := routineCall{name: identifier(tokens[1])}
	i := 2
	if tokens[i].kind == '.' {
		if len(tokens) < 6 {
			return routineCall{}, bad
		}
		call.schema, call.name = call.name, identifier(tokens[i+1])
		i += 2
		if call.schema == "" {
			return routineCall{}, bad
		}
	}
	if call.name == "" || tokens[i].kind != '(' {
		return routineCall{}, bad
	}
	i++
	call.arguments, i, err = parseRoutineArguments(query, tokens, i)
	if err != nil {
		return routineCall{}, err
	}
	if i < len(tokens) && tokens[i].kind == ';' {
		i++
	}
	if i != len(tokens) {
		return routineCall{}, bad
	}
	return call, nil
}

func parseRoutineArguments(query string, tokens []sqlToken, i int) ([]int, int, error) {
	bad := fmt.Errorf("%w: named CALL requires literal or placeholder arguments", ErrBind)
	var arguments []int
	placeholders := 0
	for i < len(tokens) && tokens[i].kind != ')' {
		start := i
		for i < len(tokens) && tokens[i].kind != ',' && tokens[i].kind != ')' {
			i++
		}
		if i == start || i == len(tokens) {
			return nil, 0, bad
		}
		position := -1
		if i == start+1 && tokens[start].kind == '?' {
			position = placeholders
			placeholders++
		} else if !routineLiteral(query, tokens[start:i]) {
			return nil, 0, bad
		}
		arguments = append(arguments, position)
		if len(arguments) > 65535 {
			return nil, 0, bad
		}
		if tokens[i].kind == ',' {
			i++
			if i == len(tokens) || tokens[i].kind == ')' {
				return nil, 0, bad
			}
		}
	}
	if i == len(tokens) || tokens[i].kind != ')' {
		return nil, 0, bad
	}
	return arguments, i + 1, nil
}

func routineLiteral(query string, tokens []sqlToken) bool {
	text := query[tokens[0].start:tokens[len(tokens)-1].end]
	if len(tokens) == 1 {
		if tokens[0].kind == 'q' && text[0] == '\'' {
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

func exactRoutinePattern(name string) string {
	return strings.NewReplacer("/", "//", "%", "/%", "_", "/_").Replace(name)
}

// bindCommandArgumentsLocked reads fresh metadata and binds under the same physical session gate.
func (c *conn) bindCommandArgumentsLocked(ctx context.Context, plan bindPlan, args []driver.NamedValue) (_ []boundValue, err error) {
	if c.session.mode != ModeMySQL || !hasNamedArguments(args) {
		return bindArguments(plan, args)
	}
	if c.responseActive {
		return nil, errResponseActive
	}
	if _, err := c.encodeSQL(plan.query); err != nil {
		return nil, err
	}
	call, err := parseNamedRoutine(plan.query, c.session.ok.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	if len(args) != plan.numInput {
		return nil, fmt.Errorf("%w: argument count", ErrBind)
	}
	names := make(map[string]bool, len(args))
	for _, arg := range args {
		name := strings.ToUpper(arg.Name)
		if name == "" || names[name] {
			return nil, fmt.Errorf("%w: missing or duplicate named argument", ErrBind)
		}
		names[name] = true
	}
	if call.schema == "" {
		values, e := c.sessionValuesLocked(ctx, []byte("SELECT DATABASE()"), 1)
		if e != nil {
			return nil, e
		}
		if !values[0].Valid || values[0].String == "" {
			return nil, fmt.Errorf("%w: CALL requires a current schema", ErrBind)
		}
		call.schema = values[0].String
	}
	rows, err := c.metadataLocked(ctx, MetadataFilter{Kind: MetadataRoutineParameters, SchemaPattern: exactRoutinePattern(call.schema), NamePattern: exactRoutinePattern(call.name)}, true)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	metadata := &metadataRows{Rows: rows, columns: metadataColumns(MetadataRoutineParameters)}
	plan, directions, err := routineParameterPlan(plan, call, metadata, c.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	values, err := bindArguments(plan, args)
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		out, output := value.value.(*outBinding)
		if directions[i] == "" || directions[i] == "IN" && output || directions[i] == routineDirectionOut && (!output || out.In) || directions[i] == routineDirectionInOut && (!output || !out.In) {
			return nil, fmt.Errorf("%w: routine argument direction", ErrBind)
		}
	}
	return values, nil
}

func routineParameterPlan(plan bindPlan, call routineCall, metadata *metadataRows, limit int64) (bindPlan, []string, error) {
	plan.named = true
	plan.slots = append([]bindSlot(nil), plan.slots...)
	directions := make([]string, len(plan.slots))
	seen := make(map[string]bool, len(call.arguments))
	ordinal := 0
	var budget int64
	var specific string
	for {
		values := make([]driver.Value, len(metadata.columns))
		if err := metadata.Next(values); err == io.EOF {
			break
		} else if err != nil {
			return plan, nil, err
		}
		ordinal++
		budget += int64(len(values)) * 16
		for _, value := range values {
			if text, ok := value.(string); ok {
				budget += int64(len(text))
			}
		}
		if budget > limit {
			return plan, nil, wire.ErrMessageTooLarge
		}
		name, _ := values[5].(string)
		name = strings.ToUpper(name)
		direction, _ := values[7].(string)
		direction = strings.ToUpper(direction)
		routine, _ := values[2].(string)
		nextSpecific, _ := values[3].(string)
		if ordinal == 1 {
			specific = nextSpecific
		}
		if ordinal > len(call.arguments) || values[1] != call.schema || !strings.EqualFold(routine, call.name) || nextSpecific == "" || nextSpecific != specific || values[6] != int64(ordinal) || name == "" || seen[name] || direction != "IN" && direction != routineDirectionOut && direction != routineDirectionInOut {
			return plan, nil, fmt.Errorf("%w: inconsistent routine metadata", ErrBind)
		}
		seen[name] = true
		position := call.arguments[ordinal-1]
		if position < 0 {
			if direction != "IN" {
				return plan, nil, fmt.Errorf("%w: output routine arguments require placeholders", ErrBind)
			}
			continue
		}
		if position >= len(plan.slots) {
			return plan, nil, fmt.Errorf("%w: routine placeholder count", ErrBind)
		}
		plan.slots[position].name = name
		directions[position] = direction
	}
	if ordinal != len(call.arguments) || metadata.Rows.(*textRows).more {
		return plan, nil, fmt.Errorf("%w: routine metadata argument count", ErrBind)
	}
	return plan, directions, nil
}
