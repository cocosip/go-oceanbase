package statement

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// LongDataParameter validates an unconsumed source without reading or claiming it.
func LongDataParameter(op *session.Operation, source *streaminput.Source) (wire.Parameter, error) {
	cfg, p := op.Config(), op.Protocol()
	return streaminput.Parameter(source, p.Mode, SupportsPrepareExecute(p.OBVersion), cfg.Limits.MaxLOBBytes, cfg.Limits.MaxMessageBytes)
}

// UploadLongData prevalidates all physical slots before claiming any one-use source.
// Repeated source copies share one read and fan each piece out in physical-slot order.
// Readers stay caller-owned; an upload failure discards the physical session and blocks replay.
func UploadLongData(ctx context.Context, op *session.Operation, id uint32, values []any) (err error) {
	type group struct {
		source  *streaminput.Source
		indices []uint16
	}
	groups := make(map[*atomic.Bool]*group)
	var order []*group
	for i, value := range values {
		source, ok := value.(*streaminput.Source)
		if !ok {
			continue
		}
		if _, err = LongDataParameter(op, source); err != nil {
			return err
		}
		if i > 65535 {
			return wire.ErrMalformed
		}
		g := groups[source.State]
		if g == nil {
			g = &group{source: source}
			groups[source.State] = g
			order = append(order, g)
		}
		g.indices = append(g.indices, uint16(i))
	}
	if len(order) == 0 {
		return nil
	}
	defer func() {
		if err == nil {
			return
		}
		op.Discard()
		if errors.Is(err, driver.ErrBadConn) {
			err = fmt.Errorf("%w: %v", dberrors.ErrLongData, err)
		} else {
			err = errors.Join(dberrors.ErrLongData, err)
		}
	}()
	cfg, p := op.Config(), op.Protocol()
	for _, g := range order {
		if !g.source.Claim() {
			return dberrors.ErrLongData
		}
		plan := streaminput.NewUploadPlan(ctx, *g.source, p.Mode, cfg.Session.Charset, cfg.Limits.MaxLOBBytes, cfg.Limits.MaxMessageBytes)
		for {
			piece, e := plan.Next()
			if e != nil {
				return e
			}
			for _, index := range g.indices {
				if err = sendLongPiece(ctx, op, id, index, piece); err != nil {
					return err
				}
			}
			if piece.Final {
				break
			}
		}
	}
	return nil
}

func sendLongPiece(ctx context.Context, op *session.Operation, id uint32, index uint16, piece streaminput.Piece) error {
	b := binary.LittleEndian.AppendUint32([]byte{24}, id)
	b = binary.LittleEndian.AppendUint16(b, index)
	oracle := op.Protocol().Mode == config.ModeOracle
	if oracle {
		b[0] = 162
		b = append(b, piece.Kind, 0)
		b = binary.LittleEndian.AppendUint64(b, uint64(len(piece.Data)))
	}
	b = append(b, piece.Data...)
	if _, err := command.Start(ctx, op, b, false); err != nil {
		return err
	}
	defer op.EndResponse()
	if err := ctx.Err(); err != nil {
		return err
	}
	if !oracle {
		return nil
	}
	response, err := op.Packets().ReadMessage(ctx)
	if err != nil {
		return err
	}
	if len(response) > 0 && response[0] == 255 {
		return command.DecodeServerError(response)
	}
	ok, err := wire.DecodeOK(response, op.Protocol().Capabilities)
	if err != nil {
		return err
	}
	if ok.Status&8 != 0 {
		return wire.ErrMalformed
	}
	op.SetOK(ok)
	return nil
}
