package call

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
)

// MySQLSQL builds private session-variable initialization and rewritten call SQL.
func (s *State) MySQLSQL(op *session.Operation, plan sqltext.Plan, values []any) ([]byte, []byte, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, err
	}
	prefix := "@_obgo_" + hex.EncodeToString(nonce[:]) + "_"
	indices := make(map[*Binding]int, len(s.bindings))
	s.vars = make([]string, len(s.bindings))
	assignments := make([]string, len(s.bindings))
	cfg, p := op.Config(), op.Protocol()
	for i, b := range s.bindings {
		indices[b] = i
		s.vars[i] = prefix + strconv.Itoa(i)
		if b.In {
			if err := ValidateValue(op, b, b.input); err != nil {
				return nil, nil, err
			}
		}
		literal, err := valuecodec.ClientLiteral(b.input, p.Mode, cfg.Session, cfg.Limits.MaxLOBBytes, cfg.Limits.MaxMessageBytes-1)
		if err != nil {
			return nil, nil, err
		}
		assignments[i] = s.vars[i] + "=" + literal
	}
	initial, err := command.EncodeSQL("SET "+strings.Join(assignments, ","), cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1)
	if err != nil {
		return nil, nil, err
	}
	if _, err = command.EncodeSQL(s.clearSQL(), cfg.Session.Charset, cfg.Limits.MaxMessageBytes-1); err != nil {
		return nil, nil, err
	}
	for i, value := range values {
		if b, ok := value.(*Binding); ok {
			values[i] = valuecodec.OutputVariable(s.vars[indices[b]])
		}
		if source, ok := value.(*streaminput.Source); ok {
			if _, err = statement.LongDataParameter(op, source); err != nil {
				return nil, nil, err
			}
			s.longArgs = append(s.longArgs, driver.NamedValue{Ordinal: len(s.longArgs) + 1, Value: source})
			values[i] = valuecodec.LongPlaceholder{}
		}
	}
	query, err := command.InterpolatePlan(op, plan, values)
	return initial, query, err
}

func (s *State) clearSQL() string {
	assignments := make([]string, len(s.vars))
	for i, variable := range s.vars {
		assignments[i] = variable + "=NULL"
	}
	return "SET " + strings.Join(assignments, ",")
}

func (s *State) readMySQLOutputs(ctx context.Context, op *session.Operation) error {
	r, err := command.Start(ctx, op, []byte("\x03SELECT "+strings.Join(s.vars, ",")), false)
	if err != nil {
		return err
	}
	if _, err = r.Advance(ctx); err != nil {
		return err
	}
	for !r.Done() {
		values, err := r.NextValues(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s.Capture(values, false)
	}
	return errors.Join(s.err, r.Drain(ctx))
}
