package oceanbase

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

type outputVariable string
type longPlaceholder struct{}

func (c *conn) mysqlOutputSQL(plan bindPlan, values []boundValue, s *outputState) ([]byte, []byte, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, err
	}
	prefix := "@_obgo_" + hex.EncodeToString(nonce[:]) + "_"
	indices := make(map[*outBinding]int, len(s.bindings))
	s.vars = make([]string, len(s.bindings))
	assignments := make([]string, len(s.bindings))
	for i, b := range s.bindings {
		indices[b] = i
		s.vars[i] = prefix + strconv.Itoa(i)
		if b.In {
			if err := c.validateOutputValue(b, b.input); err != nil {
				return nil, nil, err
			}
		}
		literal, err := c.clientLiteral(b.input, c.cfg.Limits.MaxMessageBytes-1)
		if err != nil {
			return nil, nil, err
		}
		assignments[i] = s.vars[i] + "=" + literal
	}
	initial, err := c.encodeSQL("SET " + strings.Join(assignments, ","))
	if err != nil {
		return nil, nil, err
	}
	if _, err = c.encodeSQL(s.clearSQL()); err != nil {
		return nil, nil, err
	}
	for i := range values {
		if b, ok := values[i].value.(*outBinding); ok {
			values[i].value = outputVariable(s.vars[indices[b]])
		}
		if v, ok := values[i].value.(*LongData); ok {
			if _, err = c.longDataParameter(v); err != nil {
				return nil, nil, err
			}
			s.longArgs = append(s.longArgs, driver.NamedValue{Ordinal: len(s.longArgs) + 1, Value: v})
			values[i].value = longPlaceholder{}
		}
	}
	query, err := c.interpolatePlan(plan, values)
	return initial, []byte(query), err
}

func (c *conn) queryMySQLLongOutputsLocked(ctx context.Context, query []byte, outputs *outputState) (_ *textRows, err error) {
	owned := true
	var prepared *stmt
	defer func() {
		if owned {
			cleanup, cancel := context.WithTimeout(context.Background(), c.cfg.Limits.CleanupTimeout)
			defer cancel()
			if prepared != nil {
				err = errors.Join(err, prepared.closeLocked(cleanup))
			}
			err = errors.Join(err, outputs.finishLocked(cleanup, c, false))
			c.unlock()
		}
	}()
	text, err := charset.Decode(c.cfg.Session.Charset, query)
	if err != nil {
		return nil, err
	}
	prepared, err = c.prepareServerLocked(ctx, text)
	if err != nil {
		return nil, err
	}
	params, err := c.cursorParametersLocked(ctx, prepared.plan, outputs.longArgs)
	if err != nil {
		return nil, err
	}
	payload, err := wire.EncodeStmtExecute(prepared.id, params, c.cfg.Limits.MaxMessageBytes)
	if err != nil {
		return nil, err
	}
	values, err := bindArguments(prepared.plan, outputs.longArgs)
	if err != nil {
		return nil, err
	}
	if err = c.uploadLongDataLocked(ctx, prepared.id, values); err != nil {
		return nil, err
	}
	owned = false
	return c.queryCommandStateLocked(ctx, payload, true, prepared, false, outputs)
}

func (s *outputState) clearSQL() string {
	assignments := make([]string, len(s.vars))
	for i, variable := range s.vars {
		assignments[i] = variable + "=NULL"
	}
	return "SET " + strings.Join(assignments, ",")
}

// outputCommandLocked consumes auxiliary commands without publishing execution results.
func (c *conn) outputCommandLocked(ctx context.Context, query []byte) error {
	r, err := c.queryCommandOwnedLocked(ctx, append([]byte{3}, query...), false, nil, true)
	if err != nil {
		return err
	}
	return r.drainAll(ctx)
}

func (s *outputState) readMySQLOutputs(ctx context.Context, c *conn) error {
	r, err := c.queryCommandOwnedLocked(ctx, []byte("\x03SELECT "+strings.Join(s.vars, ",")), false, nil, true)
	if err != nil {
		return err
	}
	values := make([]driver.Value, len(r.columns))
	for !r.done {
		if err = r.next(ctx, values); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		s.capture(values, false)
	}
	return errors.Join(s.err, r.drainAll(ctx))
}
