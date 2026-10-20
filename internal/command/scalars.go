package command

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"

	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// QueryScalarsRaw requires exactly one ordinary row and no extra results.
func QueryScalarsRaw(ctx context.Context, op *session.Operation, query []byte, columns int) ([]driver.Value, []wire.Column, error) {
	r, err := Start(ctx, op, append([]byte{3}, query...), false)
	if err != nil {
		return nil, nil, err
	}
	event, err := r.Advance(ctx)
	if err != nil {
		return nil, nil, err
	}
	if event.Kind != RowResult || len(r.columns) != columns || r.items != 1 {
		return nil, nil, r.fail(wire.ErrMalformed)
	}
	descriptions := r.columns
	values, err := r.NextValues(ctx)
	if err != nil {
		if err == io.EOF {
			err = r.fail(wire.ErrMalformed)
		}
		return nil, nil, err
	}
	_, err = r.NextValues(ctx)
	if err != nil && err != io.EOF {
		return nil, nil, err
	}
	if err != io.EOF || r.more {
		return nil, nil, r.fail(wire.ErrMalformed)
	}
	return values, descriptions, nil
}

// QueryScalars returns one bounded row using nullable text values.
func QueryScalars(ctx context.Context, op *session.Operation, query []byte, columns int) ([]sql.NullString, []wire.Column, error) {
	values, descriptions, err := QueryScalarsRaw(ctx, op, query, columns)
	if err != nil {
		return nil, nil, err
	}
	out := make([]sql.NullString, len(values))
	for i, value := range values {
		if err = out[i].Scan(value); err != nil {
			return nil, nil, err
		}
	}
	return out, descriptions, nil
}
