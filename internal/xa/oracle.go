package xa

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/catalog"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/materialize"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/statement"
	"github.com/cocosip/go-oceanbase/internal/valueformat"
	"github.com/cocosip/go-oceanbase/internal/wire"
	"github.com/cocosip/go-oceanbase/types"
)

const (
	xaSchema    = "SYS"
	xaXIDType   = "DBMS_XA_XID"
	xaArrayType = "DBMS_XA_XID_ARRAY"
)

func nativeXID(x XID) wire.Parameter {
	meta := wire.AppendComplexLenBytes(nil, []byte(xaSchema))
	meta = wire.AppendComplexLenBytes(meta, []byte(xaXIDType))
	meta = wire.AppendComplexLenEnc(meta, 0)
	data := wire.AppendComplexLenBytes([]byte{0}, []byte(strconv.FormatInt(int64(x.FormatID), 10)))
	data = wire.AppendComplexLenBytes(data, x.GlobalID)
	data = wire.AppendComplexLenBytes(data, x.BranchID)
	return wire.Parameter{Type: 160, TypeMetadata: meta, Data: data}
}

func oracle(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, r *Request) error {
	if r.Op == Recover {
		return oracleRecover(ctx, op, resolver, r)
	}
	// Oracle association requires autocommit off; restore only after confirmed END.
	if r.Op == Start && op.Protocol().OK.Status&2 != 0 {
		if _, err := command.ExecOK(ctx, op, []byte("\x03SET autocommit=0")); err != nil {
			op.Discard()
			return err
		}
	}
	verbs := [...]string{"XA_START", "XA_END", "XA_PREPARE", "XA_COMMIT", "XA_ROLLBACK"}
	params := []wire.Parameter{{Type: 6}, nativeXID(r.XID)}
	args := "?"
	switch r.Op {
	case Start, End:
		flags := r.Flags
		if flags == Fail {
			flags = Success
		}
		params, args = append(params, wire.Parameter{Type: 8, Data: binary.LittleEndian.AppendUint64(nil, uint64(int64(flags)))}), "?, ?"
	case Commit:
		flag := byte(0)
		if r.OnePhase {
			flag = 1
		}
		params, args = append(params, wire.Parameter{Type: 1, Data: []byte{flag}}), "?, ?"
	}
	s, err := statement.NativeCallRaw(ctx, op, "BEGIN ? := SYS.DBMS_XA."+verbs[r.Op]+"("+args+"); END;", params, 1)
	if err != nil {
		if s.Seen {
			return unknown(op, r, err)
		}
		return failure(op, r, err, s.Started)
	}
	if s.ResultItems != 2 || len(s.Values) != 1 {
		return failure(op, r, wire.ErrMalformed, true)
	}
	code, err := integer(s.Values[0])
	if err != nil || code < -2147483648 || code > 2147483647 {
		return failure(op, r, wire.ErrMalformed, true)
	}
	if code != 0 && (r.Op != Prepare || code != 3) {
		// No association guarantee follows an RM failure; preserve its code and discard.
		op.Discard()
		return &dberrors.XAError{Code: int32(code), Err: fmt.Errorf("DBMS_XA.%s", verbs[r.Op])}
	}
	r.Vote = int32(code)
	if r.Op == End {
		if err = command.RestoreAutocommit(ctx, op); err != nil {
			op.Discard()
			return fmt.Errorf("%w: XA END confirmed; autocommit restoration failed: %w", dberrors.ErrTxCleanup, err)
		}
	}
	return nil
}

func oracleRecover(ctx context.Context, op *session.Operation, resolver *catalog.Resolver, r *Request) error {
	s, err := statement.NativeCallRaw(ctx, op, "DECLARE x SYS.DBMS_XA_XID_ARRAY := SYS.DBMS_XA_XID_ARRAY(); BEGIN x := SYS.DBMS_XA.XA_RECOVER(); ? := x; END;", []wire.Parameter{{Type: 6}}, 1)
	if err != nil {
		if s.Seen || len(s.Rows) != 0 {
			return unknown(op, r, err)
		}
		return failure(op, r, err, s.Started)
	}
	if s.ResultItems != 2 || !recoveryColumn(s.Columns) || len(s.Rows) != 1 {
		op.Discard()
		return wire.ErrMalformed
	}
	if err = resolver.DescribeColumns(ctx, op, s.Columns); err != nil {
		return unknown(op, r, err)
	}
	fields, err := wire.DecodeBinaryRow(s.Rows[0], s.Columns)
	if err != nil {
		return unknown(op, r, err)
	}
	value, err := materialize.New(op, resolver).ComplexValue(ctx, s.Columns[0], fields[0])
	if err != nil {
		return unknown(op, r, err)
	}
	r.XIDs, err = decodeOracleRecovery([]driver.Value{value}, op.Config().Limits)
	if err != nil {
		op.Discard()
	}
	return err
}

func recoveryColumn(columns []wire.Column) bool {
	if len(columns) != 1 || columns[0].Type != 160 {
		return false
	}
	c := columns[0]
	return c.ComplexSchema == xaSchema && c.ComplexName == xaArrayType || c.ComplexName == "" && c.ElementType == 160 && c.ElementSchema == xaSchema && c.ElementName == xaXIDType
}

func decodeOracleRecovery(values []driver.Value, limits config.Limits) ([]XID, error) {
	if len(values) != 1 {
		return nil, wire.ErrMalformed
	}
	var a types.Array
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
		o, ok := elem.(types.Object)
		if !ok || o.Null || o.Schema != xaSchema || o.Type != xaXIDType || len(o.Fields) != 3 || o.Fields[0].Type != valueformat.TypeNameNumber || o.Fields[1].Type != valueformat.TypeNameRaw || o.Fields[2].Type != valueformat.TypeNameRaw {
			return nil, wire.ErrMalformed
		}
		f, ok := o.Fields[0].Value.(types.Number)
		g, gok := o.Fields[1].Value.(types.Raw)
		b, bok := o.Fields[2].Value.(types.Raw)
		// Oracle RAW may represent a zero-length branch qualifier as SQL NULL.
		bok = bok || o.Fields[2].Value == nil
		format, err := strconv.ParseInt(string(f), 10, 32)
		x := XID{FormatID: int32(format), GlobalID: []byte(g), BranchID: []byte(b)}
		if !ok || !gok || !bok || err != nil || !ValidXID(x) {
			return nil, wire.ErrMalformed
		}
		n := int64(len(g) + len(b) + 4)
		if n > limits.MaxMessageBytes-size {
			return nil, wire.ErrMessageTooLarge
		}
		size += n
		out = append(out, CloneXID(x))
	}
	return out, nil
}
