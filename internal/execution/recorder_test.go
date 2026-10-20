package execution

import (
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func TestRecorderOrderAndIndependentSnapshots(t *testing.T) {
	var r Recorder
	if r.Started() {
		t.Fatal("zero recorder started")
	}
	r.Begin()
	if !r.Started() {
		t.Fatal("begin did not record send")
	}
	if err := r.Command(wire.OK{Status: 2, Warnings: 3}); err != nil {
		t.Fatal(err)
	}
	const label = "label"
	columns := []wire.Column{{Catalog: "def", Schema: "s", Table: "alias", OriginalTable: "t", Name: label, OriginalName: "id"}}
	if err := r.Rowset(columns, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	before := r.Snapshot()
	if len(before.Results) != 2 || before.Results[0].RowSetIndex != -1 || before.Results[1].RowSetIndex != 0 || before.Results[1].Complete {
		t.Fatal(before)
	}
	if got := before.Results[1].Columns[0]; got != (ResultColumn{Catalog: "def", Schema: "s", Table: "alias", OriginalTable: "t", Name: label, OriginalName: "id"}) {
		t.Fatal(got)
	}
	before.Results[0].RowsAffected = 99
	before.Results[1].Columns[0].Name = "changed"
	r.RowDone(8)
	if err := r.Command(wire.OK{AffectedRows: 2, InsertID: math.MaxUint64}); err != nil {
		t.Fatal(err)
	}
	r.Finish(nil)
	got := r.Snapshot()
	if !got.Done || got.Err != nil || len(got.Results) != 3 {
		t.Fatal(got)
	}
	first, rows, last := got.Results[0], got.Results[1], got.Results[2]
	if first.Kind != ResultCommand || first.RowsAffected != 0 || !first.RowsAffectedKnown || first.InsertIDKnown || first.Warnings != (WarningSummary{Count: 3, Known: true}) {
		t.Fatal(first)
	}
	if rows.Kind != ResultRows || !rows.Complete || rows.Columns[0].Name != label || rows.Warnings != (WarningSummary{Count: 8, Known: true}) {
		t.Fatal(rows)
	}
	if last.RowSetIndex != -1 || last.RowsAffected != 2 || last.InsertID != "18446744073709551615" || !last.InsertIDKnown {
		t.Fatal(last)
	}
}

func TestRecorderMetadataBudget(t *testing.T) {
	var r Recorder
	columns := []wire.Column{{Name: "v", Extra: []byte("extra"), ComplexSchema: "s", ComplexName: "object", ElementSchema: "e", ElementName: "item"}}
	size := rowset.MetadataSize(columns)
	if err := r.Rowset(columns, size-1); !errors.Is(err, wire.ErrMessageTooLarge) || len(r.Snapshot().Results) != 0 {
		t.Fatal("rejected metadata changed recorder", err, r.Snapshot())
	}
	if err := r.Rowset(columns, size); err != nil {
		t.Fatal(err)
	}
	r.RowDone(0)
	if err := r.Rowset(columns, 2*size-1); !errors.Is(err, wire.ErrMessageTooLarge) || len(r.Snapshot().Results) != 1 {
		t.Fatal("cumulative limit not enforced", err, r.Snapshot())
	}
	if err := r.Rowset(columns, 2*size); err != nil || r.Snapshot().Results[1].RowSetIndex != 1 {
		t.Fatal("budget failure consumed rowset index", err, r.Snapshot())
	}
}

func TestRecorderOverflowAndFailure(t *testing.T) {
	var r Recorder
	if err := r.Command(wire.OK{AffectedRows: math.MaxUint64, InsertID: 4, Warnings: 7}); !errors.Is(err, command.ErrCountOverflow) {
		t.Fatal(err)
	}
	first := r.Snapshot().Results[0]
	if first.RowsAffectedKnown || !first.Complete || first.InsertID != "4" || first.Warnings.Count != 7 || !errors.Is(first.Err, command.ErrCountOverflow) {
		t.Fatal(first)
	}
	r.RowDone(99)
	if r.Snapshot().Results[0].Warnings.Count != 7 {
		t.Fatal("row completion rewrote command")
	}
	if err := r.Rowset(nil, 32); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("read failure")
	r.Finish(failure)
	r.Finish(errors.New("later close failure"))
	got := r.Snapshot()
	if !got.Done || !errors.Is(got.Err, failure) || !errors.Is(got.Results[1].Err, failure) || got.Results[1].Complete {
		t.Fatal(got)
	}
	var complete Recorder
	complete.Add(ResultInfo{Kind: ResultFailure, Complete: true})
	complete.Finish(failure)
	if got := complete.Snapshot(); got.Results[0].Err != nil || got.Results[0].RowSetIndex != -1 {
		t.Fatal("finish rewrote complete result", got)
	}
}

func TestRecorderConcurrentSnapshot(t *testing.T) {
	var r Recorder
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for range 100 {
				_ = r.Started()
				for _, result := range r.Snapshot().Results {
					if len(result.Columns) > 0 {
						result.Columns[0].Name = "private snapshot"
					}
				}
			}
		})
	}
	r.Begin()
	for range 100 {
		if err := r.Rowset([]wire.Column{{Name: "v"}}, math.MaxInt64); err != nil {
			t.Fatal(err)
		}
		r.RowDone(1)
	}
	r.Finish(nil)
	readers.Wait()
	for _, result := range r.Snapshot().Results {
		if result.Columns[0].Name != "v" || !result.Complete {
			t.Fatal(result)
		}
	}
}

func TestNilRecorder(t *testing.T) {
	var r *Recorder
	r.Begin()
	r.Add(ResultInfo{})
	if err := r.Rowset(nil, 0); err != nil {
		t.Fatal(err)
	}
	r.RowDone(0)
	if err := r.Command(wire.OK{}); err != nil {
		t.Fatal(err)
	}
	r.Finish(errors.New("ignored"))
	if r.Started() || r.Snapshot().Done || r.Snapshot().Err != nil {
		t.Fatal("nil recorder retained state")
	}
}
