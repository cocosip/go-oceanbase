package oceanbase

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type stmt struct {
	conn   *conn
	id     uint32
	plan   bindPlan
	closed bool // guarded by the owning connection's gate
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.cfg.PrepareMode == PrepareClient {
		return c.prepareClient(ctx, query)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) {
		return nil, fmt.Errorf("%w: invalid SQL text", ErrBind)
	}
	plan := bindPlan{query: query, sql: query, mode: c.session.mode}
	if plan.mode == ModeOracle {
		var err error
		plan, err = buildBindPlan(query, plan.mode)
		if err != nil {
			return nil, err
		}
	}
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	if c.closed.Load() {
		c.unlock()
		return nil, driver.ErrBadConn
	}
	r := c.startRows(ctx, false, nil)
	ctx = r.ctx
	defer r.release()
	p := c.session.packets
	p.ResetSequence()
	if err := p.WriteMessage(ctx, append([]byte{22}, plan.sql...)); err != nil {
		return nil, r.fail(err, !errorsPreSend(err))
	}
	data, err := p.ReadMessage(ctx)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if len(data) > 0 && data[0] == 255 {
		err = decodeServerError(data)
		return nil, r.fail(err, err == wire.ErrMalformed)
	}
	header, err := wire.DecodePrepareOK(data)
	if err != nil {
		return nil, r.fail(err, true)
	}
	if header.Columns > maxTextColumns {
		return nil, r.fail(wire.ErrMessageTooLarge, true)
	}
	budget := int64(len(data))
	if err = c.prepareMetadata(ctx, int(header.Parameters), &budget); err == nil {
		err = c.prepareMetadata(ctx, int(header.Columns), &budget)
	}
	if err != nil {
		return nil, r.fail(err, true)
	}
	if plan.mode == ModeMySQL {
		// Native server metadata is authoritative, including session quoting and executable comments.
		plan.numInput = int(header.Parameters)
		plan.slots = make([]bindSlot, plan.numInput)
		for i := range plan.slots {
			plan.slots[i].index = i
		}
	} else if int(header.Parameters) != len(plan.slots) {
		return nil, r.fail(fmt.Errorf("%w: server parameter count", ErrBind), true)
	}
	prepared := &stmt{conn: c, id: header.ID, plan: plan}
	err = ctx.Err()
	r.release()
	if err == nil {
		err = r.err
	}
	if err != nil {
		_ = prepared.Close()
		return nil, err
	}
	return prepared, nil
}

func (c *conn) prepareMetadata(ctx context.Context, count int, budget *int64) error {
	for i := 0; i < count; i++ {
		data, err := c.session.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		if int64(len(data)) > c.cfg.Limits.MaxMessageBytes-*budget {
			return wire.ErrMessageTooLarge
		}
		*budget += int64(len(data))
		if _, err = wire.DecodeColumn(data); err != nil {
			return err
		}
	}
	if count > 0 && c.session.capabilities&wire.ClientDeprecateEOF == 0 {
		data, err := c.session.packets.ReadMessage(ctx)
		if err != nil {
			return err
		}
		eof, err := wire.DecodeEOF(data)
		if err != nil {
			return err
		}
		c.session.ok.Status, c.session.ok.Warnings = eof.Status, eof.Warnings
	}
	return nil
}

func (s *stmt) NumInput() int { return s.plan.numInput }
func (s *stmt) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), s.conn.cfg.Limits.CleanupTimeout)
	defer cancel()
	if err := s.conn.lock(ctx); err != nil {
		s.conn.discard()
		return err
	}
	defer s.conn.unlock()
	return s.closeLocked(ctx)
}
func (s *stmt) closeLocked(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.conn.closed.Load() {
		return nil
	}
	s.conn.session.packets.ResetSequence()
	err := s.conn.session.packets.WriteMessage(ctx, binary.LittleEndian.AppendUint32([]byte{25}, s.id))
	if err != nil {
		s.conn.discard()
	}
	return err
}
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), namedValues(args))
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), namedValues(args))
}
func namedValues(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for i, arg := range args {
		values[i] = driver.NamedValue{Ordinal: i + 1, Value: arg}
	}
	return values
}
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.exec(ctx, args, false)
}
func (s *stmt) exec(ctx context.Context, args []driver.NamedValue, auto bool) (driver.Result, error) {
	rows, err := s.query(ctx, args, auto)
	if err != nil {
		return nil, err
	}
	if err = rows.drainAll(ctx); err != nil {
		return nil, err
	}
	return rows.last, nil
}
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.query(ctx, args, false)
}
func (s *stmt) query(ctx context.Context, args []driver.NamedValue, auto bool) (*textRows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := bindArguments(s.plan, args)
	if err != nil {
		return nil, err
	}
	params := make([]wire.Parameter, len(values))
	budget := s.conn.cfg.Limits.MaxMessageBytes - 10
	if len(values) > 0 {
		budget -= int64((len(values)+7)/8 + 1 + 2*len(values))
	}
	if budget < 0 {
		return nil, wire.ErrMessageTooLarge
	}
	for i, v := range values {
		params[i], err = encodeParameter(v.value, s.plan.mode, s.conn.cfg.Session.Location, budget)
		if err != nil {
			return nil, err
		}
		if params[i].EncodedSize() > budget {
			return nil, wire.ErrMessageTooLarge
		}
		budget -= params[i].EncodedSize()
	}
	payload, err := wire.EncodeStmtExecute(s.id, params, s.conn.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	if err = s.conn.lock(ctx); err != nil {
		return nil, err
	}
	if s.closed {
		s.conn.unlock()
		return nil, fmt.Errorf("oceanbase: statement closed")
	}
	if s.conn.closed.Load() {
		s.conn.unlock()
		return nil, driver.ErrBadConn
	}
	var owned *stmt
	if auto {
		owned = s
	}
	return s.conn.queryCommandLocked(ctx, payload, true, owned)
}

var (
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.Stmt               = (*stmt)(nil)
	_ driver.StmtExecContext    = (*stmt)(nil)
	_ driver.StmtQueryContext   = (*stmt)(nil)
)
