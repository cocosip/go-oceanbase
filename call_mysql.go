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
)

type outputVariable string

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
	}
	query, err := c.interpolatePlan(plan, values)
	return initial, []byte(query), err
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
