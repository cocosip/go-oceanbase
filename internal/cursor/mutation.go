package cursor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ColumnChange contains one already-normalized projected column assignment.
type ColumnChange struct {
	Name  string
	Value any
}

// Operation selects one cursor mutation or refresh operation.
type Operation uint8

// Cursor mutation operations.
const (
	Update Operation = iota
	Delete
	Insert
	Refresh
)

// Request carries a normalized key and changes from the public extension boundary.
type Request struct {
	Operation Operation
	Key       map[string]any
	Changes   []ColumnChange
}

func predicate(id *catalog.CursorIdentity, mode config.Mode, key map[string]any) (string, []driver.NamedValue, error) {
	if len(key) != len(id.Keys) {
		return "", nil, dberrors.ErrCursor
	}
	used := make(map[string]bool)
	var predicates []string
	var args []driver.NamedValue
	for i, name := range id.Keys {
		value, err := catalog.ResolveKey(mode, name, key, used)
		if err != nil {
			return "", nil, errors.Join(dberrors.ErrCursor, err)
		}
		args = append(args, driver.NamedValue{Ordinal: i + 1, Value: value})
		predicates = append(predicates, sqltext.QuoteIdentifier(mode, name)+" = ?")
	}
	return strings.Join(predicates, " AND "), args, nil
}

func assignments(id *catalog.CursorIdentity, mode config.Mode, changes []ColumnChange) ([]string, []driver.NamedValue, error) {
	used := make(map[string]bool)
	var names []string
	var args []driver.NamedValue
	for _, change := range changes {
		found := ""
		for _, name := range id.Columns {
			if catalog.MatchIdentifier(mode, change.Name, name) {
				if found != "" {
					return nil, nil, dberrors.ErrCursor
				}
				found = name
			}
		}
		if found == "" || used[found] || id.Generated[found] {
			return nil, nil, dberrors.ErrCursor
		}
		used[found] = true
		names = append(names, sqltext.QuoteIdentifier(mode, found))
		args = append(args, driver.NamedValue{Ordinal: len(args) + 1, Value: change.Value})
	}
	return names, args, nil
}

