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
	bindings []*outBinding
	physical []int
	values   []driver.Value
	seen     bool
	err      error
	vars     []string
}

func hasOutputArguments(args []driver.NamedValue) bool {
	for _, arg := range args {
		switch arg.Value.(type) {
		case sql.Out, Out, *outBinding:
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
	owned := true
	defer func() {
		if owned {
			c.unlock()
		}
	}()
	if c.closed.Load() {
		return nil, driver.ErrBadConn
	}
	if prepared != nil && (prepared.closed || prepared.closeRequested) {
		return nil, fmt.Errorf("oceanbase: statement closed")
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
	if c.session.mode == ModeOracle {
		payload, err := c.outputPrepareExecute(plan, values, prepared)
		if err != nil {
			return nil, err
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
	return c.queryCommandStateLocked(ctx, append([]byte{3}, queryBytes...), false, nil, false, outputs)
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
	for !r.done {
		values := make([]driver.Value, len(r.columns))
		if err := r.next(ctx, values); err != nil {
			if err != io.EOF {
				return err
			}
			break
		}
		r.outputs.capture(values, true)
	}
	r.hiddenOut = false
	return nil
}
