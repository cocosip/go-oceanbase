package command

import (
	"context"
	"errors"
	"io"

	"github.com/cocosip/go-oceanbase/internal/session"
	"github.com/cocosip/go-oceanbase/internal/streaminput"
)

// UploadInfile writes bounded continuation packets without reading or acknowledging the final OK.
// It closes an owned callback reader before the terminal packet, including on upload failure.
func UploadInfile(ctx context.Context, op *session.Operation, source io.Reader, owned io.ReadCloser, limit int64) (err error) {
	defer func() {
		if owned != nil {
			err = errors.Join(err, owned.Close())
		}
	}()
	reader := streaminput.NewBoundedReader(ctx, source, -1, limit)
	buf := make([]byte, int(min(int64(65536), op.Config().Limits.MaxMessageBytes)))
	for {
		n, readErr := streaminput.ReadChunk(reader, buf)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if n > 0 {
			if err = op.Packets().WriteContinuation(ctx, buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if owned != nil {
		err = owned.Close()
		owned = nil
		if err != nil {
			return err
		}
	}
	return op.Packets().WriteContinuation(ctx, nil)
}
