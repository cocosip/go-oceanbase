package batch

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
)

func TestSequentialObservation(t *testing.T) {
	for _, keepGoing := range []bool{false, true} {
		result := Result{}
		plan, err := NewPlan([][]any{{1}, {2}, {3}}, Options{ContinueOnError: keepGoing}, &result)
		if err != nil {
			t.Fatal(err)
		}
		for {
			index, _, ok := plan.Next(context.Background())
			if !ok {
				break
			}
			observation := Observation{Started: true, Result: command.Result{Known: true, Affected: 1, ID: math.MaxUint64}}
			if index == 1 {
				observation.Err = &dberrors.Error{Code: 1062}
			}
			plan.Observe(index, observation)
		}
		var failure *dberrors.BatchError
		if !errors.As(plan.Err(), &failure) || failure.Index != 1 || result.Items[0].InsertID != maxInsertID || result.Items[1].Status != Failed {
			t.Fatal(result, plan.Err())
		}
		if keepGoing && (failure.Completed != 2 || result.Items[2].Status != Succeeded) {
			t.Fatal(result, failure)
		}
		if !keepGoing && (failure.Completed != 1 || result.Items[2].Status != NotExecuted) {
			t.Fatal(result, failure)
		}
	}
}

func TestSequentialUncertaintyAndAcknowledgement(t *testing.T) {
	for _, observation := range []Observation{
		{Started: true, Err: io.ErrUnexpectedEOF},
		{Native: true, Started: true, Broken: true, Err: io.ErrUnexpectedEOF},
		{Native: true, Err: dberrors.ErrBind},
		{Native: true, Started: true, Acknowledged: true, Result: command.Result{Known: true, Affected: 7}, Err: io.ErrUnexpectedEOF},
	} {
		var result Result
		plan, err := NewPlan([][]any{{1}, {2}}, Options{ContinueOnError: true}, &result)
		if err != nil {
			t.Fatal(err)
		}
		i, _, ok := plan.Next(context.Background())
		if !ok {
			t.Fatal("first row missing")
		}
		plan.Observe(i, observation)
		_, _, next := plan.Next(context.Background())
		if observation.Acknowledged {
			if result.Items[0].Status != Succeeded || result.Items[0].RowsAffected != 7 || !next {
				t.Fatal(result, next)
			}
		} else if !observation.Started {
			if result.Items[0].Status != Failed || !next {
				t.Fatal(result, next)
			}
		} else if result.Items[0].Status != Unknown || next {
			t.Fatal(result, next)
		}
	}
	var result Result
	plan, err := NewPlan([][]any{{1}, {2}}, Options{ContinueOnError: true}, &result)
	if err != nil {
		t.Fatal(err)
	}
	i, _, _ := plan.Next(context.Background())
	plan.Observe(i, Observation{Native: true, Err: driver.ErrBadConn})
	if _, _, ok := plan.Next(context.Background()); ok {
		t.Fatal("continued after ErrBadConn")
	}
}

func TestSequentialLimitsAndCancellation(t *testing.T) {
	var result Result
	plan, err := NewPlan([][]any{{1, 2}, {3}}, Options{ContinueOnError: true, ChunkParameters: 1}, &result)
	if err != nil {
		t.Fatal(err)
	}
	i, args, ok := plan.Next(context.Background())
	if !ok || i != 1 || len(args) != 1 || result.Items[0].Status != Failed {
		t.Fatal(result, i, args, ok)
	}
	plan.Observe(i, Observation{})
	if _, _, ok = plan.Next(context.Background()); ok {
		t.Fatal("extra row")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan, err = NewPlan([][]any{{1}, {2}}, Options{}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok = plan.Next(ctx); ok || !errors.Is(plan.Err(), context.Canceled) || result.Items[0].Started || result.Items[0].Status != NotExecuted {
		t.Fatal(result, plan.Err())
	}
}
