package main

import (
	"context"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

func TestTopologySourcesWorkflow(t *testing.T) {
	definition := modeNeutralScenario(t, topologyCase)
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		t.Run(modeName(mode), func(t *testing.T) {
			s := fixtureScenario(t, nil)
			s.mode = mode
			result := runScenario(context.Background(), s, definition.run)
			if result.Status != statusPass || len(result.Objects) != 0 || len(result.CleanupErrors) != 0 {
				t.Fatalf("topology sources workflow (%s): %+v", modeName(mode), result)
			}
		})
	}
}
