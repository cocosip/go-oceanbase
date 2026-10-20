package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/cursor"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Attempt records protocol evidence for a sequential batch item before cleanup can fail.
type Attempt struct {
	Started, Acknowledged bool
	Result                command.Result
}

// Options selects output handling, resource ownership, and execution recording.
type Options struct {
	Outputs            bool
	TransactionSession bool
	Recorder           *Recorder
	Attempt            *Attempt
	Input              *streaminput.Infile
	AllowResources     bool
	CaptureLOB         bool
	RetainOperation    bool
}

// Rows owns the response and its operation until finalization has completed exactly once.
type Rows struct {
	engine                                   *Engine
	op                                       *session.Operation
	raw                                      *command.RawResponse
	ctx, iterationCtx                        context.Context
	binary                                   bool
	err                                      error
	released                                 bool
	holdRelease, retainOperation, captureLOB bool
	recorder                                 *Recorder
	attempt                                  *Attempt
	outputs                                  *call.State
	auto, parameter                          *statement.Statement
	stopCancel                               func() bool
	cancelDone                               chan struct{}
	stopTx                                   func()
	cancelExecution                          bool
	snapshot                                 *rowset.Rows
	columns                                  []wire.Column
	last                                     command.Result
	bufferBytes                              int64
	input                                    *streaminput.Infile
	mode                                     config.Mode
	sessionConfig                            config.SessionOptions
	transactionSession                       bool
}

func (e *Engine) newRows(ctx context.Context, op *session.Operation, binaryRows bool, options Options) *Rows {
	r := &Rows{engine: e, op: op, ctx: ctx, iterationCtx: ctx, binary: binaryRows, recorder: options.Recorder, attempt: options.Attempt, input: options.Input,
		captureLOB: options.CaptureLOB, retainOperation: options.RetainOperation, mode: op.Protocol().Mode, sessionConfig: op.Config().Session, transactionSession: options.TransactionSession}
	if tx := e.control.ActiveTransaction(); tx != nil && tx.Context().Done() != nil {
		txctx := tx.Context()
		merged, cancel := context.WithCancelCause(ctx)
		stop := context.AfterFunc(txctx, func() { cancel(context.Cause(txctx)) })
		if txctx.Err() != nil {
			cancel(context.Cause(txctx))
		}
		r.stopTx = func() { stop(); cancel(nil) }
		r.ctx = merged
	}
	if r.ctx.Done() != nil {
		r.cancelDone = make(chan struct{})
		r.stopCancel = context.AfterFunc(r.ctx, func() { e.Discard(); close(r.cancelDone) })
	}
	return r
}

func (r *Rows) begin() {
	r.recorder.Begin()
	if !r.retainOperation {
		r.engine.control.BeginSQL()
	}
	if r.attempt != nil {
		r.attempt.Started = true
	}
}

func (r *Rows) contextError() error {
	if err := context.Cause(r.ctx); err != nil {
		return err
	}
	if tx := r.engine.control.ActiveTransaction(); tx != nil {
		return context.Cause(tx.Context())
	}
	return nil
}

func (r *Rows) fail(err error, broken bool) error {
	if !r.released {
		if cause := r.contextError(); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}
	if r.err == nil {
		r.err = err
	}
	var server *dberrors.Error
	if !r.released && broken && !errors.As(err, &server) {
		r.engine.Discard()
	}
	r.finish(r.ctx)
	return r.err
}

func (r *Rows) finish(ctx context.Context) {
	if r.cancelExecution {
		r.engine.physical.EndCancellation(r.contextError() != nil)
		r.cancelExecution = false
	}
	if r.holdRelease {
		r.op.EndResponse()
		return
	}
	if r.released {
		return
	}
	if r.attempt != nil && r.err == nil && r.raw != nil && r.raw.Done() && !r.raw.More() {
		r.attempt.Acknowledged = true
		r.attempt.Result = r.raw.Last()
	}
	if cause := r.contextError(); cause != nil {
		r.engine.Discard()
		if r.err == nil {
			r.err = cause
		}
	}
	r.released = true
	r.op.EndResponse()
	if r.stopCancel != nil && !r.stopCancel() {
		<-r.cancelDone
	}
	if r.auto != nil && r.outputs != nil && !r.outputs.HasResources() {
		cleanup, cancel := r.cleanupContext()
		r.err = errors.Join(r.err, r.auto.Close(cleanup, r.op))
		cancel()
	}
	r.finalizeOutputs(ctx)
	if r.auto != nil && r.outputs.HasResources() {
		cleanup, cancel := r.cleanupContext()
		if err := r.auto.Close(cleanup, r.op); err != nil && r.err == nil {
			r.err = err
		}
		cancel()
	}
	if cause := r.contextError(); cause != nil && !errors.Is(r.err, cause) {
		r.err = errors.Join(r.err, cause)
		r.engine.Discard()
	}
	r.finishResources(ctx)
	if r.stopTx != nil {
		r.stopTx()
	}
	if r.auto != nil && r.outputs == nil {
		cleanup, cancel := r.cleanupContext()
		r.err = errors.Join(r.err, r.engine.statements.Recycle(cleanup, r.op, r.auto, r.err == nil))
		cancel()
	}
	r.recorder.Finish(r.err)
	if r.parameter != nil {
		r.parameter.AcknowledgeTypes(r.err)
	}
	if !r.retainOperation {
		r.engine.release(r.op)
	}
}

