package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const routineFixtureVarchar = "VARCHAR"

func TestRoutineFailureAssertions(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, name := range []string{"routines", "routines-client"} {
			for _, tc := range []struct {
				name                  string
				mysql, oracle         uint32
				other                 error
				wrap, publish, wantOK bool
			}{
				{name: "intentional-exception", mysql: 1644, oracle: 20001, wantOK: true},
				{name: "wrapped-exception", mysql: 1644, oracle: 20001, wrap: true, wantOK: true},
				{name: "syntax-rejected", mysql: 1064, oracle: 900},
				{name: "permission-rejected", mysql: 1142, oracle: 1031},
				{name: "procedure-missing", mysql: 1305, oracle: 6550},
				{name: "constraint-error", mysql: 1062, oracle: 1},
				{name: "other-mode-exception", mysql: 20001, oracle: 1644},
				{name: "zero-error-code"},
				{name: "unexpected-success"},
				{name: "transport-failure", other: errors.New("fixture transport failure")},
				{name: "call-timeout", other: context.DeadlineExceeded},
				{name: "published-on-error", mysql: 1644, oracle: 20001, publish: true},
			} {
				t.Run(modeName(mode)+"/"+name+"/"+tc.name, func(t *testing.T) {
					var failure error = &oceanbase.Error{Code: tc.mysql, SQLState: "45000", Message: "private fixture"}
					if mode == oceanbase.ModeOracle {
						failure = &oceanbase.Error{Code: tc.oracle, SQLState: "HY000", Message: "private fixture"}
					}
					if tc.name == "unexpected-success" || tc.other != nil {
						failure = tc.other
					}
					if tc.wrap {
						failure = fmt.Errorf("wrapped: %w", failure)
					}
					steps := routineFixtureSteps(t, mode, failure, tc.publish)
					s := fixtureScenario(t, steps)
					s.mode = mode
					var definition caseDefinition
					for _, d := range caseDefinitions() {
						if d.name == name {
							definition = d
						}
					}
					if definition.run == nil || definition.configure == nil {
						t.Fatal("routine scenario missing")
					}
					definition.configure(&s.cfg)
					wantMode := oceanbase.PrepareServer
					if name == "routines-client" {
						wantMode = oceanbase.PrepareClient
					}
					if s.cfg.PrepareMode != wantMode {
						t.Fatal("routine scenario did not select its preparation mode")
					}
					result := runScenario(context.Background(), s, definition.run)
					want := statusFail
					if tc.wantOK {
						want = statusPass
					}
					if result.Status != want || len(result.Objects) != 3 || len(result.CleanupErrors) != 0 {
						t.Fatalf("routine failure assertion: %+v; want %s with cleanup", result, want)
					}
					for _, object := range result.Objects {
						if object.State != fixtureDroppedState {
							t.Errorf("routine object not dropped: %+v", object)
						}
					}
				})
			}
		}
	}
}

func routineFixtureSteps(t *testing.T, mode oceanbase.Mode, failure error, publish bool) []fixtureStep {
	t.Helper()
	steps := []fixtureStep{
		{query: "CREATE PROCEDURE OBT_OFFLINE_P (IN x BIGINT,INOUT y BIGINT,OUT z VARCHAR(40)) BEGIN SET y=x+y; SET z='ok'; END"},
		{query: "CREATE FUNCTION OBT_OFFLINE_F (x BIGINT) RETURNS BIGINT DETERMINISTIC RETURN x*2"},
		{query: "CREATE PROCEDURE OBT_OFFLINE_E (OUT y BIGINT) BEGIN SET y=100; SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='obtest'; END"},
	}
	call, function, failing := "CALL OBT_OFFLINE_P(?,?,?)", "SELECT OBT_OFFLINE_F(?) INTO ?", "CALL OBT_OFFLINE_E(?)"
	if mode == oceanbase.ModeOracle {
		steps = []fixtureStep{
			{query: "CREATE PROCEDURE OBT_OFFLINE_P (x IN NUMBER,y IN OUT NUMBER,z OUT VARCHAR2) AS BEGIN y:=x+y; z:='ok'; END;"},
			{query: "CREATE FUNCTION OBT_OFFLINE_F (x IN NUMBER) RETURN NUMBER AS BEGIN RETURN x*2; END;"},
			{query: "CREATE PROCEDURE OBT_OFFLINE_E (y OUT NUMBER) AS BEGIN y:=100; RAISE_APPLICATION_ERROR(-20001,'obtest'); END;"},
		}
		call, function, failing = "BEGIN OBT_OFFLINE_P(?,?,?); END;", "BEGIN ? := OBT_OFFLINE_F(?); END;", "BEGIN OBT_OFFLINE_E(?); END;"
	}
	for i := range 2 {
		steps = append(steps, fixtureStep{query: call, inspect: func(args []driver.NamedValue) {
			if len(args) != 3 || args[0].Value != 2 {
				t.Error("routine IN argument changed")
				return
			}
			y, okY := args[1].Value.(sql.Out)
			z, okZ := args[2].Value.(oceanbase.Out)
			if !okY || !okZ || !y.In || z.Type != routineFixtureVarchar || z.Length != 40 {
				t.Error("routine output types or direction changed")
				return
			}
			if got := *y.Dest.(*int64); got != int64(40+i*2) {
				t.Errorf("routine INOUT input=%d", got)
			}
			*y.Dest.(*int64), *z.Dest.(*string) = int64(42+i*2), "ok"
		}})
	}
	steps = append(steps, fixtureStep{query: function, inspect: func(args []driver.NamedValue) {
		input, output := 0, 1
		if mode == oceanbase.ModeOracle {
			input, output = 1, 0
		}
		if len(args) != 2 || args[input].Value != 21 {
			t.Error("function IN argument changed")
			return
		}
		out, ok := args[output].Value.(sql.Out)
		if !ok || out.In {
			t.Error("function output direction changed")
			return
		}
		*out.Dest.(*oceanbase.Number) = "42"
	}})
	steps = append(steps, fixtureStep{query: failing, err: failure, inspect: func(args []driver.NamedValue) {
		if len(args) != 1 {
			t.Error("failing routine output missing")
			return
		}
		out, ok := args[0].Value.(sql.Out)
		if !ok || out.In {
			t.Error("failing routine output direction changed")
			return
		}
		if *out.Dest.(*int64) != 7 {
			t.Error("failing routine sentinel changed before execution")
		}
		if publish {
			*out.Dest.(*int64) = 100
		}
	}}, fixtureStep{query: "DROP PROCEDURE OBT_OFFLINE_E"}, fixtureStep{query: "DROP FUNCTION OBT_OFFLINE_F"}, fixtureStep{query: "DROP PROCEDURE OBT_OFFLINE_P"})
	return steps
}
