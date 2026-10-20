package cursor

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/rowset"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

func (o *operation) refreshSensitive(ctx context.Context, rows [][]driver.Value) ([][]driver.Value, error) {
	if len(o.cur.columns) < 2 {
		return nil, dberrors.ErrCursor
	}
	visible := o.cur.columns[:len(o.cur.columns)-1]
	id, err := catalog.ProjectedCursorIdentity(config.ModeOracle, visible)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(id.Columns))
	for i, name := range id.Columns {
		names[i] = sqltext.QuoteIdentifier(config.ModeOracle, name)
	}
	query := "SELECT " + strings.Join(names, ",") + " FROM " + id.Table + " WHERE ROWID = ?"
	used := rowset.MetadataSize(visible)
	for i, row := range rows {
		if len(row) != len(o.cur.columns) || row[len(row)-1] == nil {
			return nil, dberrors.ErrCursor
		}
		rowid := types.RowID(catalog.Text(row[len(row)-1]))
		if _, err = rowid.Value(); err != nil {
			return nil, err
		}
		fresh, err := o.readSensitive(ctx, query, rowid)
		if err != nil {
			return nil, err
		}
		if len(fresh) != len(o.cur.columns)-1 {
			return nil, dberrors.ErrCursor
		}
		rows[i] = fresh
		used += rowset.SnapshotSize(rows[i : i+1])
		if used > o.op.Config().Limits.MaxMessageBytes {
			return nil, wire.ErrMessageTooLarge
		}
	}
	return rows, nil
}

func (o *operation) readSensitive(ctx context.Context, query string, rowid types.RowID) (_ []driver.Value, err error) {
	prepared, payload, err := prepareCommand(ctx, o.op, o.resolver, query, []driver.NamedValue{{Ordinal: 1, Value: rowid}})
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, closeCommand(o.op, prepared)) }()
	if err = o.check(); err != nil {
		return nil, err
	}
	sets, used, err := o.receive(ctx, payload)
	if err != nil {
		return nil, err
	}
	if len(sets) != 1 || len(sets[0].Rows)+len(sets[0].RawRows) > 1 {
		return nil, dberrors.ErrCursor
	}
	err = o.decoder.DecodeSet(ctx, &sets[0], &used)
	if err == nil {
		err = o.decoder.RefCursors(ctx, ctx, &sets[0], 0, &used, make(map[materialize.RefCursorID]bool))
	}
	if err != nil {
		o.op.Discard()
		return nil, err
	}
	if len(sets[0].Rows) == 0 {
		return nil, errors.Join(dberrors.ErrCursor, sql.ErrNoRows)
	}
	return sets[0].Rows[0], nil
}
