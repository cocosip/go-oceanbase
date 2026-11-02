package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

// interpolateClient resolves complex input identities before pure statement interpolation.
func interpolateClient(ctx context.Context, op *session.Operation, plan sqltext.Plan, values []any) ([]byte, error) {
	if hasLong(values) {
		if !clientMultiStatement(op, plan.Query) {
			return nil, fmt.Errorf("%w: long data requires server preparation", dberrors.ErrUnsupported)
		}
		var err error
		values, err = materializeClientLongData(ctx, op, values)
		if err != nil {
			return nil, err
		}
	}
	hasComplex := false
	for _, value := range values {
		switch value.(type) {
		case types.Object, types.Array:
			hasComplex = true
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

func clientMultiStatement(op *session.Operation, query string) bool {
	if op.Config().PrepareMode != config.PrepareClient || !op.Config().AllowMultiStatements || op.Protocol().Mode != config.ModeMySQL || op.Protocol().Capabilities&wire.ClientMultiStatements == 0 {
		return false
	}
	noBackslash := op.Protocol().OK.Status&512 != 0
	ansiModes := []bool{false}
	if !noBackslash && strings.Contains(query, `"`) && strings.Contains(query, `\`) {
		ansiModes = append(ansiModes, true)
	}
	for _, ansiQuotes := range ansiModes {
		tokens, err := sqltext.LexSQL(query, op.Protocol().Mode, noBackslash, ansiQuotes)
		if err != nil {
			return false
		}
		found := false
		for i, token := range tokens {
			if token.Kind == ';' && i+1 < len(tokens) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func materializeClientLongData(ctx context.Context, op *session.Operation, values []any) ([]any, error) {
	result := slices.Clone(values)
	replacements := make(map[*atomic.Bool]any)
	for i, value := range result {
		source, ok := value.(*streaminput.Source)
		if !ok {
			continue
		}
		if replacement, ok := replacements[source.State]; ok {
			result[i] = replacement
			continue
		}
		if err := source.Validate(); err != nil {
			return nil, err
		}
		if !source.Claim() {
			return nil, dberrors.ErrLongData
		}
		var data []byte
		var err error
		limit := source.MaxBytes
		if limit == 0 {
			limit = op.Config().Limits.MaxLOBBytes
		}
		reader := streaminput.NewBoundedReader(ctx, source.Reader, source.Length, limit)
		if source.Length == 0 {
			var probe [1]byte
			_, err = reader.Read(probe[:])
			if err == nil {
				err = io.ErrNoProgress
			}
			if err == io.EOF {
				err = nil
			}
		} else {
			data, err = io.ReadAll(reader)
		}
		if err != nil {
			return nil, errors.Join(dberrors.ErrLongData, err)
		}
		var replacement any = data
		if source.Text {
			if _, err = streaminput.TextBoundary(data, true); err != nil {
				return nil, errors.Join(dberrors.ErrLongData, err)
			}
			replacement = string(data)
		}
		replacements[source.State] = replacement
		result[i] = replacement
	}
	return result, nil
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
