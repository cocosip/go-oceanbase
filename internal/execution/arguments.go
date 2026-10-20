package execution

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/call"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/parameters"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/metadata"
)

func hasNamed(args []driver.NamedValue) bool {
	for _, arg := range args {
		if arg.Name != "" {
			return true
		}
	}
	return false
}
func hasOutputs(args []driver.NamedValue) bool {
	for _, arg := range args {
		if _, ok := arg.Value.(*call.Binding); ok {
			return true
		}
	}
	return false
}
func hasLong(values []any) bool {
	for _, value := range values {
		if _, ok := value.(*streaminput.Source); ok {
			return true
		}
	}
	return false
}

// bind reads fresh routine metadata under the command's existing operation.
func (e *Engine) bind(ctx context.Context, op *session.Operation, plan sqltext.Plan, args []driver.NamedValue) ([]any, error) {
	if op.Protocol().Mode != config.ModeMySQL || !hasNamed(args) {
		return parameters.Bind(plan, args)
	}
	if op.ResponseActive() {
		return nil, catalog.ErrResponseActive
	}
	if _, err := command.EncodeSQL(plan.Query, op.Config().Session.Charset, op.Config().Limits.MaxMessageBytes-1); err != nil {
		return nil, err
	}
	routine, err := sqltext.NamedRoutine(plan.Query, op.Protocol().OK.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	if len(args) != plan.NumInput {
		return nil, fmt.Errorf("%w: argument count", dberrors.ErrBind)
	}
	names := make(map[string]bool, len(args))
	for _, arg := range args {
		name := strings.ToUpper(arg.Name)
		if name == "" || names[name] {
			return nil, fmt.Errorf("%w: missing or duplicate named argument", dberrors.ErrBind)
		}
		names[name] = true
	}
	if routine.Schema == "" {
		values, _, err := command.QueryScalars(ctx, op, []byte("SELECT DATABASE()"), 1)
		if err != nil {
			return nil, err
		}
		if !values[0].Valid || values[0].String == "" {
			return nil, fmt.Errorf("%w: CALL requires a current schema", dberrors.ErrBind)
		}
		routine.Schema = values[0].String
	}
	exact := strings.NewReplacer("/", "//", "%", "/%", "_", "/_")
	result, err := catalog.Metadata(ctx, op, metadata.MetadataFilter{Kind: metadata.MetadataRoutineParameters, SchemaPattern: exact.Replace(routine.Schema), NamePattern: exact.Replace(routine.Name)}, true)
	if err != nil {
		return nil, err
	}
	plan, directions, err := catalog.RoutineParameterPlan(plan, routine, result.Rows, result.More, op.Config().Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	values, err := parameters.Bind(plan, args)
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		out, output := value.(*call.Binding)
		if directions[i] == "" || directions[i] == "IN" && output || directions[i] == "OUT" && (!output || out.In) || directions[i] == "INOUT" && (!output || !out.In) {
			return nil, fmt.Errorf("%w: routine argument direction", dberrors.ErrBind)
		}
	}
	return values, nil
}