func (r *Rows) finishResources(ctx context.Context) {
	if !r.retainOperation {
		var retained []session.ResourceRef
		if r.outputs != nil {
			retained = r.outputs.Pending()
		}
		cleanup, cancel := r.cleanupContext()
		r.err = errors.Join(r.err, r.engine.cleanup(cleanup, r.op, false, retained))
		cancel()
	}
	if r.outputs != nil && r.err == nil {
		var txctx context.Context
		if tx := r.engine.control.ActiveTransaction(); tx != nil {
			txctx = tx.Context()
		}
		r.err = call.Assign(ctx, r.op, txctx, r.outputs.Bindings(), r.outputs.Values())
	}
	if r.err != nil && !r.retainOperation {
		cleanup, cancel := r.cleanupContext()
		r.err = errors.Join(r.err, r.engine.cleanup(cleanup, r.op, false, nil))
		cancel()
	}
}

func (r *Rows) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), r.op.Config().Limits.CleanupTimeout)
}

func (r *Rows) finalizeOutputs(ctx context.Context) {
	if r.outputs == nil {
		return
	}
	if err := r.outputs.CheckEpoch(r.op); err != nil {
		r.err = errors.Join(r.err, err)
		if r.outputs.HasDeferredComplex() {
			r.engine.Discard()
		}
	}
	if r.err == nil {
		r.err = r.outputs.DecodeComplex(ctx, r.op, &r.engine.catalog)
		if r.err != nil {
			r.engine.Discard()
		}
	}
	cleanup, cancel := r.cleanupContext()
	r.err = errors.Join(r.err, r.outputs.Finish(cleanup, r.op, r.err == nil))
	cancel()
	if !r.op.Closed() && (r.err == nil || r.outputs.HasResources() && r.outputs.Seen()) {
		if err := r.prepareResources(ctx); err != nil {
			r.err = errors.Join(r.err, err)
			r.engine.Discard()
		}
	}
}

func (r *Rows) prepareResources(ctx context.Context) error {
	resources, err := r.outputs.Resources()
	if err != nil {
		return err
	}
	for _, value := range resources {
		var ref session.ResourceRef
		if value.Value != nil {
			if r.transactionSession != (r.engine.control.ActiveTransaction() != nil) {
				return dberrors.ErrResource
			}
			parent := r.outputs.ParentStatement()
			if parent == nil {
				return dberrors.ErrResource
			}
			if value.Type == 163 {
				id, ok := value.Value.(materialize.RefCursorID)
				if !ok {
					return dberrors.ErrOut
				}
				ref, err = r.engine.cursors.OwnRef(r.op, parent, uint32(id), nil, cursor.Options{})
			} else {
				raw, ok := value.Value.([]byte)
				if !ok {
					return dberrors.ErrLOB
				}
				ref, err = r.engine.lobs.OwnNative(r.op, value.Column, raw, parent)
			}
			if err != nil {
				return err
			}
		}
		if err = r.outputs.Stage(value.Index, ref); err != nil {
			return err
		}
	}
	if r.err != nil {
		return nil
	}
	return r.outputs.MaterializeScalars(ctx, r.op, &r.engine.catalog)
}

