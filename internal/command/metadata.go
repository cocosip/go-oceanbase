package command

import (
	"context"

	"github.com/cocosip/go-oceanbase/config"
	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/valuecodec"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// ReadMetadata decodes bounded column metadata and observes its final status.
func ReadMetadata(ctx context.Context, op *session.Operation, count int, budget *int64, validate bool) ([]wire.Column, error) {
	columns := make([]wire.Column, 0, min(count, 64))
	cfg, p := op.Config(), op.Protocol()
	for i := 0; i < count; i++ {
		data, err := op.Packets().ReadMessage(ctx)
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > cfg.Limits.MaxMessageBytes-*budget {
			return nil, wire.ErrMessageTooLarge
		}
		*budget += int64(len(data))
		column, err := valuecodec.DecodeColumn(data, cfg.Session, p.NewMetadata)
		if err != nil {
			return nil, err
		}
		if validate && valuecodec.TextTypeName(column, p.Mode, cfg.Session) == "" {
			return nil, &dberrors.UnsupportedTypeError{Type: column.Type, Mode: p.Mode}
		}
		columns = append(columns, column)
	}
	if count > 0 && p.Capabilities&wire.ClientDeprecateEOF == 0 {
		data, err := op.Packets().ReadMessage(ctx)
		if err != nil {
			return nil, err
		}
		eof, err := wire.DecodeEOF(data)
		if err != nil {
			return nil, err
		}
		if p.Mode == config.ModeOracle && !validate && eof.Status&3 == 3 {
			eof.Status = eof.Status&^3 | op.Protocol().OK.Status&3
		}
		ok := op.Protocol().OK
		ok.Status, ok.Warnings = eof.Status, eof.Warnings
		op.SetOK(ok)
	}
	return columns, nil
}
