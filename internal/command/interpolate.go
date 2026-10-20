package command

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Interpolate binds already normalized logical values to a pure text command.
// It performs no user conversion, stream reads, or protocol I/O.
func Interpolate(op *session.Operation, query string, args []driver.NamedValue) ([]byte, error) {
	limit := op.Config().Limits.MaxMessageBytes - 1
	if int64(utf8.RuneCountInString(query)) > limit {
		return nil, wire.ErrMessageTooLarge
	}
	p := op.Protocol()
	plan, err := sqltext.BuildBindPlanEscapes(query, p.Mode, p.OK.Status&512 != 0)
	if err != nil {
		return nil, err
	}
	if len(args) != plan.NumInput {
		return nil, fmt.Errorf("%w: argument count", dberrors.ErrBind)
	}
	names := make(map[string]int, len(args))
	for i, arg := range args {
		if plan.Named {
			if arg.Name == "" {
				return nil, fmt.Errorf("%w: named argument required", dberrors.ErrBind)
			}
			key := strings.ToUpper(arg.Name)
			if _, ok := names[key]; ok {
				return nil, fmt.Errorf("%w: duplicate named argument", dberrors.ErrBind)
			}
			names[key] = i
		} else if arg.Name != "" {
			return nil, fmt.Errorf("%w: unexpected named argument", dberrors.ErrBind)
		}
	}
	values := make([]any, len(plan.Slots))
	for i, slot := range plan.Slots {
		index := slot.Index
		if plan.Named {
			var ok bool
			index, ok = names[slot.Name]
			if !ok {
				return nil, fmt.Errorf("%w: missing named argument", dberrors.ErrBind)
			}
		}
		values[i] = args[index].Value
	}
	return InterpolatePlan(op, plan, values)
}

// InterpolatePlan shares the original per-span charset and byte budget rules.
// Values are normalized physical slots; higher layers reject streams first.
func InterpolatePlan(op *session.Operation, plan sqltext.Plan, values []any) ([]byte, error) {
	if len(values) != len(plan.Slots) {
		return nil, fmt.Errorf("%w: argument count", dberrors.ErrBind)
	}
	cfg, p := op.Config(), op.Protocol()
	query := plan.Query
	limit := cfg.Limits.MaxMessageBytes - 1
	var b strings.Builder
	offset := 0
	for i, slot := range plan.Slots {
		span, err := EncodeSQL(query[offset:slot.Start], cfg.Session.Charset, limit-int64(b.Len()))
		if err != nil {
			return nil, err
		}
		b.Write(span)
		literal, err := valuecodec.ClientLiteral(values[i], p.Mode, cfg.Session, cfg.Limits.MaxLOBBytes, limit-int64(b.Len())-2)
		if err != nil {
			return nil, err
		}
		encoded, err := EncodeSQL(literal, cfg.Session.Charset, limit-int64(b.Len())-2)
		if err != nil {
			return nil, err
		}
		// Preserve token boundaries, including unary minus and numeric aliases.
		b.WriteByte(' ')
		b.Write(encoded)
		b.WriteByte(' ')
		offset = slot.End
	}
	tail, err := EncodeSQL(query[offset:], cfg.Session.Charset, limit-int64(b.Len()))
	if err != nil {
		return nil, err
	}
	b.Write(tail)
	return []byte(b.String()), nil
}
