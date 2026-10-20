package catalog

import (
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	routineDirectionOut   = "OUT"
	routineDirectionInOut = "INOUT"
)

// RoutineParameterPlan validates one bounded parameter dictionary and names physical slots.
func RoutineParameterPlan(plan sqltext.Plan, call sqltext.RoutineCall, rows [][]driver.Value, more bool, limit int64) (sqltext.Plan, []string, error) {
	plan.Named = true
	plan.Slots = append([]sqltext.Slot(nil), plan.Slots...)
	directions := make([]string, len(plan.Slots))
	seen := make(map[string]bool, len(call.Arguments))
	ordinal := 0
	var budget int64
	var specific string
	for _, values := range rows {
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
		if ordinal > len(call.Arguments) || values[1] != call.Schema || !strings.EqualFold(routine, call.Name) || nextSpecific == "" || nextSpecific != specific || values[6] != int64(ordinal) || name == "" || seen[name] || direction != "IN" && direction != routineDirectionOut && direction != routineDirectionInOut {
			return plan, nil, fmt.Errorf("%w: inconsistent routine metadata", dberrors.ErrBind)
		}
		seen[name] = true
		position := call.Arguments[ordinal-1]
		if position < 0 {
			if direction != "IN" {
				return plan, nil, fmt.Errorf("%w: output routine arguments require placeholders", dberrors.ErrBind)
			}
			continue
		}
		if position >= len(plan.Slots) {
			return plan, nil, fmt.Errorf("%w: routine placeholder count", dberrors.ErrBind)
		}
		plan.Slots[position].Name = name
		directions[position] = direction
	}
	if ordinal != len(call.Arguments) || more {
		return plan, nil, fmt.Errorf("%w: routine metadata argument count", dberrors.ErrBind)
	}
	return plan, directions, nil
}
