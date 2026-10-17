package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

type outputState struct {
	temporary      bool
	longArgs       []driver.NamedValue
	parent         *stmt
	complexColumns []wire.Column
	complexRows    [][]byte
	complexBytes   int64
	internal       bool
	started        bool // native command may have reached the server
	resultItems    int
	columns        []wire.Column
	bindings       []*outBinding
	physical       []int
	values         []driver.Value
	seen           bool
	err            error
	vars           []string
}

func hasOutputArguments(args []driver.NamedValue) bool {
	for _, arg := range args {
		switch arg.Value.(type) {
		case sql.Out, Out, CursorOut, *outBinding:
			return true
		}
	}
	return false
}

func (c *conn) queryOutputs(ctx context.Context, query string, args []driver.NamedValue, prepared *stmt) (*textRows, error) {
	if err := c.lock(ctx); err != nil {
		return nil, err
	}
	return c.queryOutputsLocked(ctx, query, args, prepared)
}

// queryOutputsLocked transfers the gate to the returned Rows, including failure cleanup.
func (c *conn) queryOutputsLocked(ctx context.Context, query string, args []driver.NamedValue, prepared *stmt) (*textRows, error) {
	c.clearDescriptors()
	owned := true
	defer func() {
		if owned {
			c.unlock()
		}
	}()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if prepared != nil {
		if err := prepared.checkOpenLocked(); err != nil {
			return nil, err
		}
	}
	if err := c.clearStatementsLocked(ctx); err != nil {
		return nil, err
	}
	if int64(len(query)) >= c.cfg.Limits.MaxMessageBytes {
		return nil, wire.ErrMessageTooLarge
	}
	plan, err := buildBindPlanEscapes(query, c.session.mode, c.session.ok.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	values, err := bindArguments(plan, args)
	if err != nil {
		return nil, err
	}
	outputs, err := c.outputBindings(plan, values)
	if err != nil {
		return nil, err
	}
	for _, b := range outputs.bindings {
		if b.resource != 0 {
			e, _ := ctx.Value(executionContextKey{}).(*Execution)
			if e == nil || !validSession(e.session) {
				return nil, ErrOut
			}
		}
	}
	if c.session.mode == ModeOracle {
		for _, value := range values {
			if v, ok := value.value.(*LongData); ok {
				if _, err = c.longDataParameter(v); err != nil {
					return nil, err
				}
			}
		}
		if longValues(values) && prepared == nil {
			prepared, err = c.prepareServerLocked(ctx, plan.query)
			if err != nil {
				return nil, err
			}
			outputs.temporary = true
			defer func() {
				if owned {
					cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
					defer cancel()
					_ = prepared.closeLocked(cleanup)
				}
			}()
		}
		payload, err := c.outputPrepareExecute(ctx, plan, values, prepared)
		if err != nil {
			return nil, err
		}
		if longValues(values) {
			if err = c.uploadLongDataLocked(ctx, prepared.id, values); err != nil {
				return nil, err
			}
		}
		owned = false
		return c.queryPrepareExecuteLocked(ctx, payload, plan, outputs, prepared)
	}
	initial, queryBytes, err := c.mysqlOutputSQL(plan, values, outputs)
	if err != nil {
		return nil, err
	}
	if err = c.outputCommandLocked(ctx, initial); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
		defer cancel()
		return nil, errors.Join(err, outputs.finishLocked(cleanup, c, false))
	}
	owned = false
	if len(outputs.longArgs) > 0 {
		return c.queryMySQLLongOutputsLocked(ctx, queryBytes, outputs)
	}
	return c.queryCommandStateLocked(ctx, append([]byte{3}, queryBytes...), false, nil, false, outputs, nil)
}

