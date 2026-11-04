package main

import (
	"context"
	"errors"
	"testing"
)

func TestOracleWarningFixtureCreationFailure(t *testing.T) {
	failure := errors.New("fixture creation failed")
	s := fixtureScenario(t, []fixtureStep{{query: "CREATE PROCEDURE OBT_OFFLINE_W IS BEGIN OBT_OFFLINE_M; END;", err: failure}})
	if err := runWarnings(context.Background(), s); !errors.Is(err, failure) {
		t.Fatal("Oracle warning fixture creation was skipped or its error changed", err)
	}
	if len(s.cleanups) != 0 {
		t.Fatal("unconfirmed warning fixture registered a destructive cleanup")
	}
}

func TestOracleWarningFixtureCleanupAfterExecutionFailure(t *testing.T) {
	s := fixtureScenario(t, []fixtureStep{
		{query: "CREATE PROCEDURE OBT_OFFLINE_W IS BEGIN OBT_OFFLINE_M; END;"},
		{query: "CREATE OR REPLACE PROCEDURE OBT_OFFLINE_W IS BEGIN OBT_OFFLINE_M; END;", err: errors.New("execution failed")},
		{query: "DROP PROCEDURE OBT_OFFLINE_W"},
	})
	r := runScenario(context.Background(), s, runWarnings)
	if r.Status != statusFail || len(r.Objects) != 1 || r.Objects[0].State != fixtureDroppedState {
		t.Fatal("confirmed warning fixture leaked after execution failure", r)
	}
}
