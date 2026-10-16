package oceanbase

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const maxTextColumns = 4096

type textRows struct {
	conn                 *conn
	ctx                  context.Context
	columns              []wire.Column
	done, more, released bool
	items                int
	last                 result
	err                  error
	stopCancel           func() bool
	cancelDone           chan struct{}
	stopTx               func()
	binary               bool
	autoStmt             *stmt
	execution            *Execution
}

func (r *textRows) Columns() []string {
	names := make([]string, len(r.columns))
	for i, c := range r.columns {
		names[i] = c.Name
	}
	return names
}

func (r *textRows) release() {
	if !r.released {
		if r.err == nil {
			r.err = r.contextError()
		}
		r.released = true
		if r.stopCancel != nil && !r.stopCancel() {
			<-r.cancelDone
		}
		if r.stopTx != nil {
			r.stopTx()
		}
		if r.autoStmt != nil {
			ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
			if err := r.autoStmt.closeLocked(ctx); err != nil && r.err == nil {
				r.err = err
			}
			cancel()
		}
		r.execution.finish(r.err)
		r.conn.unlock()
	}
}
func (r *textRows) fail(err error, broken bool) error {
	if !r.released {
		if cause := r.contextError(); cause != nil && !errors.Is(err, cause) {
			err = errors.Join(err, cause)
		}
	}
	if r.err == nil {
		r.err = err
	}
	if !r.released && broken {
		r.conn.discard()
	}
	r.release()
	return r.err
}

func (r *textRows) contextError() error {
	if err := context.Cause(r.ctx); err != nil {
		return err
	}
	if r.conn.activeTx != nil {
		return context.Cause(r.conn.activeTx.ctx)
	}
	return nil
}

// advance skips command-only results so NextResultSet exposes ordinary rowsets only.
func (r *textRows) advance(ctx context.Context) error {
	for {
		data, err := r.conn.session.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return wire.ErrMalformed
		}
		if data[0] == 255 {
			err = decodeServerError(data)
			var server *Error
			if errors.As(err, &server) {
				if r.execution != nil && r.items < r.conn.cfg.Limits.MaxResultItems {
					r.execution.add(ResultInfo{Kind: ResultFailure, Complete: true, Err: err})
				}
				return r.fail(err, false)
			}
			return err
		}
		r.items++
		if r.items > r.conn.cfg.Limits.MaxResultItems {
			return fmt.Errorf("oceanbase: result item limit exceeded")
		}
		if data[0] == 0 {
			ok, err := wire.DecodeOK(data, r.conn.session.capabilities)
			if err != nil {
				return err
			}
			r.last = result{affected: ok.AffectedRows, id: ok.InsertID, known: true}
			r.conn.session.ok = ok
			if err = r.execution.command(ok); err != nil {
				return err
			}
			r.more = ok.Status&8 != 0
			r.done = true
			r.columns = nil
			if !r.more {
				r.release()
				return r.err
			}
			continue
		}
		n, err := wire.DecodeColumnCount(data, maxTextColumns)
		if err != nil {
			return err
		}
		r.execution.add(ResultInfo{Kind: ResultRows})
		r.columns = make([]wire.Column, n)
		metadataBytes := int64(len(data))
		for i := range r.columns {
			data, err = r.conn.session.packets.ReadMessage(ctx)
			if err != nil {
				return err
			}
			if int64(len(data)) > r.conn.cfg.Limits.MaxMessageBytes-metadataBytes {
				return wire.ErrMessageTooLarge
			}
			metadataBytes += int64(len(data))
			if r.columns[i], err = r.conn.decodeColumn(data); err != nil {
				return err
			}
			if textTypeName(r.columns[i], r.conn.session.mode) == "" {
				return &UnsupportedTypeError{Type: r.columns[i].Type, Mode: r.conn.session.mode}
			}
		}
		if r.conn.session.capabilities&wire.ClientDeprecateEOF == 0 {
			data, err = r.conn.session.packets.ReadMessage(ctx)
			if err != nil {
				return err
			}
			if _, err = wire.DecodeEOF(data); err != nil {
				return err
			}
		}
		r.done = false
		r.more = false
		return nil
	}
}

func (r *textRows) Next(dest []driver.Value) error {
	if r.err != nil {
		return r.err
	}
	if r.done {
		return io.EOF
	}
	if len(dest) != len(r.columns) {
		return r.fail(wire.ErrMalformed, true)
	}
	return r.next(r.ctx, dest)
}

