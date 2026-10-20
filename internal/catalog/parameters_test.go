package catalog

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

const routineApp = "app"
const routineOther = "other"

func TestRoutineParameterPlan(t *testing.T) {
	plan, err := sqltext.BuildBindPlan("CALL app.p(1,?,?)", config.ModeMySQL)
	if err != nil {
		t.Fatal(err)
	}
	call, err := sqltext.NamedRoutine(plan.Query, false)
	if err != nil {
		t.Fatal(err)
	}
	rows := [][]driver.Value{{metadataCatalogMySQL, routineApp, "p", "p", nil, "literal", int64(1), "IN"}, {metadataCatalogMySQL, routineApp, "p", "p", nil, "a", int64(2), "IN"}, {metadataCatalogMySQL, routineApp, "p", "p", nil, "b", int64(3), routineDirectionOut}}
	bound, directions, err := RoutineParameterPlan(plan, call, rows, false, 4096)
	if err != nil || !bound.Named || bound.Slots[0].Name != "A" || bound.Slots[1].Name != "B" || !reflect.DeepEqual(directions, []string{"IN", routineDirectionOut}) {
		t.Fatal(bound, directions, err)
	}
	if plan.Named || plan.Slots[0].Name != "" {
		t.Fatal("mutated source plan", plan)
	}
	for _, mutate := range []func([][]driver.Value){
		func(v [][]driver.Value) { v[0][7] = routineDirectionOut },
		func(v [][]driver.Value) { v[1][1] = routineOther },
		func(v [][]driver.Value) { v[1][2] = routineOther },
		func(v [][]driver.Value) { v[1][3] = routineOther },
		func(v [][]driver.Value) { v[1][3] = "" },
		func(v [][]driver.Value) { v[1][5] = "LITERAL" },
		func(v [][]driver.Value) { v[1][6] = int64(1) },
		func(v [][]driver.Value) { v[1][7] = "RETURN" },
	} {
		copyRows := make([][]driver.Value, len(rows))
		for i := range rows {
			copyRows[i] = append([]driver.Value(nil), rows[i]...)
		}
		mutate(copyRows)
		if _, _, err := RoutineParameterPlan(plan, call, copyRows, false, 4096); !errors.Is(err, dberrors.ErrBind) {
			t.Fatal(copyRows, err)
		}
	}
	if _, _, err := RoutineParameterPlan(plan, call, rows, true, 4096); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal(err)
	}
	if _, _, err := RoutineParameterPlan(plan, call, rows, false, 1); !errors.Is(err, wire.ErrMessageTooLarge) {
		t.Fatal(err)
	}
	if _, _, err := RoutineParameterPlan(plan, call, rows[:2], false, 4096); !errors.Is(err, dberrors.ErrBind) {
		t.Fatal(err)
	}
}
