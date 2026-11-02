package execution

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/types"
)

// interpolateClient resolves complex input identities before pure statement interpolation.
func interpolateClient(ctx context.Context, op *session.Operation, plan sqltext.Plan, values []any) ([]byte, error) {
	hasComplex := false
	for _, value := range values {
		switch value.(type) {
		case types.Object, types.Array:
			hasComplex = true
		}
		if _, ok := value.(*streaminput.Source); ok {
			return nil, fmt.Errorf("%w: long data requires server preparation", dberrors.ErrUnsupported)
		}
	}
	if hasComplex {
		if err := validateConstructorSQL(op, plan.Query); err != nil {
			return nil, err
		}
		values = slices.Clone(values)
		var resolver catalog.Resolver
		for i, value := range values {
			var key valuecodec.Key
			switch v := value.(type) {
			case types.Object:
				key = valuecodec.Key{Mode: config.ModeOracle, Schema: v.Schema, Name: v.Type, Version: v.Version}
			case types.Array:
				key = valuecodec.Key{Mode: config.ModeOracle, Schema: v.Schema, Name: v.Type, Version: v.Version}
			default:
				continue
			}
			if key.Name == "" {
				return nil, dberrors.ErrUnsupported
			}
			d, err := resolver.Resolve(ctx, op, key)
			if err != nil {
				return nil, err
			}
			values[i] = valuecodec.ResolvedComplex{Descriptor: d, Value: value}
		}
	}
	return statement.Interpolate(op, plan, values)
}

func validateConstructorSQL(op *session.Operation, query string) error {
	if op.Protocol().Mode != config.ModeOracle {
		return dberrors.ErrUnsupported
	}
	if _, err := command.EncodeSQL(query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1); err != nil {
		return err
	}
	tokens, err := sqltext.LexSQL(query, config.ModeOracle, false, false)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token.Kind != 'w' {
			continue
		}
		switch strings.ToUpper(query[token.Start:token.End]) {
		case "CREATE", "ALTER", "DROP", "RENAME", "TRUNCATE", "EXECUTE":
			return fmt.Errorf("%w: complex constructors cannot share a command with DDL or dynamic SQL", dberrors.ErrUnsupported)
		}
	}
	return nil
}
