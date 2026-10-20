package cursor

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ValidateOpen checks cursor query and transaction requirements before preparation.
func ValidateOpen(op *session.Operation, query string, opts Options, writable bool) error {
	if opts.Mode > Sensitive || opts.MaxCacheBytes < 0 {
		return dberrors.ErrCursor
	}
	p := op.Protocol()
	if opts.Mode == Sensitive && (p.Mode != config.ModeOracle || !statement.SupportsPrepareExecute(p.OBVersion)) {
		return dberrors.ErrUnsupported
	}
	if opts.Updatable {
		if !writable || p.OK.Status&8192 != 0 {
			return dberrors.ErrUnsupported
		}
		if err := sqltext.CursorUpdatableQuery(query, p.Mode, p.OK.Status&512 != 0); err != nil {
			return err
		}
	}
	tokens, err := sqltext.LexSQL(query, p.Mode, p.OK.Status&512 != 0, false)
	if err != nil || !sqltext.CursorSingleSelect(query, tokens) {
		return errors.Join(dberrors.ErrCursor, err)
	}
	return nil
}

// Open prepares and owns an explicit cursor. Arguments have already crossed the public conversion boundary.
// The returned token remains unpublished until the lifetime coordinator commits the operation.
func (m *Manager) Open(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, query string, args []driver.NamedValue, opts Options, writable bool) (ref session.ResourceRef, err error) {
	if err = ValidateOpen(op, query, opts, writable); err != nil {
		return ref, err
	}
	epoch := op.Epoch()
	if opts.Mode == Sensitive {
		query, err = sqltext.SensitiveCursorQuery(query)
		if err != nil {
			return ref, err
		}
	}
	prepared, err := statement.Prepare(ctx, op, query)
	if err != nil {
		return ref, err
	}
	registered := false
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
		defer cancel()
		if registered {
			lease, e := m.Close(cleanup, op, ref)
			err = errors.Join(err, e, lease.Release(cleanup, op))
			op.Forget(ref)
		} else {
			err = errors.Join(err, prepared.Close(cleanup, op))
		}
	}()
	if op.Epoch() != epoch {
		return ref, dberrors.ErrResource
	}
	if resolver == nil {
		resolver = &catalog.Resolver{}
	}
	resolver.Clear()
	decoder := materialize.New(op, resolver)
	columns, last, err := executeOpen(ctx, op, resolver, decoder, prepared, args, opts)
	if err != nil {
		return ref, err
	}
	if op.Epoch() != epoch {
		return ref, dberrors.ErrResource
	}
	p := op.Protocol()
	cur := &record{columns: columns, last: last, options: opts, offset: p.Mode == config.ModeOracle && statement.SupportsVersion(p.OBVersion, 2, 2, 5), statement: prepared, scope: op.StatementScope()}
	ref, err = m.own(op, cur)
	if err != nil {
		return ref, err
	}
	registered = true
	if err = prepared.Close(ctx, op); err != nil {
		return ref, err
	}
	o := &operation{op: op, resolver: resolver, decoder: decoder, ref: ref, cur: cur}
	if opts.Updatable {
		if err = o.loadIdentity(ctx); err != nil {
			return ref, err
		}
	}
	if opts.Mode == Static {
		if err = o.cache(ctx); err != nil {
			return ref, err
		}
	}
	return ref, ctx.Err()
}

