package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCleanupAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var order []int
	s := &scenario{cleanupTimeout: time.Second}
	s.cleanups = append(s.cleanups, cleanupAction{"first", func(ctx context.Context) error {
		if ctx.Err() != nil {
			t.Error("cleanup inherited cancellation")
		}
		order = append(order, 1)
		return nil
	}}, cleanupAction{"second", func(context.Context) error {
		order = append(order, 2)
		return errors.New("private server text")
	}})
	cancel()
	result := runScenario(ctx, s, func(ctx context.Context, _ *scenario) error { return ctx.Err() })
	if result.Status != statusFail || len(result.CleanupErrors) != 1 || !reflect.DeepEqual(order, []int{2, 1}) {
		t.Fatalf("lost failure or cleanup order: %+v %v", result, order)
	}
	if strings.Contains(result.CleanupErrors[0], "private server text") {
		t.Fatal("raw error exposed")
	}
}

func TestConfigRejectsMistakesWithoutSecrets(t *testing.T) {
	for _, input := range []string{
		`{"targets":[{"name":"test","dsn":"secret","dsnn":"typo"}]}`,
		`{"targets":[{"name":"test","dsn":"secret","dsn_env":"OB_DSN"}]}`,
		`{"targets":[{"name":"test","dsn":"secret"}],"cases":["typo"]}`,
		`{"targets":[{"name":"test","dsn":"secret"}]} {}`,
		`{"targets":[]}`,
		`{"timeout":"0s","targets":[{"name":"test","dsn":"secret"}]}`,
	} {
		_, err := readConfig(strings.NewReader(input))
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid config accepted or exposed: %v", err)
		}
	}
}

func TestResultExitCode(t *testing.T) {
	for _, tc := range []struct {
		statuses []string
		strict   bool
		want     int
	}{
		{[]string{statusPass, statusNA}, true, 0},
		{[]string{statusPass, statusSkip}, false, 3},
		{[]string{statusPass, statusSkip}, true, 1},
		{[]string{statusPass, statusFail}, false, 1},
		{nil, false, 3},
	} {
		r := report{}
		for _, status := range tc.statuses {
			r.Results = append(r.Results, caseResult{Status: status})
		}
		if got := r.exitCode(tc.strict); got != tc.want {
			t.Fatalf("statuses=%v: got %d want %d", tc.statuses, got, tc.want)
		}
	}
}

func TestCleanupFailureChangesPass(t *testing.T) {
	s := &scenario{cleanupTimeout: time.Second, cleanups: []cleanupAction{{"drop own table", func(context.Context) error { return errors.New("private credential") }}}}
	r := runScenario(context.Background(), s, func(context.Context, *scenario) error { return nil })
	if r.Status != statusFail || len(r.CleanupErrors) != 1 || strings.Contains(r.CleanupErrors[0], "private credential") {
		t.Fatalf("cleanup failure hidden or exposed: %+v", r)
	}
}
