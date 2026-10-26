package main

import (
	"context"
	"database/sql/driver"
	"reflect"
	"testing"
	"time"

	"github.com/cocosip/go-oceanbase"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestTemporalTimeWorkflow(t *testing.T) {
	for _, name := range []string{"temporal-time-server", "temporal-time-client"} {
		var definition caseDefinition
		for _, candidate := range caseDefinitions() {
			if candidate.name == name {
				definition = candidate
			}
		}
		if definition.run == nil || definition.configure == nil {
			t.Fatalf("missing %s acceptance case", name)
		}
		for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
			for _, valid := range []bool{true, false} {
				t.Run(name+"/"+modeName(mode)+map[bool]string{true: "/valid", false: "/wrong-instant"}[valid], func(t *testing.T) {
					stamp := time.Date(2024, 6, 1, 12, 30, 45, 123456789, time.UTC)
					date := time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC)
					ddl := "(ID INTEGER PRIMARY KEY,D DATE,DT DATETIME(6),TS TIMESTAMP(6),TM TIME(6),Y YEAR) ENGINE=InnoDB"
					fields, types := "D,DT,TS,TM,Y", []byte{10, 12, 7, 11, 13}
					values := []driver.Value{date, stamp.Truncate(time.Microsecond), stamp.Truncate(time.Microsecond), "-25:00:00.123456", int64(2024)}
					if mode == oceanbase.ModeOracle {
						ddl = "(ID INTEGER PRIMARY KEY,D DATE,DT TIMESTAMP(9),TS TIMESTAMP(9) WITH TIME ZONE,LTZ TIMESTAMP(9) WITH LOCAL TIME ZONE)"
						fields, types = "D,DT,TS,LTZ", []byte{12, 202, 200, 201}
						date = date.Add(12*time.Hour + 34*time.Minute + 56*time.Second)
						values = []driver.Value{date, stamp, stamp.In(time.FixedZone("", -1800)), stamp}
					}
					if !valid {
						values[1] = stamp.Add(time.Hour)
					}
					marks := "?,?,?,?,?,?"
					if mode == oceanbase.ModeOracle {
						marks = "?,?,?,?,?"
					}
					steps := []fixtureStep{{query: "CREATE TABLE OBT_OFFLINE_T " + ddl}, {query: "INSERT INTO OBT_OFFLINE_T VALUES (" + marks + ")", affected: 1}, {query: "INSERT INTO OBT_OFFLINE_T VALUES (" + marks + ")", affected: 1}}
					for _, parameterized := range []bool{false, true} {
						query := "SELECT " + fields + " FROM OBT_OFFLINE_T WHERE ID>=1 ORDER BY ID"
						if parameterized {
							query = "SELECT " + fields + " FROM OBT_OFFLINE_T WHERE ID>=? ORDER BY ID"
						}
						columns := make([]wire.Column, len(types))
						for i, typ := range types {
							columns[i] = wire.Column{Type: typ}
						}
						rows := rowset.New(context.Background(), mode, true, []rowset.Set{{Columns: columns, Rows: [][]driver.Value{append([]driver.Value(nil), values...), make([]driver.Value, len(types))}}})
						steps = append(steps, fixtureStep{query: query, rows: rows})
						if !valid {
							break
						}
					}
					steps = append(steps, fixtureStep{query: fixtureDropTableSQL})
					s := fixtureScenario(t, steps)
					s.mode, s.cfg.Mode, s.cfg.Session.Location = mode, mode, time.UTC
					definition.configure(&s.cfg)
					if !s.cfg.Session.ParseTime || s.cfg.Session.CompatibleMysqlVersion != 8 {
						t.Fatal("time scenario configuration lost")
					}
					result := runScenario(context.Background(), s, definition.run)
					want := statusPass
					if !valid {
						want = statusFail
					}
					if result.Status != want || len(result.CleanupErrors) != 0 || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState {
						t.Fatalf("unexpected result: %+v", result)
					}
					if reflect.TypeOf(values[1]) != reflect.TypeFor[time.Time]() {
						t.Fatal("fixture must exercise time.Time sources")
					}
				})
			}
		}
	}
}