func executeOpen(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, decoder *materialize.Decoder, prepared *statement.Statement, args []driver.NamedValue, opts Options) ([]wire.Column, bool, error) {
	plan := prepared.Plan()
	values, err := parameters.Bind(plan, args)
	if err != nil {
		return nil, false, err
	}
	query, err := command.EncodeSQL(plan.SQL, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, false, err
	}
	combined := opts.Mode == Sensitive
	budget := op.Config().Limits.MaxMessageBytes - 10
	if combined {
		budget = op.Config().Limits.MaxMessageBytes - 30 - (wire.Parameter{Data: query, LengthEncoded: true}).EncodedSize()
	}
	if len(values) > 0 || !combined {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	params, err := parameters.Encode(ctx, op, resolver, values, budget)
	if err != nil {
		return nil, false, err
	}
	var payload []byte
	if combined {
		payload, err = wire.EncodePrepareExecuteOptions(prepared.ID(), query, params, op.Config().Limits.MaxMessageBytes, prepared.ParameterOptions(op, query, params, true))
	} else {
		payload, err = wire.EncodeStmtExecuteOptions(prepared.ID(), params, op.Config().Limits.MaxMessageBytes, statement.ExecuteOptions(op, query, false))
	}
	if err != nil {
		return nil, false, err
	}
	payload[5] = 1
	if combined {
		binary.LittleEndian.PutUint32(payload[len(payload)-16:], 8)
	}
	if err = statement.UploadLongData(ctx, op, prepared.ID(), values); err != nil {
		return nil, false, err
	}
	if combined {
		columns, e := openSensitive(ctx, op, decoder, prepared, payload)
		return columns, false, e
	}
	return command.OpenCursor(ctx, op, payload)
}

func openSensitive(ctx context.Context, op *session.Operation, decoder *materialize.Decoder, prepared *statement.Statement, payload []byte) ([]wire.Column, error) {
	r, err := command.Start(ctx, op, payload, true)
	if err != nil {
		return nil, err
	}
	data, err := op.Packets().ReadMessage(ctx)
	if err != nil {
		op.Discard()
		op.EndResponse()
		return nil, err
	}
	if len(data) > 0 && data[0] == 255 {
		err = command.DecodeServerError(data)
		if errors.Is(err, wire.ErrMalformed) {
			op.Discard()
		}
		op.EndResponse()
		return nil, err
	}
	header, _, budget, err := command.CombinedMetadata(ctx, op, data)
	if err != nil {
		op.Discard()
		op.EndResponse()
		return nil, err
	}
	if header.ID != prepared.ID() || int(header.Parameters) != len(prepared.Plan().Slots) || !header.HasRows || header.Flags != 0 {
		op.Discard()
		op.EndResponse()
		return nil, wire.ErrMalformed
	}
	if err = r.BeginRowset(ctx, int(header.Columns), budget); err != nil {
		return nil, err
	}
	r.NativeTail(true)
	var used int64
	sets, err := decoder.CollectResponse(ctx, r, true, &used)
	if err != nil {
		op.Discard()
		op.EndResponse()
		return nil, err
	}
	if err = decoder.CheckEpoch(); err != nil {
		return nil, err
	}
	if len(sets) != 1 {
		op.Discard()
		return nil, dberrors.ErrCursor
	}
	err = decoder.DecodeSet(ctx, &sets[0], &used)
	if err == nil {
		err = decoder.RefCursors(ctx, ctx, &sets[0], 0, &used, map[materialize.RefCursorID]bool{materialize.RefCursorID(prepared.ID()): true})
	}
	if err != nil {
		op.Discard()
		return nil, err
	}
	return sets[0].Columns, ctx.Err()
}

func (o *operation) loadIdentity(ctx context.Context) error {
	columns := o.cur.columns
	if o.cur.options.Mode == Sensitive && len(columns) > 0 {
		columns = columns[:len(columns)-1]
	}
	if len(columns) == 0 {
		return dberrors.ErrCursor
	}
	col := columns[0]
	set, err := catalog.QueryRows(ctx, o.op, catalog.DictionaryPlan(o.op.Protocol().Mode, col.Schema, col.OriginalTable))
	if err != nil {
		return err
	}
	if err = o.check(); err != nil {
		return err
	}
	o.cur.identity, err = catalog.ResolveCursorIdentity(o.op.Protocol().Mode, columns, []catalog.Result{set})
	return err
}
