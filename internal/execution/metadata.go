package execution

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/metadata"
)

// Metadata returns the fixed public dictionary schema.
func (e *Engine) Metadata(ctx context.Context, filter metadata.MetadataFilter) (driver.Rows, error) {
	op, err := e.acquire(ctx)
	if err != nil {
		return nil, err
	}
	owned := true
	defer func() {
		if owned {
			e.release(op)
		}
	}()
	if filter.Kind == metadata.MetadataStatementParameters {
		plan, err := catalog.Plan(op.Protocol().Mode, op.Protocol().OBVersion, filter)
		if err != nil {
			return nil, err
		}
		s, err := statement.Prepare(ctx, op, plan.SQL)
		if err != nil {
			return nil, err
		}
		if err = s.Close(ctx, op); err != nil {
			return nil, err
		}
		values := catalog.StatementParameters(op.Protocol().Mode, s.Plan(), s.Parameters(), op.Config().Session)
		return rowset.NewMetadata(ctx, []rowset.Set{{Columns: catalog.MetadataColumns(filter.Kind), Rows: values}}), nil
	}
	p := op.Protocol()
	if filter.Kind == metadata.MetadataTypeInfo || filter.Kind == metadata.MetadataRoutineParameters && p.Mode == config.ModeMySQL && !catalog.HasParameterDictionary(p.OBVersion) {
		result, err := catalog.Metadata(ctx, op, filter, false)
		if err != nil {
			return nil, err
		}
		return rowset.NewMetadata(ctx, []rowset.Set{{Columns: result.Columns, Rows: result.Rows}}), nil
	}
	plan, err := catalog.Plan(p.Mode, p.OBVersion, filter)
	if err != nil {
		return nil, err
	}
	payload, err := command.Interpolate(op, plan.SQL, plan.Args)
	if err != nil {
		return nil, err
	}
	rows, err := e.start(ctx, op, append([]byte{3}, payload...), false, nil, nil, nil, Options{}, true, false)
	if err != nil {
		return nil, err
	}
	columns := catalog.MetadataColumns(filter.Kind)
	if len(rows.columns) != len(columns) {
		return nil, errors.Join(fmt.Errorf("oceanbase: metadata projection count differs from plan"), rows.Close())
	}
	rows.retainOperation = false
	owned = rows.released
	return rowset.WrapMetadata(rows, columns), nil
}
