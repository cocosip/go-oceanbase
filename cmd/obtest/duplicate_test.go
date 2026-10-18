package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/cocosip/go-oceanbase"
)

const (
	duplicateFixtureInsert     = "INSERT INTO OBT_OFFLINE_T VALUES (?,?)"
	duplicateCRUDCase          = "crud"
	duplicateUnexpectedSuccess = "unexpected-success"
	duplicateValidBatch        = "valid-batch"
	duplicateWrappedBatch      = "wrapped"
)

func TestDuplicateKeyWorkflows(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, name := range []string{duplicateCRUDCase, "optimized-execution"} {
			for _, tc := range []struct {
				name             string
				mysql, oracle    uint32
				other            error
				wrap, join, pass bool
			}{
				{name: "duplicate-key", mysql: 1062, oracle: 1, pass: true},
				{name: "wrapped-duplicate", mysql: 1062, oracle: 1, wrap: true, pass: true},
				{name: "joined-duplicates", mysql: 1062, oracle: 1, join: true, pass: true},
				{name: "joined-unrelated", mysql: 1062, oracle: 1, join: true, other: context.DeadlineExceeded},
				{name: "wrong-mode", mysql: 1, oracle: 1062},
				{name: "syntax", mysql: 1064, oracle: 900},
				{name: "permission", mysql: 1142, oracle: 1031},
				{name: "table-missing", mysql: 1146, oracle: 942},
				{name: "value-too-long", mysql: 1406, oracle: 12899},
				{name: "zero-code"},
				{name: duplicateUnexpectedSuccess},
				{name: "transport", other: errors.New("private transport error")},
				{name: "timeout", other: context.DeadlineExceeded},
				{name: "canceled", other: context.Canceled},
			} {
				t.Run(modeName(mode)+"/"+name+"/"+tc.name, func(t *testing.T) {
					code := tc.mysql
					if mode == oceanbase.ModeOracle {
						code = tc.oracle
					}
					var failure error = &oceanbase.Error{Code: code, SQLState: "23000", Message: "private duplicate fixture"}
					if tc.name == duplicateUnexpectedSuccess || tc.other != nil && !tc.join {
						failure = tc.other
					}
					if tc.join {
						extra := tc.other
						if extra == nil {
							extra = &oceanbase.Error{Code: code}
						}
						failure = errors.Join(failure, extra)
					}
					if tc.wrap {
						failure = fmt.Errorf("wrapped: %w", failure)
					}
					s := fixtureScenario(t, duplicateFixtureSteps(t, mode, name, failure))
					s.mode = mode
					var definition caseDefinition
					for _, d := range caseDefinitions() {
						if d.name == name {
							definition = d
						}
					}
					if definition.run == nil {
						t.Fatal("duplicate-key scenario missing")
					}
					if definition.configure != nil {
						definition.configure(&s.cfg)
					}
					result := runScenario(context.Background(), s, definition.run)
					want := statusFail
					if tc.pass {
						want = statusPass
					}
					if result.Status != want || len(result.Objects) != 1 || result.Objects[0].State != fixtureDroppedState || len(result.CleanupErrors) != 0 {
						t.Fatalf("duplicate-key workflow: %+v; want %s with cleanup", result, want)
					}
				})
			}
		}
	}
}

func duplicateFixtureSteps(t *testing.T, mode oceanbase.Mode, name string, failure error) []fixtureStep {
	t.Helper()
	ddl := parameterFixtureDDL
	if mode == oceanbase.ModeMySQL {
		ddl += parameterFixtureEngine
	}
	steps := []fixtureStep{{query: ddl}}
	if name == duplicateCRUDCase {
		steps = append(steps,
			fixtureStep{query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'seed')", affected: 1},
			fixtureStep{query: "UPDATE OBT_OFFLINE_T SET V='updated' WHERE ID=1", affected: 1},
			fixtureStep{query: parameterFixtureRead, values: [][]driver.Value{{"updated"}}},
			fixtureStep{query: "INSERT INTO OBT_OFFLINE_T VALUES (1,'duplicate')", err: failure},
			fixtureStep{query: "SELECT COUNT(*) FROM " + countFixtureTable, values: [][]driver.Value{{int64(1)}}, optional: true},
			fixtureStep{query: "DELETE FROM OBT_OFFLINE_T WHERE ID=1", affected: 1, optional: true},
			fixtureStep{query: parameterFixtureRead, optional: true})
	} else {
		values := []any{unicodeValue, nil, seedChanged}
		for i, value := range values {
			steps = append(steps, fixtureStep{query: duplicateFixtureInsert, affected: 1, inspect: duplicateArguments(t, i, value)})
		}
		steps = append(steps, fixtureStep{query: duplicateFixtureInsert, err: failure, inspect: duplicateArguments(t, 0, "duplicate")})
		for i, value := range values {
			steps = append(steps, fixtureStep{query: "SELECT V FROM OBT_OFFLINE_T WHERE ID=?", values: [][]driver.Value{{value}}, optional: true, inspect: duplicateArguments(t, i)})
		}
		steps = append(steps, fixtureStep{query: "SELECT COUNT(*) FROM " + countFixtureTable, values: [][]driver.Value{{int64(3)}}, optional: true})
	}
	return append(steps, fixtureStep{query: fixtureDropTableSQL})
}

