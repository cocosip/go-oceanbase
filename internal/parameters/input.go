package parameters

import (
	"context"
	"database/sql/driver"
	"fmt"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/configspec"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// PrepareArguments preserves prepare validation and conversion order without reading inputs.
// Each deferred logical value is resolved and checked before advancing to the next one.
func PrepareArguments(op *session.Operation, query string, args []driver.NamedValue) ([]driver.NamedValue, error) {
	if int64(utf8.RuneCountInString(query)) >= op.Config().Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if len(args) > 65535 {
		return nil, fmt.Errorf("%w: too many arguments", dberrors.ErrBind)
	}
	if op.Protocol().Mode == config.CompatibilityModeMySQL {
		for _, arg := range args {
			if arg.Name != "" {
				if _, err := sqltext.NamedRoutine(query, op.Protocol().OK.Status&512 != 0); err != nil {
					return nil, err
				}
				break
			}
		}
	}
	resolved := append([]driver.NamedValue(nil), args...)
	for i := range resolved {
		if source, ok := resolved[i].Value.(ArgumentSource); ok {
			value, err := source.Resolve()
			if err != nil {
				return nil, err
			}
			resolved[i].Value = value
		}
		if source, ok := resolved[i].Value.(*streaminput.Source); ok {
			if _, err := sourceParameter(op, source); err != nil {
				return nil, err
			}
		}
	}
	if op.Protocol().Mode == config.CompatibilityModeOracle {
		plan, err := sqltext.BuildBindPlan(query, op.Protocol().Mode)
		if err != nil {
			return nil, err
		}
		if _, err = Bind(plan, resolved); err != nil {
			return nil, err
		}
	}
	return resolved, nil
}

// Bind resolves deferred logical values once after each name check, then maps physical slots.
// Already normalized values never invoke a Valuer again.
func Bind(plan sqltext.Plan, args []driver.NamedValue) ([]any, error) {
	return sqltext.BindValues(plan, args, func(value any) (any, error) {
		if source, ok := value.(ArgumentSource); ok {
			return source.Resolve()
		}
		return value, nil
	})
}

// Encode consumes physical IN values under the caller's remaining message budget.
// The resolver belongs to the outer command and is never reset here.
func Encode(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, values []any, budget int64) (valuecodec.ParamSet, error) {
	if budget < 0 {
		return nil, wire.ErrMessageTooLarge
	}
	params := make(valuecodec.ParamSet, len(values))
	for i, value := range values {
		var err error
		params[i], err = EncodeValue(ctx, op, resolver, value, budget)
		if err != nil {
			return nil, err
		}
		size := params[i].EncodedSize() + int64(len(params[i].TypeMetadata))
		if size > budget {
			return nil, wire.ErrMessageTooLarge
		}
		budget -= size
	}
	return params, nil
}

// EncodeValue encodes one normalized input at its original traversal point.
func EncodeValue(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, value any, limit int64) (wire.Parameter, error) {
	switch v := value.(type) {
	case *streaminput.Source:
		return sourceParameter(op, v)
	case types.Object:
		return encodeComplex(ctx, op, resolver, v, v.Null, valuecodec.Key{Mode: op.Protocol().Mode, Schema: v.Schema, Name: v.Type, Version: v.Version}, limit)
	case types.Array:
		return encodeComplex(ctx, op, resolver, v, v.Null, valuecodec.Key{Mode: op.Protocol().Mode, Schema: v.Schema, Name: v.Type, Version: v.Version}, limit)
	case []byte:
		if int64(len(v)) > op.Config().Limits.MaxLOBBytes {
			return wire.Parameter{}, wire.ErrMessageTooLarge
		}
	case types.Raw:
		limit = min(limit, op.Config().Limits.MaxLOBBytes)
	}
	return valuecodec.EncodeParameter(value, op.Protocol().Mode, op.Config().Session, limit)
}

func sourceParameter(op *session.Operation, source *streaminput.Source) (wire.Parameter, error) {
	cfg, p := op.Config(), op.Protocol()
	return streaminput.Parameter(source, p.Mode, configspec.SupportsPrepareExecute(p.OBVersion), cfg.Limits.MaxLOBBytes, cfg.Limits.MaxMessageBytes)
}
