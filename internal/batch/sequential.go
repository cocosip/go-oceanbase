package batch

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
)

// Observation describes one attempted ordinary execution without invoking its owner.
// Result must contain the acknowledged result when Acknowledged is true.
type Observation struct {
	Native, Started, Broken, Acknowledged bool
	Result                                driver.Result
	Err                                   error
}

// Plan yields raw references for the next sequential row at its conversion point.
type Plan struct {
	rows    [][]any
	options Options
	result  *Result
	next    int
	stopped bool
	ctx     context.Context
}

// NormalizeOptions validates batch bounds and supplies the original defaults.
func NormalizeOptions(options Options) (Options, error) {
	if options.Strategy > MultiValues || options.ChunkRows < 0 || options.ChunkParameters < 0 {
		return options, &config.ConfigError{Field: "BatchOptions"}
	}
	if options.ChunkRows == 0 {
		options.ChunkRows = 128
	}
	if options.ChunkParameters == 0 {
		options.ChunkParameters = 65535
	}
	return options, nil
}

// NewPlan retains row references without resolving, converting or reading any value.
func NewPlan(rows [][]any, options Options, result *Result) (*Plan, error) {
	*result = Result{Items: make([]Item, len(rows)), Strategy: Sequential}
	options, err := NormalizeOptions(options)
	if err != nil {
		return nil, err
	}
	return &Plan{rows: rows, options: options, result: result}, nil
}

// Next validates only the current row's bounds and yields its original references.
func (p *Plan) Next(ctx context.Context) (int, []any, bool) {
	for !p.stopped && p.next < len(p.rows) {
		i := p.next
		p.next++
		item := &p.result.Items[i]
		if err := ctx.Err(); err != nil {
			item.Err, p.stopped = err, true
			return 0, nil, false
		}
		args := p.rows[i]
		if len(args) > p.options.ChunkParameters || len(args) > 65535 {
			item.Status, item.Err = Failed, fmt.Errorf("%w: batch parameter limit", dberrors.ErrBind)
			if !p.options.ContinueOnError {
				p.stopped = true
			}
			continue
		}
		p.ctx = ctx
		return i, args, true
	}
	return 0, nil, false
}

// Observe records ACK certainty and decides whether the next row may execute.
func (p *Plan) Observe(index int, observation Observation) {
	item := &p.result.Items[index]
	item.Started, item.Err = observation.Started, observation.Err
	if observation.Err == nil || observation.Acknowledged {
		item.Status = Succeeded
		if observation.Result != nil {
			item.RecordResult(observation.Result)
		}
		if observation.Err == nil {
			return
		}
	}
	var server *dberrors.Error
	definite := observation.Acknowledged || observation.Native && (!observation.Broken || !observation.Started) || errors.As(observation.Err, &server)
	if !observation.Acknowledged {
		item.Status = Failed
		if !definite {
			item.Status = Unknown
		}
	}
	if !p.options.ContinueOnError || !definite || observation.Broken || p.ctx.Err() != nil || errors.Is(observation.Err, driver.ErrBadConn) {
		p.stopped = true
	}
}

// Err reports every recorded error with the first failing input and confirmed successes.
func (p *Plan) Err() error { return Failure(p.result) }

// SucceededCount counts confirmed successes, including ACKs followed by cleanup errors.
func SucceededCount(result *Result) int {
	n := 0
	for _, item := range result.Items {
		if item.Status == Succeeded {
			n++
		}
	}
	return n
}

// Failure preserves ordered causes without inferring effects for unacknowledged inputs.
func Failure(result *Result) error {
	first, completed := -1, 0
	var causes []error
	for i, item := range result.Items {
		if item.Status == Succeeded {
			completed++
		}
		if item.Err != nil {
			if first < 0 {
				first = i
			}
			causes = append(causes, item.Err)
		}
	}
	if first < 0 {
		return nil
	}
	return &dberrors.BatchError{Index: first, Completed: completed, Err: errors.Join(causes...)}
}
