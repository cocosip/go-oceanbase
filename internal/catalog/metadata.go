package catalog

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/metadata"
)

// Metadata reads dictionary rows under an existing operation. Statement parameter
// preparation remains with the execution coordinator.
func Metadata(ctx context.Context, op *session.Operation, f metadata.MetadataFilter, procedureOnly bool) (result Result, err error) {

	if op.ResponseActive() {
		return Result{}, ErrResponseActive
	}
	if op.Closed() {
		return Result{}, driver.ErrBadConn
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	p := op.Protocol()
	if f.Kind == metadata.MetadataRoutineParameters && p.Mode == config.ModeMySQL && !HasParameterDictionary(p.OBVersion) {
		return routineFallback(ctx, op, f, procedureOnly)
	}
	var plan Query
	if procedureOnly && f.Kind == metadata.MetadataRoutineParameters && p.Mode == config.ModeMySQL {
		plan, err = MySQLPlan(p.OBVersion, f, true)
	} else {
		plan, err = Plan(p.Mode, p.OBVersion, f)
	}
	if err != nil {
		return Result{}, err
	}
	if f.Kind == metadata.MetadataStatementParameters {
		return Result{}, dberrors.ErrUnsupported
	}
	if f.Kind == metadata.MetadataTypeInfo {
		return BufferedRows(ctx, op, f.Kind, BuiltinTypeInfo(p.Mode))
	}
	sets, err := QuerySets(ctx, op, plan)
	if err != nil {
		return Result{}, err
	}
	result = sets[0]
	result.Columns = MetadataColumns(f.Kind)
	rows := rowset.NewMetadata(ctx, []rowset.Set{{Columns: result.Columns, Rows: result.Rows}})
	defer func() { _ = rows.Close() }()
	for _, values := range result.Rows {
		if err := rows.Next(values); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

// BufferedRows applies the original generated-metadata memory and item budgets.
func BufferedRows(ctx context.Context, op *session.Operation, kind metadata.MetadataKind, values [][]driver.Value) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if op.Config().Limits.MaxResultItems < 1 {
		return Result{}, fmt.Errorf("oceanbase: result item limit exceeded")
	}
	if len(values) > command.MaxColumns {
		return Result{}, wire.ErrMessageTooLarge
	}
	wireColumns := MetadataColumns(kind)
	size := valuecodec.SnapshotMetadataSize(wireColumns)
	for _, row := range values {
		size += valuecodec.SnapshotValuesSize(row) + int64(len(row))*16
	}
	if size > op.Config().Limits.MaxMessageBytes {
		return Result{}, wire.ErrMessageTooLarge
	}
	return Result{Columns: wireColumns, Rows: values, Bytes: size}, nil
}

func routineFallback(ctx context.Context, op *session.Operation, f metadata.MetadataFilter, procedureOnly bool) (Result, error) {
	schema, name, err := ValidateRoutineFallback(f)
	if err != nil {
		return Result{}, err
	}
	if f.Catalog != "" && f.Catalog != metadataCatalogMySQL {
		return BufferedRows(ctx, op, f.Kind, nil)
	}
	if schema == "" {
		sets, err := QueryRows(ctx, op, Query{SQL: "SELECT DATABASE()"})
		if err != nil {
			return Result{}, err
		}
		v, err := Scalar(sets)
		if err != nil {
			return Result{}, err
		}
		schema = Text(v)
		if schema == "" {
			return Result{}, fmt.Errorf("%w: routine metadata requires a current database", dberrors.ErrBind)
		}
	}
	qualified := sqltext.QuoteIdentifier(config.ModeMySQL, schema) + "." + sqltext.QuoteIdentifier(config.ModeMySQL, name)
	sets, err := QueryRows(ctx, op, Query{SQL: "SHOW CREATE PROCEDURE " + qualified})
	if err != nil {
		var server *dberrors.Error
		if procedureOnly || !errors.As(err, &server) || server.Code != 1305 {
			return Result{}, err
		}
		sets, err = QueryRows(ctx, op, Query{SQL: "SHOW CREATE FUNCTION " + qualified})
		if err != nil {
			return Result{}, err
		}
	}
	if len(sets.Rows) != 1 {
		return Result{}, fmt.Errorf("oceanbase: invalid SHOW CREATE routine response")
	}
	var ddl, sqlMode string
	for i, col := range sets.Columns {
		switch strings.ToLower(col.Name) {
		case "create procedure", "create function":
			ddl = Text(sets.Rows[0][i])
		case "sql_mode":
			sqlMode = Text(sets.Rows[0][i])
		}
	}
	if ddl == "" {
		return Result{}, fmt.Errorf("%w: SHOW CREATE routine definition is unavailable", dberrors.ErrUnsupported)
	}
	values, err := ParseRoutineCreateMode(ddl, schema, name, strings.Contains(strings.ToUpper(sqlMode), "NO_BACKSLASH_ESCAPES"))
	if err != nil {
		return Result{}, err
	}
	if f.ColumnPattern != "" {
		filtered := values[:0]
		for _, row := range values {
			if row[5] != nil && MetadataLike(Text(row[5]), f.ColumnPattern) {
				filtered = append(filtered, row)
			}
		}
		values = filtered
	}
	return BufferedRows(ctx, op, f.Kind, values)
}
