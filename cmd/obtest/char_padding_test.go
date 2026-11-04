package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	charPaddingInsertFix = "INSERT INTO OBT_OFFLINE_T VALUES (?,?,?)"
	charPaddingSelectFix = "SELECT C,V FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
	charPaddingBoundFix  = "SELECT C,V FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
	charPaddingBytesFix  = "SELECT C,V FROM OBT_OFFLINE_T WHERE ID=1"
	charPaddingMatchFix  = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE C='ab'"
	charPaddingTrimFix   = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE RTRIM(C)='ab'"
	charPaddingCountFix  = "SELECT COUNT(*) FROM OBT_OFFLINE_T WHERE ID>=90"
	charPaddingWrong     = "zz"
)

type charPaddingFixtureCase struct {
	name   string
	failAt int
	mutate func(body *[]fixtureStep, profile charPaddingProfile)
	wantOK bool
}

func charPaddingFixtureCases() []charPaddingFixtureCase {
	return []charPaddingFixtureCase{
		{name: caseRoundTrip, failAt: -1, wantOK: true},
		{name: "padded-changed", failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			(*body)[4].rows = newCharPaddingRows(p, charPaddingWrong)
		}},
		{name: "varchar-changed", failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			rows := newCharPaddingRows(p, p.padded)
			rows.values[0][1] = charPaddingWrong
			(*body)[4].rows = rows
		}},
		{name: caseNullChanged, failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			rows := newCharPaddingRows(p, p.padded)
			rows.values[2] = []driver.Value{"a", "a"}
			(*body)[4].rows = rows
		}},
		{name: caseMetadataType, failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			rows := newCharPaddingRows(p, p.padded)
			rows.types[0] = blobType
			(*body)[4].rows = rows
		}},
		{name: "length-changed", failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			rows := newCharPaddingRows(p, p.padded)
			rows.lengths[0] = 6
			(*body)[4].rows = rows
		}},
		{name: caseExtraRow, failAt: 4, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			rows := newCharPaddingRows(p, p.padded)
			rows.values = append(rows.values, rows.values[0])
			(*body)[4].rows = rows
		}},
		{name: "bound-changed", failAt: 5, mutate: func(body *[]fixtureStep, p charPaddingProfile) {
			(*body)[5].rows = newCharPaddingRows(p, charPaddingWrong)
		}},
		{name: "bytes-changed", failAt: 6, mutate: func(body *[]fixtureStep, _ charPaddingProfile) {
			(*body)[6].values = [][]driver.Value{{charPaddingWrong, charPaddingWrong}}
		}},
		{name: "match-count", failAt: 7, mutate: func(body *[]fixtureStep, _ charPaddingProfile) {
			(*body)[7].values = [][]driver.Value{{int64(0)}}
		}},
		{name: "trim-count", failAt: 8, mutate: func(body *[]fixtureStep, _ charPaddingProfile) {
			(*body)[8].values = [][]driver.Value{{int64(0)}}
		}},
		{name: "overflow-code", failAt: 9, mutate: func(body *[]fixtureStep, _ charPaddingProfile) {
			code := uint32(1064)
			(*body)[9].err = &oceanbase.Error{Code: code}
		}},
	}
}

func TestCharPaddingWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, charPaddingCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		profile := charPaddingProfileFor(mode)
		for _, tc := range charPaddingFixtureCases() {
			t.Run(modeName(mode)+"/"+tc.name, func(t *testing.T) {
				steps := charPaddingSteps(t, mode, profile, tc)
				s := fixtureScenario(t, steps)
				s.mode = mode
				result := runScenario(context.Background(), s, definition.run)
				want := statusFail
				if tc.wantOK {
					want = statusPass
				}
				if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
					t.Fatalf("char padding workflow (%s): %+v; want %s", modeName(mode), result, want)
				}
			})
		}
	}
}

func charPaddingSteps(t *testing.T, mode oceanbase.Mode, profile charPaddingProfile, tc charPaddingFixtureCase) []fixtureStep {
	t.Helper()
	ddl := "CREATE TABLE OBT_OFFLINE_T " + profile.ddl
	if mode == oceanbase.ModeMySQL {
		ddl += parameterFixtureEngine
	}
	body := []fixtureStep{{query: ddl}}
	for id, value := range []any{charPaddingFloat, charPaddingFull, nil} {
		body = append(body, fixtureStep{query: charPaddingInsertFix, inspect: charPaddingInspect(t, id+1, value)})
	}
	body = append(body,
		fixtureStep{query: charPaddingSelectFix, rows: newCharPaddingRows(profile, profile.padded)},
		fixtureStep{query: charPaddingBoundFix, rows: newCharPaddingRows(profile, profile.padded)},
		fixtureStep{query: charPaddingBytesFix, values: [][]driver.Value{{profile.padded, charPaddingFloat}}},
		fixtureStep{query: charPaddingMatchFix, values: [][]driver.Value{{int64(1)}}},
		fixtureStep{query: charPaddingTrimFix, values: [][]driver.Value{{int64(1)}}},
		fixtureStep{query: charPaddingInsertFix, err: &oceanbase.Error{Code: profile.overflow}},
		fixtureStep{query: charPaddingCountFix, values: [][]driver.Value{{int64(0)}}},
	)
	if tc.mutate != nil {
		tc.mutate(&body, profile)
	}
	if tc.failAt >= 0 {
		body = body[:tc.failAt+1]
	}
	return append(body, fixtureStep{query: fixtureDropTableSQL})
}

func charPaddingInspect(t *testing.T, id int, value any) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		if len(args) != 3 || fixtureArgText(args[0].Value) != fixtureArgText(id) {
			t.Errorf("CHAR insert %d bound unexpected arguments", id)
			return
		}
		if fixtureArgText(args[1].Value) != fixtureArgText(value) || fixtureArgText(args[2].Value) != fixtureArgText(value) {
			t.Errorf("CHAR insert %d bound unexpected values", id)
		}
	}
}

type charPaddingRows struct {
	fixtureRows
	types   []string
	lengths []int64
}

func newCharPaddingRows(profile charPaddingProfile, padValue string) *charPaddingRows {
	return &charPaddingRows{
		fixtureRows: fixtureRows{width: 2, values: [][]driver.Value{
			{padValue, charPaddingFloat},
			{charPaddingFull, charPaddingFull},
			{nil, nil},
		}},
		types:   []string{profile.typeNameC, profile.typeNameV},
		lengths: []int64{5, 10},
	}
}

func (r *charPaddingRows) ColumnTypeDatabaseTypeName(i int) string { return r.types[i] }
func (r *charPaddingRows) ColumnTypeScanType(int) reflect.Type     { return reflect.TypeFor[string]() }
func (r *charPaddingRows) ColumnTypeLength(i int) (int64, bool)    { return r.lengths[i], true }
