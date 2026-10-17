package oceanbase

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"strconv"

	"github.com/cocosip/go-oceanbase/internal/wire"
)

const (
	xaSchema    = "SYS"
	xaXIDType   = "DBMS_XA_XID"
	xaArrayType = "DBMS_XA_XID_ARRAY"
)

func (c *conn) mysqlXALocked(ctx context.Context, r *xaRequest) error {
	if r.op == xaRecover {
		var err error
		r.xids, err = c.mysqlXARecoverLocked(ctx)
		if err != nil {
			return c.xaFailure(r, err, false)
		}
		return err
	}
	verbs := [...]string{"START", "END", "PREPARE", "COMMIT", "ROLLBACK"}
	query := "XA " + verbs[r.op] + " " + mysqlXID(r.xid)
	switch {
	case r.op == xaStart && r.flags == XAJoin:
		query += " JOIN"
	case r.op == xaStart && r.flags == XAResume:
		query += " RESUME"
	case r.op == xaEnd && r.flags == XASuspend:
		query += " SUSPEND"
	case r.op == xaCommit && r.onePhase:
		query += " ONE PHASE"
	}
	p := c.session.packets
	p.ResetSequence()
	if err := p.WriteMessage(ctx, []byte("\x03"+query)); err != nil {
		return c.xaFailure(r, err, !errorsPreSend(err))
	}
	if err := c.readCommandOK(ctx); err != nil {
		return c.xaFailure(r, err, true)
	}
	r.vote = XAPrepared
	return nil
}

func xaInteger(v driver.Value) (int64, error) {
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
	format, e1 := xaInteger(values[0])
	g, e2 := xaInteger(values[1])
	b, e3 := xaInteger(values[2])
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
	return cloneXID(XID{FormatID: int32(format), GlobalID: data[:int(g)], BranchID: data[int(g):]}), nil
}