func (o *operation) mutationSQL(request Request, writable bool) (string, []driver.NamedValue, error) {
	id := o.cur.identity
	if id == nil {
		return "", nil, dberrors.ErrUnsupported
	}
	mode := o.op.Protocol().Mode
	if !writable || o.op.Protocol().OK.Status&8192 != 0 || !id.Locking {
		return "", nil, dberrors.ErrUnsupported
	}
	if request.Operation > Refresh || (request.Operation == Update || request.Operation == Insert) && (len(request.Changes) == 0 || len(request.Changes) > command.MaxColumns) {
		return "", nil, dberrors.ErrCursor
	}
	for _, change := range request.Changes {
		if !valueformat.Identifier(change.Name) {
			return "", nil, dberrors.ErrCursor
		}
	}
	names, values, err := assignments(id, mode, request.Changes)
	if err != nil {
		return "", nil, err
	}
	if request.Operation == Insert {
		key := make(map[string]any)
		for i, name := range names {
			for _, primary := range id.Keys {
				if name == sqltext.QuoteIdentifier(mode, primary) {
					key[primary] = values[i].Value
				}
			}
		}
		for name, value := range key {
			key[name], err = parameters.CloneKey(value)
			if err != nil {
				return "", nil, errors.Join(dberrors.ErrCursor, err)
			}
		}
		if _, _, err = predicate(id, mode, key); err != nil {
			return "", nil, err
		}
		return "INSERT INTO " + id.Table + " (" + strings.Join(names, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + ")", values, nil
	}
	pred, args, err := predicate(id, mode, request.Key)
	if err != nil {
		return "", nil, err
	}
	from := " FROM " + id.Table + " WHERE " + pred
	switch request.Operation {
	case Refresh:
		var projection []string
		for i, name := range id.Columns {
			projection = append(projection, sqltext.QuoteIdentifier(mode, name)+" AS "+sqltext.QuoteIdentifier(mode, o.cur.columns[i].Name))
		}
		return "SELECT " + strings.Join(projection, ",") + from, args, nil
	case Delete:
		return "DELETE" + from, args, nil
	case Update:
		for i := range names {
			names[i] += " = ?"
		}
		all := append(values, args...)
		for i := range all {
			all[i].Ordinal = i + 1
		}
		return "UPDATE " + id.Table + " SET " + strings.Join(names, ",") + " WHERE " + pred, all, nil
	}
	return "", nil, dberrors.ErrCursor
}

func prepareCommand(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, query string, args []driver.NamedValue) (*statement.Statement, []byte, error) {
	prepared, err := statement.Prepare(ctx, op, query)
	if err != nil {
		return nil, nil, err
	}
	values, err := parameters.Bind(prepared.Plan(), args)
	var payload []byte
	if err == nil {
		budget := op.Config().Limits.MaxMessageBytes - 10 - int64((len(values)+7)/8+1+2*len(values))
		params, e := parameters.Encode(ctx, op, resolver, values, budget)
		err = e
		if err == nil {
			payload, err = wire.EncodeStmtExecute(prepared.ID(), params, op.Config().Limits.MaxMessageBytes)
		}
	}
	if err != nil {
		return nil, nil, errors.Join(err, closeCommand(op, prepared))
	}
	return prepared, payload, nil
}

func closeCommand(op *session.Operation, prepared *statement.Statement) error {
	cleanup, cancel := context.WithTimeout(context.Background(), op.Config().Limits.CleanupTimeout)
	defer cancel()
	return prepared.Close(cleanup, op)
}

// Mutate sends a validated mutation and marks only the original failure boundaries for retirement.
func (m *Manager) Mutate(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, ref session.ResourceRef, request Request, writable bool, retireResource func() error) (_ command.Result, retire bool, err error) {
	o, err := m.operation(op, resolver, ref)
	if err != nil {
		return command.Result{}, false, err
	}
	query, args, err := o.mutationSQL(request, writable)
	if err != nil {
		return command.Result{}, false, err
	}
	prepared, payload, err := prepareCommand(ctx, op, o.resolver, query, args)
	if err != nil {
		return command.Result{}, true, err
	}
	defer func() { err = errors.Join(err, closeCommand(op, prepared)) }()
	defer func() {
		retire = err != nil
		if retire && retireResource != nil {
			err = errors.Join(err, retireResource())
		}
	}()
	if request.Operation != Insert {
		if err = o.check(); err != nil {
			return command.Result{}, false, err
		}
		pred, key, e := predicate(o.cur.identity, op.Protocol().Mode, request.Key)
		if e != nil {
			return command.Result{}, false, e
		}
		set, e := catalog.QueryRows(ctx, op, catalog.Query{SQL: "SELECT 1 FROM " + o.cur.identity.Table + " WHERE " + pred + " FOR UPDATE", Args: key})
		if e != nil {
			return command.Result{}, false, e
		}
		if len(set.Rows) != 1 {
			return command.Result{}, false, errors.Join(dberrors.ErrCursor, sql.ErrNoRows)
		}
	}
	if err = o.check(); err != nil {
		return command.Result{}, false, err
	}
	result, err := executeMutation(ctx, op, payload, prepared)
	if err != nil {
		return command.Result{}, false, err
	}
	if err = o.check(); err != nil {
		return command.Result{}, false, err
	}
	if op.Protocol().OK.Warnings != 0 {
		return command.Result{}, false, fmt.Errorf("%w: mutation returned warnings; roll back the caller transaction", dberrors.ErrCursor)
	}
	if !result.Known || result.Affected > 1 || request.Operation != Update && result.Affected != 1 {
		return command.Result{}, false, dberrors.ErrCursor
	}
	return result, false, nil
}

func executeMutation(ctx context.Context, op *session.Operation, payload []byte, prepared *statement.Statement) (_ command.Result, err error) {
	defer func() { err = errors.Join(err, closeCommand(op, prepared)) }()
	r, err := command.Start(ctx, op, payload, true)
	if err != nil {
		return command.Result{}, err
	}
	if _, err = r.Advance(ctx); err == nil {
		err = r.Drain(ctx)
	}
	return r.Last(), err
}

// Refresh receives the current base row without replacing a static cursor snapshot.
func (m *Manager) Refresh(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, ref session.ResourceRef, request Request, writable bool, busy *BusyToken) (_ driver.Rows, retire bool, err error) {
	if request.Operation != Refresh {
		return nil, false, dberrors.ErrCursor
	}
	o, err := m.operation(op, resolver, ref)
	if err != nil {
		return nil, false, err
	}
	query, args, err := o.mutationSQL(request, writable)
	if err != nil {
		return nil, false, err
	}
	defer func() { retire = err != nil }()
	prepared, payload, err := prepareCommand(ctx, op, o.resolver, query, args)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, closeCommand(op, prepared)) }()
	if err = o.check(); err != nil {
		return nil, false, err
	}
	sets, used, err := o.receive(ctx, payload)
	if err != nil {
		return nil, false, err
	}
	if len(sets) != 1 || len(sets[0].Rows)+len(sets[0].RawRows) != 1 {
		return nil, false, errors.Join(dberrors.ErrCursor, sql.ErrNoRows)
	}
	if err = o.decoder.DecodeSet(ctx, &sets[0], &used); err != nil {
		op.Discard()
		return nil, false, err
	}
	if err = o.check(); err != nil {
		return nil, false, err
	}
	buffered := rowset.New(ctx, op.Protocol().Mode, op.Config().Session.ParseTime, []rowset.Set{{Columns: sets[0].Columns, Rows: sets[0].Rows}})
	return &batchRows{Rows: buffered, busy: busy}, false, nil
}

func (o *operation) receive(ctx context.Context, payload []byte) ([]materialize.Set, int64, error) {
	r, err := command.Start(ctx, o.op, payload, true)
	if err != nil {
		return nil, 0, err
	}
	if _, err = r.Advance(ctx); err != nil {
		return nil, 0, err
	}
	var used int64
	sets, err := o.decoder.CollectResponse(ctx, r, true, &used)
	if err != nil {
		if o.op.ResponseActive() {
			o.op.Discard()
			o.op.EndResponse()
		}
		return nil, used, err
	}
	if err = o.check(); err != nil {
		return nil, used, err
	}
	return sets, used, nil
}
