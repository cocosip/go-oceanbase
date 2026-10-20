package batch

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func batchArrayCandidate(request Request) bool {
	if request.Options.Strategy != ArrayBinding || !request.Options.ContinueOnError || len(request.RawRows) < 2 || !batchScalarRows(request.RawRows) {
		return false
	}
	for _, row := range request.RawRows {
		for _, value := range row {
			if named, ok := value.(sql.NamedArg); ok {
				value = named.Value
			}
			switch value.(type) {
			case uint, uint64:
				return false
			}
		}
	}
	return true
}

func (n *native) tryBatchArray(ctx context.Context, request Request, writable bool) (handled bool, err error) {
	if !batchArrayCandidate(request) || n.op.Config().PrepareMode != config.PrepareServer {
		return false, nil
	}
	if n.op.Closed() {
		return true, driver.ErrBadConn
	}
	p := n.op.Protocol()
	if p.Mode != config.ModeOracle || !statement.SupportsVersion(p.OBVersion, 4, 2, 1) || !writable || p.OK.Status&2 != 0 || !sqltext.BatchDML(request.Query, p.Mode, p.OK.Status&512 != 0) || !n.batchParametersValid(request) {
		return false, nil
	}
	plan, bindErr := sqltext.BuildBindPlan(request.Query, p.Mode)
	if bindErr != nil || len(plan.Slots) == 0 {
		return false, nil
	}
	if err = n.clearStatements(ctx); err != nil {
		request.Result.Items[0].Err = err
		return true, Failure(request.Result)
	}
	prepared, err := statement.Prepare(ctx, n.op, request.Query)
	if err != nil {
		request.Result.Items[0].Err = err
		return true, Failure(request.Result)
	}
	defer func() { err = n.cleanup(prepared, err, request.Result) }()
	request.Result.Strategy = ArrayBinding
	return true, n.execBatchArrayLocked(ctx, request, prepared)
}

func (n *native) tryBatchRewrite(ctx context.Context, request Request) (handled bool, err error) {
	if request.Options.Strategy != Rewrite || request.Options.ContinueOnError || len(request.RawRows) < 2 || request.Options.ChunkRows < 2 || !n.op.Config().AllowMultiStatements || !batchScalarRows(request.RawRows) {
		return false, nil
	}
	if n.op.Closed() {
		return true, driver.ErrBadConn
	}
	p := n.op.Protocol()
	if p.Mode != config.ModeMySQL || p.Capabilities&wire.ClientMultiStatements == 0 || !sqltext.BatchDML(request.Query, p.Mode, p.OK.Status&512 != 0) {
		return false, nil
	}
	plan, err := sqltext.BuildBindPlanEscapes(request.Query, p.Mode, p.OK.Status&512 != 0)
	if err != nil || len(plan.Slots) > request.Options.ChunkParameters/2 || n.op.Config().Limits.MaxResultItems < 2 {
		return false, nil
	}
	allowed, err := n.batchRewriteAllowedLocked(ctx)
	if err != nil {
		request.Result.Items[0].Err = err
		return true, Failure(request.Result)
	}
	if !allowed {
		return false, nil
	}
	request.Result.Strategy = Rewrite
	if err = n.clearStatements(ctx); err != nil {
		request.Result.Items[0].Err = err
		return true, Failure(request.Result)
	}
	return true, n.cleanup(nil, n.execBatchRewriteLocked(ctx, request), request.Result)
}

func (n *native) tryBatchMultiValues(ctx context.Context, request Request) (bool, error) {
	if request.Options.Strategy != MultiValues || len(request.RawRows) < 2 || request.Options.ChunkRows < 2 || !batchScalarRows(request.RawRows) {
		return false, nil
	}
	if n.op.Closed() {
		return true, driver.ErrBadConn
	}
	p := n.op.Protocol()
	if p.Mode != config.ModeMySQL {
		return false, nil
	}
	_, plan, ok := sqltext.BatchValuesPlan(request.Query, p.OK.Status&512 != 0)
	if !ok || len(plan.Slots) > request.Options.ChunkParameters/2 {
		return false, nil
	}
	request.Result.Strategy = MultiValues
	if err := n.clearStatements(ctx); err != nil {
		request.Result.Items[0].Err = err
		return true, Failure(request.Result)
	}
	return true, n.cleanup(nil, n.execBatchValuesLocked(ctx, request), request.Result)
}
