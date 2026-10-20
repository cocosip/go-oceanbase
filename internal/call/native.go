package call

import (
	"context"
	"fmt"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// PreparePayload encodes Oracle IN and OUT slots under the original per-parameter budget.
func PreparePayload(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, plan sqltext.Plan, values []any, prepared *statement.Statement) ([]byte, error) {
	if !statement.SupportsPrepareExecute(op.Protocol().OBVersion) {
		return nil, fmt.Errorf("%w: Oracle output bindings require OceanBase >= 2.2.76", dberrors.ErrUnsupported)
	}
	cfg := op.Config()
	query, err := command.EncodeSQL(plan.SQL, cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, err
	}
	budget := cfg.Limits.MaxMessageBytes - 30 - (wire.Parameter{Data: query, LengthEncoded: true}).EncodedSize()
	if len(values) > 0 {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	if budget < 0 {
		return nil, wire.ErrMessageTooLarge
	}
	params := make([]wire.Parameter, len(values))
	for i, value := range values {
		if b, ok := value.(*Binding); ok {
			value = b.input
			if b.In {
				if err = ValidateValue(op, b, b.input); err != nil {
					return nil, err
				}
			}
		}
		limit := budget
		switch value.(type) {
		case types.Raw, []byte:
			limit = min(limit, cfg.Limits.MaxLOBBytes)
		}
		params[i], err = parameters.EncodeValue(ctx, op, resolver, value, limit)
		if err != nil {
			return nil, err
		}
		size := params[i].EncodedSize() + int64(len(params[i].TypeMetadata))
		if size > budget {
			return nil, wire.ErrMessageTooLarge
		}
		budget -= size
	}
	var id uint32
	options := statement.ExecuteOptions(op, query, true)
	if prepared != nil {
		id = prepared.ID()
		options = prepared.ParameterOptions(op, query, params, true)
	}
	return wire.EncodePrepareExecuteOptions(id, query, params, cfg.Limits.MaxMessageBytes, options)
}