func (c *conn) mysqlXARecoverLocked(ctx context.Context) ([]XID, error) {
	r, err := c.queryCommandOwnedLocked(ctx, []byte("\x03XA RECOVER"), false, nil, true)
	if err != nil {
		return nil, err
	}
	if len(r.columns) != 4 || r.items != 1 {
		c.discard()
		return nil, r.fail(wire.ErrMalformed, true)
	}
	var out []XID
	var size int64
	values := make([]driver.Value, 4)
	for {
		err = r.Next(values)
		if err == io.EOF {
			if r.more {
				return nil, r.fail(wire.ErrMalformed, true)
			}
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		x, err := decodeRecoveredXID(values)
		if err != nil {
			return nil, r.fail(err, true)
		}
		n := int64(len(x.GlobalID) + len(x.BranchID) + 4)
		if len(out) >= c.cfg.Limits.MaxResultItems || n > c.cfg.Limits.MaxMessageBytes-size {
			return nil, r.fail(wire.ErrMessageTooLarge, true)
		}
		size += n
		out = append(out, x)
	}
}

func nativeXID(x XID) wire.Parameter {
	meta := complexLenBytes(nil, []byte(xaSchema))
	meta = complexLenBytes(meta, []byte(xaXIDType))
	meta = complexLenEnc(meta, 0)
	data := complexLenBytes([]byte{0}, []byte(strconv.FormatInt(int64(x.FormatID), 10)))
	data = complexLenBytes(data, x.GlobalID)
	data = complexLenBytes(data, x.BranchID)
	return wire.Parameter{Type: 160, TypeMetadata: meta, Data: data}
}

func (c *conn) oracleXALocked(ctx context.Context, r *xaRequest) error {
	if r.op == xaRecover {
		s, err := c.callInternalLocked(ctx, "DECLARE x SYS.DBMS_XA_XID_ARRAY := SYS.DBMS_XA_XID_ARRAY(); BEGIN x := SYS.DBMS_XA.XA_RECOVER(); ? := x; END;", []wire.Parameter{{Type: 6}}, 1)
		if err != nil {
			if s.seen || len(s.complexRows) != 0 {
				return c.xaUnknown(r, err)
			}
			return c.xaFailure(r, err, s.started)
		}
		if s.resultItems != 2 || !xaRecoveryColumn(s.columns) {
			c.discard()
			return wire.ErrMalformed
		}
		r.xids, err = decodeOracleRecovery(s.values, c.cfg.Limits)
		if err != nil {
			c.discard()
		}
		return err
	}
	// Oracle association requires autocommit off; restore only after confirmed END.
	if r.op == xaStart && c.session.ok.Status&2 != 0 {
		if err := c.commandOK(ctx, []byte("\x03SET autocommit=0")); err != nil {
			c.discard()
			return err
		}
	}
	verbs := [...]string{"XA_START", "XA_END", "XA_PREPARE", "XA_COMMIT", "XA_ROLLBACK"}
	params := []wire.Parameter{{Type: 6}, nativeXID(r.xid)}
	args := "?"
	switch r.op {
	case xaStart, xaEnd:
		flags := r.flags
		if flags == XAFail {
			flags = XASuccess
		}
		params, args = append(params, lobInteger(int64(flags))), "?, ?"
	case xaCommit:
		flag := byte(0)
		if r.onePhase {
			flag = 1
		}
		params, args = append(params, wire.Parameter{Type: 1, Data: []byte{flag}}), "?, ?"
	}
	s, err := c.callInternalLocked(ctx, "BEGIN ? := SYS.DBMS_XA."+verbs[r.op]+"("+args+"); END;", params, 1)
	if err != nil {
		if s.seen {
			return c.xaUnknown(r, err)
		}
		return c.xaFailure(r, err, s.started)
	}
	if s.resultItems != 2 || len(s.values) != 1 {
		return c.xaFailure(r, wire.ErrMalformed, true)
	}
	code, err := xaInteger(s.values[0])
	if err != nil || code < -2147483648 || code > 2147483647 {
		return c.xaFailure(r, wire.ErrMalformed, true)
	}
	if code != 0 && (r.op != xaPrepare || code != 3) {
		// No association guarantee follows an RM failure; preserve its code and discard.
		c.discard()
		return &XAError{Code: int32(code), Err: fmt.Errorf("DBMS_XA.%s", verbs[r.op])}
	}
	r.vote = XAVote(code)
	if r.op == xaEnd {
		if err = c.restoreAutocommit(ctx); err != nil {
			c.discard()
			return fmt.Errorf("%w: XA END confirmed; autocommit restoration failed: %w", ErrTxCleanup, err)
		}
	}
	return nil
}

func xaRecoveryColumn(columns []wire.Column) bool {
	if len(columns) != 1 || columns[0].Type != 160 {
		return false
	}
	c := columns[0]
	return c.ComplexSchema == xaSchema && c.ComplexName == xaArrayType || c.ComplexName == "" && c.ElementType == 160 && c.ElementSchema == xaSchema && c.ElementName == xaXIDType
}

func decodeOracleRecovery(values []driver.Value, limits Limits) ([]XID, error) {
	if len(values) != 1 {
		return nil, wire.ErrMalformed
	}
	var a Array
	if err := a.Scan(values[0]); err != nil {
		return nil, wire.ErrMalformed
	}
	validType := a.Schema == xaSchema && a.Type == xaArrayType || a.Type == "" && a.ElementType == xaXIDType
	if a.Null || !validType || len(a.Elements) > limits.MaxResultItems {
		return nil, wire.ErrMalformed
	}
	var out []XID
	var size int64
	for _, elem := range a.Elements {
		o, ok := elem.(Object)
		if !ok || o.Null || o.Schema != xaSchema || o.Type != xaXIDType || len(o.Fields) != 3 || o.Fields[0].Type != typeNameNumber || o.Fields[1].Type != typeNameRaw || o.Fields[2].Type != typeNameRaw {
			return nil, wire.ErrMalformed
		}
		f, ok := o.Fields[0].Value.(Number)
		g, gok := o.Fields[1].Value.(Raw)
		b, bok := o.Fields[2].Value.(Raw)
		// Oracle RAW may represent a zero-length branch qualifier as SQL NULL.
		bok = bok || o.Fields[2].Value == nil
		format, err := strconv.ParseInt(string(f), 10, 32)
		x := XID{FormatID: int32(format), GlobalID: []byte(g), BranchID: []byte(b)}
		if !ok || !gok || !bok || err != nil || !validXID(x) {
			return nil, wire.ErrMalformed
		}
		n := int64(len(g) + len(b) + 4)
		if n > limits.MaxMessageBytes-size {
			return nil, wire.ErrMessageTooLarge
		}
		size += n
		out = append(out, cloneXID(x))
	}
	return out, nil
}