func duplicateArguments(t *testing.T, want ...any) func([]driver.NamedValue) {
	t.Helper()
	return func(args []driver.NamedValue) {
		var values []any
		for _, arg := range args {
			values = append(values, arg.Value)
		}
		if !reflect.DeepEqual(values, want) {
			t.Error("duplicate-key workflow changed bound arguments")
		}
	}
}

func TestBatchDuplicateAssertions(t *testing.T) {
	for _, mode := range []oceanbase.Mode{oceanbase.ModeMySQL, oceanbase.ModeOracle} {
		for _, keepGoing := range []bool{false, true} {
			for _, fault := range []string{duplicateValidBatch, duplicateWrappedBatch, "wrong-aggregate-code", "wrong-item-code", "wrong-mode-code", "missing-server-error", "missing-item-error", duplicateUnexpectedSuccess, "wrong-index", "wrong-completed", "missing-item", "extra-item", "first-failed", "first-error", "second-succeeded", "last-status", "last-error", "joined-finalization", "joined-summary", "joined-item"} {
				t.Run(fmt.Sprintf("%s/%t/%s", modeName(mode), keepGoing, fault), func(t *testing.T) {
					code, otherMode := uint32(1062), uint32(1)
					if mode == oceanbase.ModeOracle {
						code, otherMode = otherMode, code
					}
					server := &oceanbase.Error{Code: code}
					itemError := &oceanbase.Error{Code: code}
					batch := &oceanbase.BatchError{Index: 1, Completed: 1, Err: server}
					r := oceanbase.BatchResult{Items: []oceanbase.BatchItem{{Status: oceanbase.BatchSucceeded}, {Status: oceanbase.BatchFailed, Err: itemError}, {Status: oceanbase.BatchNotExecuted}}}
					if keepGoing {
						batch.Completed, r.Items[2].Status = 2, oceanbase.BatchSucceeded
					}
					var failure error = batch
					switch fault {
					case duplicateWrappedBatch:
						failure = fmt.Errorf("wrapped: %w", batch)
						r.Items[1].Err = fmt.Errorf("wrapped: %w", itemError)
					case "wrong-aggregate-code":
						server.Code = 1064
					case "wrong-item-code":
						itemError.Code = 1031
					case "wrong-mode-code":
						server.Code, itemError.Code = otherMode, otherMode
					case "missing-server-error":
						batch.Err = context.DeadlineExceeded
					case "missing-item-error":
						r.Items[1].Err = nil
					case duplicateUnexpectedSuccess:
						failure = nil
					case "wrong-index":
						batch.Index = 0
					case "wrong-completed":
						batch.Completed = 3
					case "missing-item":
						r.Items = r.Items[:2]
					case "extra-item":
						r.Items = append(r.Items, oceanbase.BatchItem{})
					case "first-failed":
						r.Items[0].Status = oceanbase.BatchFailed
					case "first-error":
						r.Items[0].Err = context.Canceled
					case "second-succeeded":
						r.Items[1].Status = oceanbase.BatchSucceeded
					case "last-status":
						r.Items[2].Status = oceanbase.BatchUnknown
					case "last-error":
						r.Items[2].Err = context.Canceled
					case "joined-finalization":
						failure = errors.Join(batch, &oceanbase.BatchError{Index: -1, Completed: batch.Completed, Err: context.Canceled})
					case "joined-summary":
						batch.Err = errors.Join(server, context.Canceled)
					case "joined-item":
						r.Items[1].Err = errors.Join(itemError, context.Canceled)
					}
					pass := fault == duplicateValidBatch || fault == duplicateWrappedBatch
					if err := checkBatchDuplicate(r, failure, mode, keepGoing); (err == nil) != pass {
						t.Fatalf("batch duplicate assertion accepted=%t want %t", err == nil, pass)
					}
				})
			}
		}
	}
}
