package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/execution"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
)

type conn struct{ engine *execution.Engine }

func (c *conn) Close() error                   { return c.engine.Close() }
func (c *conn) Ping(ctx context.Context) error { return c.engine.Ping(ctx) }
func (c *conn) IsValid() bool                  { return c.engine.Valid() }

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if query == "" && len(args) == 1 {
		switch request := args[0].Value.(type) {
		case cursorMutationRequest:
			return c.queryCursorRefresh(ctx, request)
		case cursorFetchRequest:
			return c.queryCursorFetch(ctx, request)
		case metadataRequest:
			return c.engine.Metadata(ctx, request.filter)
		case executionRequest:
			_, transaction := request.execution.session.(*sql.Tx)
			return c.engine.Query(ctx, request.query, deferredNamedValues(request.args, request.execution.session), execution.Options{
				Outputs: hasOutputArguments(request.args), Recorder: &request.execution.recorder,
				AllowResources: validSession(request.execution.session), TransactionSession: transaction,
			})
		}
	}
	return c.engine.Query(ctx, query, args, execution.Options{Outputs: hasOutputArguments(args)})
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query == "" && len(args) == 1 {
		switch request := args[0].Value.(type) {
		case *xaRequest:
			return c.execXA(ctx, request)
		case changeUserRequest:
			return c.execChangeUser(ctx, request)
		case *sessionOperationRequest:
			return c.execSessionOperation(ctx, request)
		case localInfileRequest:
			return c.engine.Import(ctx, request.query, request.input.source())
		case batchRequest:
			return c.execBatch(ctx, request)
		case cursorMutationRequest:
			return c.execCursorMutation(ctx, request)
		case cursorOpenRequest:
			return c.execOpenCursor(ctx, request)
		case lobOpenRequest:
			return c.execOpenLOB(ctx, request)
		case tableBlobOpenRequest:
			return c.execOpenTableBlob(ctx, request)
		case lobRequest:
			return c.execLOB(ctx, request)
		case resourceRequest:
			if request.handle == nil {
				return nil, ErrResource
			}
			if err := c.engine.CloseResource(ctx, request.handle.ref); err != nil {
				return nil, err
			}
			return command.Result{Known: true}, nil
		case savepointRequest:
			return c.execSavepoint(ctx, request)
		}
	}
	result, err := c.engine.Exec(ctx, query, args, execution.Options{Outputs: hasOutputArguments(args)})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeArgument(value any) (any, error) {
	for {
		switch v := value.(type) {
		case *LongData:
			source := v.source()
			return source, source.Validate()
		case *streaminput.Source:
			return v, v.Validate()
		case CursorOut:
			return makeOutput(Out{Dest: v.Dest, Type: "REF CURSOR"})
		case sql.Out:
			return makeOutput(Out{Dest: v.Dest, In: v.In})
		case Out:
			return makeOutput(v)
		case *call.Binding:
			return v, nil
		}
		next, again, err := parameters.NormalizeStep(value)
		if err != nil || !again {
			return next, err
		}
		value = next
	}
}

// argumentSource keeps the original reference until its logical conversion point.
type argumentSource struct {
	raw     any
	session Session
}

func (s argumentSource) Resolve() (any, error) {
	value, err := normalizeArgument(s.raw)
	if err == nil && s.session != nil {
		setOutputSession([]driver.NamedValue{{Value: value}}, s.session)
	}
	return value, err
}

func deferredNamedValues(args []driver.NamedValue, session Session) []driver.NamedValue {
	values := append([]driver.NamedValue(nil), args...)
	for i := range values {
		values[i].Value = argumentSource{raw: values[i].Value, session: session}
	}
	return values
}

func deferredBatchRows(rows [][]any) [][]any {
	values := make([][]any, len(rows))
	for i, row := range rows {
		values[i] = make([]any, len(row))
		for j, value := range row {
			if named, ok := value.(sql.NamedArg); ok {
				named.Value = argumentSource{raw: named.Value}
				values[i][j] = named
			} else {
				values[i][j] = argumentSource{raw: value}
			}
		}
	}
	return values
}

func (*conn) CheckNamedValue(value *driver.NamedValue) error {
	switch value.Value.(type) {
	case *xaRequest, changeUserRequest, *sessionOperationRequest, localInfileRequest, batchRequest, cursorMutationRequest, cursorOpenRequest, cursorFetchRequest, savepointRequest, executionRequest, metadataRequest, resourceRequest, lobOpenRequest, lobRequest, tableBlobOpenRequest:
		return nil
	}
	normalized, err := normalizeArgument(value.Value)
	if err == nil {
		value.Value = normalized
	}
	return err
}

var (
	_ driver.Conn              = (*conn)(nil)
	_ driver.Pinger            = (*conn)(nil)
	_ driver.Validator         = (*conn)(nil)
	_ driver.QueryerContext    = (*conn)(nil)
	_ driver.ExecerContext     = (*conn)(nil)
	_ driver.NamedValueChecker = (*conn)(nil)
)