func (r *textRows) next(ctx context.Context, dest []driver.Value) error {
	data, err := r.conn.session.packets.ReadMessage(ctx)
	if err != nil {
		return r.fail(err, true)
	}
	if len(data) == 0 {
		return r.fail(wire.ErrMalformed, true)
	}
	if data[0] == 255 {
		err = decodeServerError(data)
		var server *Error
		return r.fail(err, !errors.As(err, &server))
	}
	if data[0] == 254 && (len(data) < 9 || (r.conn.session.capabilities&wire.ClientDeprecateEOF != 0 && len(data) < 0xffffff)) {
		var ok wire.OK
		if r.conn.session.capabilities&wire.ClientDeprecateEOF != 0 {
			ok, err = wire.DecodeOK(data, r.conn.session.capabilities)
		} else {
			ok, err = wire.DecodeEOF(data)
		}
		if err != nil {
			return r.fail(err, true)
		}
		r.conn.session.ok = ok
		r.done = true
		r.execution.rowDone()
		r.more = ok.Status&8 != 0
		if !r.more {
			r.release()
		}
		if r.err != nil {
			return r.err
		}
		return io.EOF
	}
	var fields [][]byte
	if r.binary {
		fields, err = wire.DecodeBinaryRow(data, r.columns)
	} else {
		fields, err = wire.DecodeTextRow(data, len(r.columns))
	}
	if err != nil {
		return r.fail(err, true)
	}
	if dest != nil {
		for i, field := range fields {
			if typ := r.columns[i].Type; typ >= 249 && typ <= 252 && int64(len(field)) > r.conn.cfg.Limits.MaxLOBBytes {
				return r.fail(wire.ErrMessageTooLarge, true)
			}
			dest[i], err = r.conn.decodeRowValue(r.columns[i], r.binary, field)
			if err != nil {
				return r.fail(err, true)
			}
		}
	}
	return nil
}

func (r *textRows) drainCurrent(ctx context.Context) error {
	for !r.done {
		if err := r.next(ctx, nil); err != nil && err != io.EOF {
			return err
		}
	}
	return nil
}

func (r *textRows) drainAll(ctx context.Context) error {
	if r.err != nil {
		return r.err
	}
	if r.released {
		return r.err
	}
	for {
		if err := r.drainCurrent(ctx); err != nil {
			return err
		}
		if !r.more {
			r.release()
			return r.err
		}
		if err := r.advance(ctx); err != nil {
			return r.fail(err, true)
		}
		if r.released {
			return r.err
		}
	}
}

func (r *textRows) Close() error {
	if r.released {
		return r.err
	}
	if err := r.contextError(); err != nil {
		return r.fail(err, true)
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.conn.cfg.Limits.CleanupTimeout)
	defer cancel()
	return r.drainAll(ctx)
}

func (r *textRows) HasNextResultSet() bool { return !r.released && (r.more || !r.done) }
func (r *textRows) NextResultSet() error {
	if r.err != nil {
		return r.err
	}
	if r.released {
		return io.EOF
	}
	if err := r.drainCurrent(r.ctx); err != nil {
		return err
	}
	if !r.more {
		return io.EOF
	}
	if err := r.advance(r.ctx); err != nil {
		return r.fail(err, true)
	}
	if r.released {
		return io.EOF
	}
	return nil
}

func (r *textRows) ColumnTypeDatabaseTypeName(index int) string {
	return textTypeName(r.columns[index], r.conn.session.mode)
}
func (r *textRows) ColumnTypeNullable(index int) (bool, bool) {
	return r.columns[index].Flags&1 == 0, true
}
func (r *textRows) ColumnTypeScanType(index int) reflect.Type {
	c := r.columns[index]
	if r.conn.cfg.Session.ParseTime && (c.Type == 7 || c.Type == 10 || c.Type == 12 || c.Type == 14 || c.Type == 202 && r.conn.session.mode == ModeOracle) {
		return reflect.TypeFor[time.Time]()
	}
	if c.Charset == 63 && (c.Type == 15 || c.Type >= 249) || c.Type == 16 || c.Type == 203 {
		return reflect.TypeFor[[]byte]()
	}
	switch c.Type {
	case 1, 2, 8, 9, 13:
		return reflect.TypeFor[int64]()
	case 3:
		if r.conn.session.mode != ModeOracle {
			return reflect.TypeFor[int64]()
		}
	case 4, 5:
		return reflect.TypeFor[float64]()
	}
	return reflect.TypeFor[string]()
}

var (
	_ driver.Rows                           = (*textRows)(nil)
	_ driver.RowsNextResultSet              = (*textRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*textRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*textRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*textRows)(nil)
)
