package batch

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/lifetime"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// Request retains matching original rows for qualification and deferred rows for execution.
// Neither slice is copied or resolved while choosing a strategy.
type Request struct {
	Query         string
	RawRows, Rows [][]any
	Options       Options
	Result        *Result
	Outcome       *NativeOutcome
}

// NativeOutcome records state the execution coordinator commits after a native strategy.
// PrepareErr is delivered once by the sequential fallback after pipeline preparation fails.
type NativeOutcome struct {
	Started, WarningsChanged bool
	WarningCount             uint16
	WarningsKnown            bool
	PrepareErr               error
}

type native struct {
	op       *session.Operation
	resolver *catalog.Resolver
	cache    *statement.Cache
	owners   *lifetime.Owners
	outcome  *NativeOutcome
}

// TryNative chooses native strategies in the original order under the held operation.
// A false result leaves ordered ordinary execution to the caller; it never calls execution.
func TryNative(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, cache *statement.Cache, owners *lifetime.Owners, request Request, writable bool) (bool, error) {
	if len(request.Rows) == 0 {
		return false, nil
	}
	if request.Outcome == nil {
		request.Outcome = &NativeOutcome{}
	}
	n := native{op: op, resolver: resolver, cache: cache, owners: owners, outcome: request.Outcome}
	if handled, err := n.tryBatchMultiValues(ctx, request); handled {
		return true, err
	}
	if handled, err := n.tryBatchArray(ctx, request, writable); handled {
		return true, err
	}
	if handled, err := n.tryBatchRewrite(ctx, request); handled {
		return true, err
	}
	if op.Config().Network == "sharedmemory" || !batchMultiCandidate(request) || op.Config().PrepareMode != config.PrepareServer {
		return false, nil
	}
	if op.Closed() {
		return true, driver.ErrBadConn
	}
	p := op.Protocol()
	if op.Packets().Framed() || !sqltext.BatchDML(request.Query, p.Mode, p.OK.Status&512 != 0) || !n.batchParametersValid(request) {
		return false, nil
	}
	prepared, err := statement.Prepare(ctx, op, request.Query)
	if err != nil {
		request.Outcome.PrepareErr = err
		return false, nil
	}
	return true, n.execBatchMultiLocked(ctx, request, prepared)
}

func batchScalarRows(rows [][]any) bool {
	for _, row := range rows {
		for _, value := range row {
			if named, ok := value.(sql.NamedArg); ok {
				value = named.Value
			}
			switch value.(type) {
			case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, string, []byte, time.Time,
				types.Number, types.Raw, types.NString, types.RowID, types.TimestampTZ, types.TimestampLTZ, types.IntervalYM, types.IntervalDS:
			default:
				return false
			}
		}
	}
	return true
}

func batchMultiCandidate(request Request) bool {
	return request.Options.ContinueOnError && len(request.RawRows) >= 2 && (request.Options.Strategy == Auto || request.Options.Strategy == MultiSend) && batchScalarRows(request.RawRows)
}

// Qualification uses only raw shapes and names; conversion belongs to the current row.
func (n *native) batchParametersValid(request Request) bool {
	p := n.op.Protocol()
	var plan sqltext.Plan
	if p.Mode == config.ModeOracle {
		var err error
		plan, err = sqltext.BuildBindPlan(request.Query, p.Mode)
		if err != nil || len(plan.Slots) > request.Options.ChunkParameters {
			return false
		}
	}
	for _, row := range request.RawRows {
		if len(row) > request.Options.ChunkParameters || len(row) > 65535 {
			return false
		}
		args, err := parameters.NamedArguments(row)
		if err != nil {
			return false
		}
		if p.Mode == config.ModeOracle {
			if _, err = parameters.Bind(plan, args); err != nil {
				return false
			}
		} else {
			for _, arg := range args {
				if arg.Name != "" {
					return false
				}
			}
		}
	}
	return true
}

type scalarArgument struct{ value any }

