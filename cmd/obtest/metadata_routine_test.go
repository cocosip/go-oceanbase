package main

import (
	"context"
	"database/sql/driver"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	metadataRoutineProcFixMY = "CREATE PROCEDURE OBT_OFFLINE_PROC (pIn INTEGER, OUT pOut VARCHAR(20)) BEGIN SET pOut='x'; END"
	metadataRoutineProcFixOR = "CREATE PROCEDURE OBT_OFFLINE_PROC (pIn INTEGER, pOut OUT VARCHAR2) AS BEGIN pOut:='x'; END;"
	metadataRoutineFuncFixMY = "CREATE FUNCTION OBT_OFFLINE_FUNC (pIn INTEGER) RETURNS INTEGER DETERMINISTIC RETURN pIn+1"
	metadataRoutineFuncFixOR = "CREATE FUNCTION OBT_OFFLINE_FUNC (pIn INTEGER) RETURN INTEGER AS BEGIN RETURN pIn+1; END;"
	metadataRoutineDropFunc  = "DROP FUNCTION OBT_OFFLINE_FUNC"
	metadataRoutineDropProc  = "DROP PROCEDURE OBT_OFFLINE_PROC"
	metadataRoutineProcName  = "OBT_OFFLINE_PROC"
	metadataRoutineFuncName  = "OBT_OFFLINE_FUNC"
)

type metadataRoutineFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep)
	wantOK bool
}

func metadataRoutineFixtureCases() []metadataRoutineFixtureCase {
	return []metadataRoutineFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "procedure-kind", failAt: 2, mutate: func(body *[]fixtureStep) {
			(*body)[2].rows = newMetadataRoutineRows("FUNCTION")
		}},
		{name: "function-kind", failAt: 3, mutate: func(body *[]fixtureStep) {
			(*body)[3].rows = newMetadataRoutineRows("PROCEDURE")
		}},
		{name: "procedure-direction", failAt: 4, mutate: func(body *[]fixtureStep) {
			rows := newMetadataRoutineParamRows(true)
			rows.values[1][7] = "IN"
			(*body)[4].rows = rows
		}},
		{name: "procedure-missing", failAt: 4, mutate: func(body *[]fixtureStep) {
			rows := newMetadataRoutineParamRows(true)
			rows.values = rows.values[:1]
			(*body)[4].rows = rows
		}},
		{name: "function-return", failAt: 5, mutate: func(body *[]fixtureStep) {
			rows := newMetadataRoutineParamRows(false)
			rows.values[0][6] = int64(1)
			(*body)[5].rows = rows
		}},
		{name: "function-arg-direction", failAt: 5, mutate: func(body *[]fixtureStep) {
			rows := newMetadataRoutineParamRows(false)
			rows.values[1][7] = "OUT"
			(*body)[5].rows = rows
		}},
		{name: caseExtraRow, failAt: 5, mutate: func(body *[]fixtureStep) {
			rows := newMetadataRoutineParamRows(false)
			rows.values = append(rows.values, rows.values[1])
			(*body)[5].rows = rows
		}},
	}
}

func TestMetadataRoutineWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, metadataRoutineCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, tc := range metadataRoutineFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := metadataRoutineSteps(t, mode, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 2 || len(result.CleanupErrors) != 0 {
					t.Fatalf("metadata routine workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
				for _, object := range result.Objects {
					if object.State != fixtureDroppedState {
						t.Fatalf("metadata routine object %s state=%s", object.Name, object.State)
					}
				}
			})
		}
	}
}

func metadataRoutineSteps(t *testing.T, mode oceanbase.Mode, tc metadataRoutineFixtureCase) []fixtureStep {
	t.Helper()
	proc, function := metadataRoutineProcFixMY, metadataRoutineFuncFixMY
	if mode == oceanbase.ModeOracle {
		proc, function = metadataRoutineProcFixOR, metadataRoutineFuncFixOR
	}
	body := []fixtureStep{
		{query: proc},
		{query: function},
		{query: "", rows: newMetadataRoutineRows("PROCEDURE")},
		{query: "", rows: newMetadataRoutineRows("FUNCTION")},
		{query: "", rows: newMetadataRoutineParamRows(true)},
		{query: "", rows: newMetadataRoutineParamRows(false)},
	}
	if tc.mutate != nil {
		tc.mutate(&body)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return append(body,
		fixtureStep{query: metadataRoutineDropFunc},
		fixtureStep{query: metadataRoutineDropProc},
	)
}

type metadataRoutineRows struct {
	fixtureRows
}

func newMetadataRoutineRows(kind string) *metadataRoutineRows {
	name := metadataRoutineProcName
	if kind == "FUNCTION" {
		name = metadataRoutineFuncName
	}
	return &metadataRoutineRows{fixtureRows: fixtureRows{width: 7, values: [][]driver.Value{
		{nil, "S", name, name + ":1", nil, kind, nil},
	}}}
}

type metadataRoutineParamRows struct {
	fixtureRows
}

func newMetadataRoutineParamRows(procedure bool) *metadataRoutineParamRows {
	rows := &metadataRoutineParamRows{fixtureRows: fixtureRows{width: 15}}
	if procedure {
		rows.values = [][]driver.Value{
			{nil, "S", metadataRoutineProcName, metadataRoutineProcName + ":1", nil, "pIn", int64(1), "IN", generatedTypeName, int64(22), int64(38), int64(0), nil, nil, int64(0)},
			{nil, "S", metadataRoutineProcName, metadataRoutineProcName + ":1", nil, "pOut", int64(2), "OUT", typeNameVarchar2, int64(20), nil, nil, nil, nil, int64(0)},
		}
		return rows
	}
	rows.values = [][]driver.Value{
		{nil, "S", metadataRoutineFuncName, metadataRoutineFuncName + ":1", nil, nil, int64(0), metadataRoutineReturn, generatedTypeName, int64(22), int64(38), int64(0), nil, nil, int64(0)},
		{nil, "S", metadataRoutineFuncName, metadataRoutineFuncName + ":1", nil, "pIn", int64(1), "IN", generatedTypeName, int64(22), int64(38), int64(0), nil, nil, int64(0)},
	}
	return rows
}
