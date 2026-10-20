package execution

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/cocosip/go-oceanbase/dberrors"
	"github.com/cocosip/go-oceanbase/internal/charset"
	"github.com/cocosip/go-oceanbase/internal/command"
	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/sqltext"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
	"github.com/cocosip/go-oceanbase/internal/wire"
)

// Import executes an already claimed input; the public boundary preserves the original claim timing.
func (e *Engine) Import(ctx context.Context, query string, input *streaminput.Infile) (command.Result, error) {
	rows, err := e.Query(ctx, query, nil, Options{Input: input})
	if err != nil {
		return command.Result{}, streaminput.InfileError(err)
	}
	if err = rows.Drain(ctx); err != nil {
		return command.Result{}, streaminput.InfileError(err)
	}
	return rows.Last(), nil
}

func (e *Engine) validateImport(op *session.Operation, query string, input *streaminput.Infile) error {
	if input == nil {
		return nil
	}
	p := op.Protocol()
	if err := sqltext.LocalImportStatement(query, p.Mode, p.OK.Status&512 != 0); err != nil {
		return err
	}
	if p.Capabilities&wire.ClientLocalFiles == 0 {
		return dberrors.ErrUnsupported
	}
	if e.control.ActiveTransaction() != nil || p.OK.Status&1 != 0 || p.OK.Status&2 == 0 || e.xa.Used() {
		return fmt.Errorf("%w: LOCAL INFILE requires autocommit without an active transaction", dberrors.ErrUnsupported)
	}
	return nil
}

func (r *Rows) uploadInfile(ctx context.Context, request []byte) (err error) {
	input := r.input
	r.input = nil
	if input == nil || r.op.Protocol().Capabilities&wire.ClientLocalFiles == 0 || len(request) < 2 || len(request) > 4097 {
		return dberrors.ErrLocalInfile
	}
	encoding := r.op.Config().Session.Charset
	if encoding == "" {
		encoding = "utf8mb4"
	}
	name, err := charset.Decode(encoding, request[1:])
	if err != nil || !streaminput.ValidInfileName(name) || input.Name != "" && input.Name != name {
		return dberrors.ErrLocalInfile
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	source := input.Reader
	var owned io.ReadCloser
	defer func() {
		if owned != nil {
			err = errors.Join(err, owned.Close())
		}
		err = streaminput.InfileError(err)
	}()
	if input.Open != nil {
		owned, err = input.Open(ctx, name)
		if streaminput.NilReader(owned) {
			owned = nil
			return errors.Join(dberrors.ErrLocalInfile, err)
		}
		if err != nil {
			return err
		}
		source = owned
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = r.op.Config().Limits.MaxLOBBytes
	}
	err = command.UploadInfile(ctx, r.op, source, owned, limit)
	owned = nil
	return err
}