func (c *conn) outputBindings(plan bindPlan, values []boundValue) (*outputState, error) {
	s := &outputState{}
	indices := make(map[*outBinding]int)
	destinations := make(map[any]bool)
	for i, value := range values {
		b, ok := value.value.(*outBinding)
		if !ok {
			continue
		}
		index, exists := indices[b]
		if !exists {
			if destinations[b.Dest] {
				return nil, fmt.Errorf("%w: distinct output arguments alias the same destination", ErrOut)
			}
			if err := b.validateMode(plan.mode); err != nil {
				return nil, err
			}
			b.parameter = plan.slots[i].index + 1
			index = len(s.bindings)
			indices[b] = index
			destinations[b.Dest] = true
			s.bindings = append(s.bindings, b)
		}
		s.physical = append(s.physical, index)
	}
	return s, nil
}

func (s *outputState) capture(values []driver.Value, physical bool) {
	if s.seen || len(values) != len(s.bindings) && !physical || physical && len(values) != len(s.physical) {
		s.err = fmt.Errorf("%w: output row cardinality or column count", ErrOut)
		return
	}
	s.seen = true
	s.values = make([]driver.Value, len(s.bindings))
	assigned := make([]bool, len(s.bindings))
	for i, value := range values {
		index := i
		if physical {
			index = s.physical[i]
		}
		if assigned[index] && !reflect.DeepEqual(s.values[index], value) {
			s.err = fmt.Errorf("%w: repeated output binding returned conflicting values", ErrOut)
			return
		}
		if data, ok := value.([]byte); ok {
			value = slices.Clone(data)
		}
		s.values[index] = value
		assigned[index] = true
	}
}

func (s *outputState) finishLocked(ctx context.Context, c *conn, success bool) error {
	if len(s.vars) != 0 && !c.closed.Load() {
		if success {
			s.err = errors.Join(s.err, s.readMySQLOutputs(ctx, c))
		}
		if err := c.outputCommandLocked(ctx, []byte(s.clearSQL())); err != nil {
			c.discard()
			return errors.Join(s.err, err)
		}
	}
	if !success {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	if !s.seen {
		return fmt.Errorf("%w: output response missing", ErrOut)
	}
	return nil
}

func (r *textRows) readOutputs(ctx context.Context) error {
	for _, column := range r.columns {
		if column.Type == 160 {
			return r.readComplexOutputs(ctx)
		}
	}
	for !r.done {
		values := make([]driver.Value, len(r.columns))
		if err := r.next(ctx, values); err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
		r.outputs.capture(values, true)
		if r.outputs.seen && r.outputs.err == nil {
			r.outputs.columns = make([]wire.Column, len(r.outputs.bindings))
			for i, column := range r.columns {
				r.outputs.columns[r.outputs.physical[i]] = column
			}
		}
	}
	r.hiddenOut = false
	return nil
}

func (r *textRows) readComplexOutputs(ctx context.Context) error {
	if !r.binary {
		return ErrUnsupported
	}
	if r.outputs.complexColumns != nil {
		r.outputs.err = ErrOut
	}
	r.outputs.complexColumns = r.columns
	for !r.done {
		data, err := r.rowPacket(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		r.outputs.complexBytes += int64(len(data)) + 24
		if r.outputs.complexBytes > r.conn.cfg.Limits.MaxMessageBytes {
			return r.fail(wire.ErrMessageTooLarge, true)
		}
		r.outputs.complexRows = append(r.outputs.complexRows, data)
	}
	r.hiddenOut = false
	return nil
}

func (s *outputState) decodeComplexLocked(ctx context.Context, c *conn) error {
	if len(s.complexRows) == 0 {
		return nil
	}
	if err := c.describeComplexColumnsLocked(ctx, s.complexColumns); err != nil {
		return err
	}
	r := &textRows{conn: c, ctx: ctx, columns: s.complexColumns, binary: true, captureLOB: true, keepGate: true}
	for _, raw := range s.complexRows {
		values := make([]driver.Value, len(s.complexColumns))
		if err := r.decodeRow(raw, values); err != nil {
			return err
		}
		if err := c.materializeComplexValuesLocked(ctx, s.complexColumns, values); err != nil {
			return err
		}
		s.capture(values, true)
		if s.err != nil {
			return s.err
		}
	}
	s.columns = make([]wire.Column, len(s.bindings))
	for i, column := range s.complexColumns {
		s.columns[s.physical[i]] = column
	}
	s.complexRows = nil
	return nil
}