func (a scalarArgument) Resolve() (any, error) {
	value := a.value
	for {
		next, again, err := parameters.NormalizeStep(value)
		if err != nil || !again {
			return next, err
		}
		value = next
	}
}

func rowArguments(row []any) ([]driver.NamedValue, error) {
	args, err := parameters.NamedArguments(row)
	if err != nil {
		return nil, err
	}
	for i := range args {
		if _, ok := args[i].Value.(parameters.ArgumentSource); !ok {
			args[i].Value = scalarArgument{args[i].Value}
		}
	}
	return args, nil
}

func (n *native) encodeParameters(ctx context.Context, prepared *statement.Statement, row []any) ([]wire.Parameter, error) {
	args, err := rowArguments(row)
	if err != nil {
		return nil, err
	}
	values, err := parameters.Bind(prepared.Plan(), args)
	if err != nil {
		return nil, err
	}
	budget := n.op.Config().Limits.MaxMessageBytes - 10 - int64((len(values)+7)/8+1+2*len(values))
	return parameters.Encode(ctx, n.op, n.resolver, values, budget)
}

func (n *native) encodeSQL(query string) ([]byte, error) {
	cfg := n.op.Config()
	return command.EncodeSQL(query, cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1)
}

func (n *native) clearStatements(ctx context.Context) error {
	if n.cache == nil {
		return nil
	}
	return n.cache.Clear(ctx, n.op)
}

func (n *native) cleanup(prepared *statement.Statement, err error, result *Result) error {
	ctx, cancel := context.WithTimeout(context.Background(), n.op.Config().Limits.CleanupTimeout)
	defer cancel()
	closeErr := prepared.Close(ctx, n.op)
	if n.owners != nil {
		closeErr = errors.Join(closeErr, n.owners.Cleanup(ctx, n.op, false, nil))
	}
	if closeErr != nil {
		return errors.Join(err, &dberrors.BatchError{Index: -1, Completed: SucceededCount(result), Err: closeErr})
	}
	return err
}

type responseBoundary struct {
	op     *session.Operation
	ctx    context.Context
	cancel bool
	stop   func() bool
	done   chan struct{}
}

func (n *native) beginResponse(ctx context.Context, cancel bool) (*responseBoundary, error) {
	if err := n.op.BeginResponse(); err != nil {
		return nil, err
	}
	b := &responseBoundary{op: n.op, ctx: ctx}
	if cancel {
		b.cancel = n.op.BeginCancellation()
	}
	if ctx.Done() != nil {
		b.done = make(chan struct{})
		b.stop = context.AfterFunc(ctx, func() { n.op.Discard(); close(b.done) })
	}
	return b, nil
}

func (b *responseBoundary) finish(err error) error {
	if b.cancel {
		b.op.EndCancellation(b.ctx.Err() != nil)
	}
	if cause := b.ctx.Err(); cause != nil {
		b.op.Discard()
		if err == nil {
			err = cause
		} else if !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}
	b.op.EndResponse()
	if b.stop != nil && !b.stop() {
		<-b.done
	}
	return err
}

func (n *native) clearWarnings() {
	n.outcome.WarningsChanged, n.outcome.WarningCount, n.outcome.WarningsKnown = true, 0, false
}
func (n *native) recordWarnings(ok wire.OK) {
	n.outcome.WarningsChanged, n.outcome.WarningCount, n.outcome.WarningsKnown = true, ok.Warnings, true
}

func sameParameterTypes(a, b []wire.Parameter) bool {
	if len(a) != len(b) {
		return false
	}
	for i, p := range a {
		if p.Type != b[i].Type || !bytes.Equal(p.TypeMetadata, b[i].TypeMetadata) {
			return false
		}
	}
	return true
}

func (n *native) batchRewriteUnknown(items []Item, err error) error {
	n.op.Discard()
	for i := range items {
		items[i] = Item{Status: Unknown, Started: true, Err: err}
	}
	return err
}

func batchLimitError() error { return fmt.Errorf("%w: batch parameter limit", dberrors.ErrBind) }
