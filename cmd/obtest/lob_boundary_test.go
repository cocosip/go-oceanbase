package main

import (
	"context"
	"errors"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestNativeNCLOBDatatypeBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		want    string
	}{
		{"unsupported-datatype", &oceanbase.Error{Code: 902}, statusSkip},
		{"unrelated-internal-error", &oceanbase.Error{Code: 600}, statusFail},
		{"mixed-failure", errors.Join(&oceanbase.Error{Code: 902}, errors.New("connection failed")), statusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixtureScenario(t, []fixtureStep{{query: "CREATE TABLE OBT_OFFLINE_T (N NCLOB)", err: tc.failure}})
			got := runScenario(context.Background(), s, runNativeNCLOB)
			if got.Status != tc.want || len(got.Objects) != 1 || got.Objects[0].State != "create_unconfirmed" || len(got.CleanupErrors) != 0 {
				t.Fatalf("NCLOB boundary: %+v", got)
			}
		})
	}
}