func (r *Rows) advance(ctx context.Context) error {
	for {
		event, err := r.raw.Advance(ctx)
		if err != nil {
			var server *dberrors.Error
			if errors.As(err, &server) && r.raw.Items() < r.op.Config().Limits.MaxResultItems {
				r.recorder.Add(ResultInfo{Kind: ResultFailure, Complete: true, Err: err})
			}
			return err
		}
		r.last = r.raw.Last()
		switch event.Kind {
		case command.InfileRequest:
			if err = r.uploadInfile(ctx, event.Request); err != nil {
				return err
			}
			continue
		case command.CommandResult:
			if !r.retainOperation {
				r.engine.control.ObserveOK(event.OK)
			}
			if err = r.recorder.Command(event.OK); err != nil {
				return err
			}
			if !r.raw.More() {
				r.finish(ctx)
				return r.err
			}
			continue
		case command.RowResult:
			r.columns = event.Columns
			if r.outputs != nil && r.op.Protocol().Capabilities&wire.ClientDeprecateEOF == 0 && r.op.Protocol().OK.Status&4096 != 0 {
				if err = r.outputs.ReadHidden(ctx, r.op, r.raw); err != nil {
					return err
				}
				if !r.raw.More() {
					r.finish(ctx)
					return r.err
				}
				continue
			}
			return r.recorder.Rowset(r.columns, r.op.Config().Limits.MaxMessageBytes)
		}
	}
}

func (r *Rows) rowDone(ctx context.Context) {
	ok := r.op.Protocol().OK
	if !r.retainOperation {
		r.engine.control.ObserveOK(ok)
	}
	r.recorder.RowDone(ok.Warnings)
	if !r.raw.More() {
		r.finish(ctx)
	}
}

