package xa

import (
	"context"
	"database/sql/driver"
	"io"
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

func mysql(ctx context.Context, op *session.Operation, r *Request) error {
	if r.Op == Recover {
		var err error
		r.XIDs, err = mysqlRecover(ctx, op)
		if err != nil {
			return failure(op, r, err, false)
		}
		return nil
	}
	verbs := [...]string{"START", "END", "PREPARE", "COMMIT", "ROLLBACK"}
	query := "XA " + verbs[r.Op] + " " + mysqlXID(r.XID)
	switch {
	case r.Op == Start && r.Flags == Join:
		query += " JOIN"
	case r.Op == Start && r.Flags == Resume:
		query += " RESUME"
	case r.Op == End && r.Flags == Suspend:
		query += " SUSPEND"
	case r.Op == Commit && r.OnePhase:
		query += " ONE PHASE"
	}
	if _, err := command.Start(ctx, op, []byte("\x03"+query), false); err != nil {
		return failure(op, r, err, !command.PreSend(err))
	}
	if _, err := command.ReadOK(ctx, op); err != nil {
		return failure(op, r, err, true)
	}
	r.Vote = Prepared
	return nil
}

func integer(v driver.Value) (int64, error) {
	var text string
	switch n := v.(type) {
	case int64:
		return n, nil
	case string:
		text = n
	case []byte:
		text = string(n)
	default:
		return 0, wire.ErrMalformed
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, wire.ErrMalformed
	}
	return n, nil
}

func decodeRecoveredXID(values []driver.Value) (XID, error) {
	if len(values) != 4 {
		return XID{}, wire.ErrMalformed
	}
	format, e1 := integer(values[0])
	g, e2 := integer(values[1])
	b, e3 := integer(values[2])
	var data []byte
	switch v := values[3].(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return XID{}, wire.ErrMalformed
	}
	if e1 != nil || e2 != nil || e3 != nil || format < -2147483648 || format > 2147483647 || format == -1 || g < 1 || g > 64 || b < 0 || b > 64 || g+b != int64(len(data)) {
		return XID{}, wire.ErrMalformed
	}
	return CloneXID(XID{FormatID: int32(format), GlobalID: data[:int(g)], BranchID: data[int(g):]}), nil
}

func mysqlRecover(ctx context.Context, op *session.Operation) ([]XID, error) {
	r, err := command.Start(ctx, op, []byte("\x03XA RECOVER"), false)
	if err != nil {
		return nil, err
	}
	if _, err = r.Advance(ctx); err != nil {
		return nil, err
	}
	fail := func(cause error) ([]XID, error) { op.Discard(); op.EndResponse(); return nil, cause }
	if len(r.Columns()) != 4 || r.Items() != 1 {
		return fail(wire.ErrMalformed)
	}
	var out []XID
	var size int64
	for {
		values, err := r.NextValues(ctx)
		if err == io.EOF {
			if r.More() {
				return fail(wire.ErrMalformed)
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		x, err := decodeRecoveredXID(values)
		if err != nil {
			return fail(err)
		}
		n := int64(len(x.GlobalID) + len(x.BranchID) + 4)
		cfg := op.Config()
		if len(out) >= cfg.Limits.MaxResultItems || n > cfg.Limits.MaxMessageBytes-size {
			return fail(wire.ErrMessageTooLarge)
		}
		size += n
		out = append(out, x)
	}
}
