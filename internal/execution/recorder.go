package execution

import (
	"math"
	"slices"
	"strconv"
	"sync"

	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ResultKind identifies one observed result in its original execution order.
type ResultKind uint8

// Result kinds preserve command results independently from rowsets.
const (
	ResultRows ResultKind = iota
	ResultCommand
	ResultFailure
)

// ResultColumn retains the server label and origin without inferring missing fields.
type ResultColumn struct {
	Catalog, Schema, Table, OriginalTable, Name, OriginalName string
}

// WarningSummary records the count from a consumed server response boundary.
type WarningSummary struct {
	Count uint16
	Known bool
}

// ResultInfo describes one observed response and whether its boundary was consumed.
type ResultInfo struct {
	Kind              ResultKind
	RowSetIndex       int
	RowsAffected      int64
	RowsAffectedKnown bool
	InsertID          string
	InsertIDKnown     bool
	Complete          bool
	Err               error
	Warnings          WarningSummary
	Columns           []ResultColumn
}

// Snapshot is an independent view of recorded execution progress.
type Snapshot struct {
	Results []ResultInfo
	Done    bool
	Err     error
}

// Recorder synchronizes result accounting independently of public Rows ownership.
type Recorder struct {
	mu            sync.RWMutex
	snapshot      Snapshot
	started       bool
	rowsets       int
	metadataBytes int64
}

// Begin records that the execution may have sent a request.
func (r *Recorder) Begin() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
}

// Started reports whether Begin has observed a sent execution.
func (r *Recorder) Started() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.started
}

// Add appends a non-rowset result without consuming a rowset index.
func (r *Recorder) Add(info ResultInfo) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	info.RowSetIndex = -1
	r.snapshot.Results = append(r.snapshot.Results, info)
}

// Rowset records complete metadata within the cumulative execution snapshot budget.
func (r *Recorder) Rowset(columns []wire.Column, limit int64) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	size := rowset.MetadataSize(columns)
	if size > limit-r.metadataBytes {
		return wire.ErrMessageTooLarge
	}
	info := ResultInfo{Kind: ResultRows, RowSetIndex: r.rowsets, Columns: make([]ResultColumn, len(columns))}
	for i, column := range columns {
		info.Columns[i] = ResultColumn{
			Catalog: column.Catalog, Schema: column.Schema,
			Table: column.Table, OriginalTable: column.OriginalTable,
			Name: column.Name, OriginalName: column.OriginalName,
		}
	}
	r.metadataBytes += size
	r.rowsets++
	r.snapshot.Results = append(r.snapshot.Results, info)
	return nil
}

// Command records a consumed OK, preserving unsigned insert IDs and count overflow.
func (r *Recorder) Command(ok wire.OK) error {
	if r == nil {
		return nil
	}
	info := ResultInfo{Kind: ResultCommand, RowsAffectedKnown: true, Complete: true, Warnings: WarningSummary{Count: ok.Warnings, Known: true}}
	if ok.InsertID != 0 {
		info.InsertID = strconv.FormatUint(ok.InsertID, 10)
		info.InsertIDKnown = true
	}
	if ok.AffectedRows > math.MaxInt64 {
		info.RowsAffectedKnown = false
		info.Err = command.ErrCountOverflow
	} else {
		info.RowsAffected = int64(ok.AffectedRows)
	}
	r.Add(info)
	return info.Err
}

// RowDone marks the most recent rowset complete at its consumed response boundary.
func (r *Recorder) RowDone(warnings uint16) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := len(r.snapshot.Results); n > 0 && r.snapshot.Results[n-1].Kind == ResultRows {
		r.snapshot.Results[n-1].Complete = true
		r.snapshot.Results[n-1].Warnings = WarningSummary{Count: warnings, Known: true}
	}
}

// Finish preserves the first execution and incomplete-result errors across repeated closes.
func (r *Recorder) Finish(err error) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil && r.snapshot.Err == nil {
		r.snapshot.Err = err
	}
	if n := len(r.snapshot.Results); err != nil && n > 0 && !r.snapshot.Results[n-1].Complete && r.snapshot.Results[n-1].Err == nil {
		r.snapshot.Results[n-1].Err = err
	}
	r.snapshot.Done = true
}

// Snapshot copies both result and column slices without reading from the network.
func (r *Recorder) Snapshot() Snapshot {
	if r == nil {
		return Snapshot{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	snapshot := r.snapshot
	snapshot.Results = slices.Clone(snapshot.Results)
	for i := range snapshot.Results {
		snapshot.Results[i].Columns = slices.Clone(snapshot.Results[i].Columns)
	}
	return snapshot
}
