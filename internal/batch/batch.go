// Package batch owns batch strategies, ordered plans and acknowledged outcomes.
package batch

import (
	"database/sql/driver"
	"github.com/cocosip/go-oceanbase/internal/command"
	"strconv"
)

// Strategy is a preferred strategy; unsafe or unavailable optimizations fall back to sequential.
type Strategy uint8

// Batch execution strategies. Result reports the strategy actually used.
const (
	Auto Strategy = iota
	Sequential
	MultiSend
	Rewrite
	Bulk
	ArrayBinding
	MultiValues
)

// Options controls error handling and bounds each encoded chunk. Zero limits use defaults.
type Options struct {
	ContinueOnError bool
	Strategy        Strategy
	ChunkRows       int
	ChunkParameters int
}

// Status distinguishes unexecuted, acknowledged and uncertain operations.
type Status uint8

// Batch item outcomes never imply that the caller's transaction has committed.
// Failed records an error, not a guarantee of zero database effects.
const (
	NotExecuted Status = iota
	Succeeded
	Failed
	Unknown
)

// Item describes one input row; Started includes a possibly partial business-command write.
// A rewritten command can be sent but leave later items NotExecuted after a server error.
type Item struct {
	Status            Status
	Started           bool
	RowsAffected      int64
	RowsAffectedKnown bool
	Err               error
	InsertID          string
	InsertIDKnown     bool
}

// Result keeps input order, including items left unsent after a failure.
type Result struct {
	Items    []Item
	Strategy Strategy
	Chunks   []Chunk
}

// Chunk reports the server's aggregate result for a multi-VALUES command.
// Start is inclusive and End is exclusive; no per-row counts or IDs are inferred.
type Chunk struct {
	Start, End int
	Item
}

// RecordResult preserves confirmed counts and the full native unsigned insert identity.
func (item *Item) RecordResult(res driver.Result) {
	if n, err := res.RowsAffected(); err == nil && n >= 0 {
		item.RowsAffected, item.RowsAffectedKnown = n, true
	}
	if native, ok := res.(command.Result); ok {
		if native.Known && native.ID != 0 {
			item.InsertID, item.InsertIDKnown = strconv.FormatUint(native.ID, 10), true
		}
	} else if id, err := res.LastInsertId(); err == nil && id > 0 {
		item.InsertID, item.InsertIDKnown = strconv.FormatInt(id, 10), true
	}
}