func (r *Rows) next(ctx context.Context, dest []driver.Value) error {
	fields, err := r.raw.NextFields(ctx)
	if err == io.EOF {
		r.rowDone(ctx)
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	if err != nil {
		return r.fail(err, true)
	}
	if dest != nil {
		values, err := materialize.Row(r.op, r.columns, r.binary, fields, r.captureLOB)
		if err != nil {
			return r.fail(err, true)
		}
		copy(dest, values)
	}
	return nil
}

func (r *Rows) bufferResources(ctx context.Context) error {
	if r.captureLOB || r.released || r.op.Protocol().Mode != config.ModeOracle {
		return nil
	}
	found := false
	for _, column := range r.columns {
		found = found || column.Type == 160 || column.Type == 163 || column.Type == 210 || column.Type == 211
	}
	if !found {
		return nil
	}
	r.holdRelease, r.captureLOB = true, true
	sets, err := r.collect(ctx)
	if err == nil && r.outputs != nil {
		err = r.outputs.CheckEpoch(r.op)
	}
	if err == nil {
		decoder := materialize.New(r.op, &r.engine.catalog)
		for i := range sets {
			if err = decoder.DecodeSet(ctx, &sets[i], &r.bufferBytes); err != nil {
				break
			}
		}
		if err == nil {
			for i := range sets {
				if err = decoder.RefCursors(ctx, r.iterationCtx, &sets[i], 0, &r.bufferBytes, make(map[materialize.RefCursorID]bool)); err != nil {
					break
				}
			}
		}
	}
	r.holdRelease = false
	if err != nil {
		return r.fail(err, true)
	}
	r.finish(ctx)
	if r.err != nil {
		return r.err
	}
	snapshots := make([]rowset.Set, len(sets))
	for i, set := range sets {
		snapshots[i] = rowset.Set{Columns: set.Columns, Rows: set.Rows}
	}
	r.snapshot = rowset.New(r.iterationCtx, r.mode, r.sessionConfig.ParseTime, snapshots)
	if len(sets) > 0 {
		r.columns = sets[0].Columns
	}
	return nil
}

func (r *Rows) collect(ctx context.Context) ([]materialize.Set, error) {
	decoder := materialize.New(r.op, &r.engine.catalog)
	var sets []materialize.Set
	for {
		if !r.raw.Done() || r.raw.Columns() != nil {
			set, err := decoder.CollectSet(ctx, r.raw, r.binary, &r.bufferBytes)
			if err != nil {
				return nil, err
			}
			r.rowDone(ctx)
			sets = append(sets, set)
		}
		if !r.raw.More() {
			return sets, nil
		}
		if err := r.advance(ctx); err != nil {
			return nil, err
		}
		if r.raw.Done() && !r.raw.More() {
			return sets, nil
		}
	}
}

// Columns returns the current result set's labels.
func (r *Rows) Columns() []string {
	if r.snapshot != nil {
		return r.snapshot.Columns()
	}
	names := make([]string, len(r.columns))
	for i, column := range r.columns {
		names[i] = column.Name
	}
	return names
}

// Next decodes the next row or returns io.EOF at the current result boundary.
func (r *Rows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.snapshot != nil {
		return r.snapshot.Next(dest)
	}
	if r.raw.Done() {
		return io.EOF
	}
	if len(dest) != len(r.columns) {
		return r.fail(wire.ErrMalformed, true)
	}
	return r.next(r.ctx, dest)
}
func (r *Rows) drainCurrent(ctx context.Context) error {
	for !r.raw.Done() {
		if err := r.next(ctx, nil); err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}

// Drain consumes all remaining result sets and finalizes the response.
func (r *Rows) Drain(ctx context.Context) error {
	if r.err != nil || r.released {
		return r.err
	}
	for {
		if err := r.bufferResources(ctx); err != nil {
			return err
		}
		if r.released {
			if r.snapshot != nil {
				_ = r.snapshot.Close()
				r.snapshot = nil
			}
			return r.err
		}
		if err := r.drainCurrent(ctx); err != nil {
			return err
		}
		if !r.raw.More() {
			r.finish(ctx)
			return r.err
		}
		if err := r.advance(ctx); err != nil {
			return r.fail(err, true)
		}
		if r.released {
			return r.err
		}
	}
}

// Close discards buffered rows or drains the active response.
func (r *Rows) Close() error {
	if r.snapshot != nil {
		_ = r.snapshot.Close()
		r.snapshot = nil
		return r.err
	}
	if r.released {
		return r.err
	}
	if err := r.contextError(); err != nil {
		return r.fail(err, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.op.Config().Limits.CleanupTimeout)
	defer cancel()
	return r.Drain(ctx)
}

// HasNextResultSet reports whether another result may follow.
func (r *Rows) HasNextResultSet() bool {
	if r.snapshot != nil {
		return r.snapshot.HasNextResultSet()
	}
	return !r.released && (r.raw.More() || !r.raw.Done())
}

// NextResultSet drains the current set and advances to the next row set.
func (r *Rows) NextResultSet() error {
	if r.err != nil {
		return r.err
	}
	if r.snapshot != nil {
		return r.snapshot.NextResultSet()
	}
	if r.released {
		return io.EOF
	}
	if err := r.drainCurrent(r.ctx); err != nil {
		return err
	}
	if !r.raw.More() {
		return io.EOF
	}
	if err := r.advance(r.ctx); err != nil {
		return r.fail(err, true)
	}
	if r.released {
		return io.EOF
	}
	return r.bufferResources(r.ctx)
}

// Last returns the most recent command result.
func (r *Rows) Last() command.Result { return r.last }

// ColumnTypeDatabaseTypeName returns the current column's database type name.
func (r *Rows) ColumnTypeDatabaseTypeName(i int) string {
	if r.snapshot != nil {
		return r.snapshot.ColumnTypeDatabaseTypeName(i)
	}
	return valuecodec.TextTypeName(r.columns[i], r.mode)
}

// ColumnTypeNullable reports the current column's nullability.
func (r *Rows) ColumnTypeNullable(i int) (bool, bool) {
	if r.snapshot != nil {
		return r.snapshot.ColumnTypeNullable(i)
	}
	return r.columns[i].Flags&1 == 0, true
}

// ColumnTypeLength returns the current column's declared length.
func (r *Rows) ColumnTypeLength(i int) (int64, bool) {
	if r.snapshot != nil {
		return r.snapshot.ColumnTypeLength(i)
	}
	return valuecodec.ColumnLength(r.columns[i], r.mode)
}

// ColumnTypePrecisionScale returns the current column's numeric precision and scale.
func (r *Rows) ColumnTypePrecisionScale(i int) (int64, int64, bool) {
	if r.snapshot != nil {
		return r.snapshot.ColumnTypePrecisionScale(i)
	}
	return valuecodec.ColumnPrecisionScale(r.columns[i], r.mode)
}

// ColumnTypeScanType returns the current column's preferred Go scan type.
func (r *Rows) ColumnTypeScanType(i int) reflect.Type {
	if r.snapshot != nil {
		return r.snapshot.ColumnTypeScanType(i)
	}
	return valuecodec.ColumnScanType(r.columns[i], r.mode, r.sessionConfig)
}

var _ driver.RowsNextResultSet = (*Rows)(nil)
